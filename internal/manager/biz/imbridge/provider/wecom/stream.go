package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	bizbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// errReplaced ends Run when WeCom moves the bot to a newer connection.
var errReplaced = errors.New("wecom: connection replaced — another client subscribed with this BotID " +
	"(a second ongrid manager, or another tool using the same bot); WeCom keeps one long connection per bot")

// streamRefreshWindow is how long a stream message accepts updates. WeCom ends
// a stream 10 minutes after its first frame; the margin keeps the final frame
// clear of that deadline.
const streamRefreshWindow = 9*time.Minute + 30*time.Second

// Option customizes a StreamClient or Probe.
type Option func(*options)

type options struct {
	url string
}

// WithURL overrides the long-connection endpoint, for privately deployed
// WeCom installations and tests.
func WithURL(url string) Option {
	return func(o *options) {
		if url = strings.TrimSpace(url); url != "" {
			o.url = url
		}
	}
}

func applyOptions(opts []Option) options {
	o := options{url: DefaultURL}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Probe validates a BotID / Secret pair by subscribing on a fresh connection
// and closing it again. WeCom keeps one connection per bot, so a successful
// probe evicts a connection that is already open for the same bot; callers
// skip the probe while the bot is known to be connected.
func Probe(ctx context.Context, botID, secret string, opts ...Option) error {
	if strings.TrimSpace(botID) == "" || strings.TrimSpace(secret) == "" {
		return errors.New("wecom: BotID and Secret required")
	}
	c, err := connect(ctx, dialConfig{url: applyOptions(opts).url}, botID, secret)
	if err != nil {
		return err
	}
	c.close(nil)
	return nil
}

// StreamClient owns the long connection of one WeCom bot. The supervisor calls
// Run again after it returns, so the client tracks the current connection and
// replies go out on whichever connection is live when they are sent. WeCom
// accepts replies to a callback for 24 hours, so a reconnect during an agent
// run does not strand the answer.
type StreamClient struct {
	app          *model.ImApp
	bridge       *bizbridge.Bridge
	hub          *Hub
	url          string
	allowed      map[string]struct{} // empty: any member of the enterprise
	pingInterval time.Duration
	ackTimeout   time.Duration // zero: defaultAckTimeout
	log          *slog.Logger

	mu   sync.Mutex
	live *conn
}

// NewStreamClient builds the client for one WeCom ImApp (app_id = BotID,
// app_secret = long-connection Secret). hub may be nil when proactive sends
// are not wired.
func NewStreamClient(app *model.ImApp, bridge *bizbridge.Bridge, hub *Hub, log *slog.Logger, opts ...Option) (*StreamClient, error) {
	if app == nil || strings.TrimSpace(app.AppID) == "" || strings.TrimSpace(app.AppSecret) == "" {
		return nil, errors.New("wecom: BotID (app_id) and long-connection Secret (app_secret) required")
	}
	if log == nil {
		log = slog.Default()
	}
	allowed := make(map[string]struct{})
	for _, id := range bizbridge.ParseAllowFrom(app.AllowFrom) {
		allowed[id] = struct{}{}
	}
	return &StreamClient{
		app:          app,
		bridge:       bridge,
		hub:          hub,
		url:          applyOptions(opts).url,
		allowed:      allowed,
		pingInterval: defaultPingInterval,
		log:          log.With(slog.String("provider", model.ProviderWeCom), slog.Uint64("im_app_id", app.ID)),
	}, nil
}

// ProviderName satisfies bizbridge.StreamClient.
func (c *StreamClient) ProviderName() string { return model.ProviderWeCom }

// Run connects, subscribes and handles callbacks until ctx is cancelled or the
// connection ends. Every other exit returns an error, so the supervisor
// reconnects with backoff and shows the reason as the stream's last error.
func (c *StreamClient) Run(ctx context.Context) (err error) {
	cn, err := connect(ctx, dialConfig{url: c.url, ackTimeout: c.ackTimeout, log: c.log}, c.app.AppID, c.app.AppSecret)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	stop := context.AfterFunc(ctx, func() { cn.close(ctx.Err()) })
	c.setLive(cn)
	c.hub.attach(c.app.ID, c)
	c.log.Info("wecom long connection subscribed")

	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		cn.heartbeat(ctx, c.pingInterval)
	}()
	defer func() {
		c.hub.detach(c.app.ID, c)
		c.setLive(nil)
		stop()
		cn.close(err)
		<-heartbeatDone
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	return c.readLoop(cn)
}

func (c *StreamClient) readLoop(cn *conn) error {
	for {
		f, err := cn.next()
		if err != nil {
			return err
		}
		switch f.Cmd {
		case cmdMsgCallback:
			c.handleMessage(f)
		case cmdEventCallback:
			if eventType(f.Body) == eventDisconnected {
				c.log.Warn("wecom: connection replaced by a newer subscription for this bot")
				return errReplaced
			}
			// enter_chat, template_card_event and feedback_event need no
			// action from the bridge.
		default:
			c.log.Debug("wecom: ignoring frame", slog.String("cmd", f.Cmd))
		}
	}
}

func eventType(body json.RawMessage) string {
	var b callbackBody
	if json.Unmarshal(body, &b) != nil || b.Event == nil {
		return ""
	}
	return b.Event.EventType
}

func (c *StreamClient) handleMessage(f frame) {
	var body callbackBody
	if err := json.Unmarshal(f.Body, &body); err != nil {
		c.log.Warn("wecom: decode message callback", slog.Any("err", err))
		return
	}
	in, ok := inboundFromCallback(c.app.AppID, body)
	c.log.Info("wecom message callback received",
		slog.String("msgtype", body.MsgType),
		slog.String("chattype", body.ChatType),
		slog.String("chat_id", body.ChatID),
		slog.String("user_id", body.From.UserID),
		slog.Bool("bridged", ok))
	if !ok || f.Headers.ReqID == "" || c.bridge == nil {
		return
	}
	if !c.permits(in.OpenID) {
		c.log.Warn("wecom inbound from non-allowlisted sender — ignored",
			slog.String("user_id", in.OpenID),
			slog.String("chat_id", in.ChatID))
		return
	}
	reply := &streamReply{
		client:   c,
		reqID:    f.Headers.ReqID,
		chatID:   in.ChatID,
		chatType: sendChatType(in.ReceiveIDType),
		locale:   c.app.DefaultLocale,
		now:      time.Now,
		started:  map[string]time.Time{},
	}
	// Detach: agent runs take 30s+, and the read loop must keep reading the
	// acks the reply is waiting for.
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				c.log.Error("wecom bridge panic recovered",
					slog.Any("panic", recovered),
					slog.String("stack", string(debug.Stack())))
			}
		}()
		if err := c.bridge.HandleInbound(context.Background(), reply, in); err != nil {
			c.log.Warn("wecom bridge handle_inbound failed", slog.Any("err", err))
		}
	}()
}

func (c *StreamClient) permits(userID string) bool {
	if len(c.allowed) == 0 {
		return true
	}
	_, ok := c.allowed[userID]
	return ok
}

func (c *StreamClient) setLive(cn *conn) {
	c.mu.Lock()
	c.live = cn
	c.mu.Unlock()
}

func (c *StreamClient) connection() *conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live
}

// push sends a proactive Markdown message. WeCom delivers it only to chats in
// which a user has already messaged the bot.
func (c *StreamClient) push(ctx context.Context, chatID string, chatType int, markdown string) error {
	if strings.TrimSpace(chatID) == "" {
		return errors.New("wecom: chat id required")
	}
	cn := c.connection()
	if cn == nil {
		return errNotConnected
	}
	return cn.request(ctx, cmdSendMsg, newID(cmdSendMsg), sendMsgBody{
		ChatID:   chatID,
		ChatType: chatType,
		MsgType:  "markdown",
		Markdown: markdownBody{Content: fitContent(markdown, c.app.DefaultLocale)},
	})
}

// streamReply answers one message callback. Every reply frame carries the
// callback's req_id and the stream id of the message it creates or updates,
// so one streamReply serves the bridge's placeholder, edits and final answer.
// Calls are serialized because an ack only echoes the req_id.
type streamReply struct {
	client   *StreamClient
	reqID    string
	chatID   string
	chatType int
	locale   string
	now      func() time.Time

	mu      sync.Mutex
	started map[string]time.Time // stream id → time of its first frame
}

// SendText opens a new stream message showing text.
func (r *streamReply) SendText(ctx context.Context, _, _ string, text string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := newID("stream")
	if err := r.respond(ctx, id, text, false); err != nil {
		return "", err
	}
	r.started[id] = r.now()
	return id, nil
}

// EditText replaces the content of an open stream message.
func (r *streamReply) EditText(ctx context.Context, messageID, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	started, ok := r.started[messageID]
	if !ok {
		return fmt.Errorf("wecom: stream %q is not open", messageID)
	}
	if r.now().Sub(started) >= streamRefreshWindow {
		// WeCom has ended, or is about to end, this stream. Skip the update;
		// FinishText delivers the final answer as a new message.
		return nil
	}
	return r.respond(ctx, messageID, text, false)
}

// FinishText closes a stream message. A non-empty text becomes the final
// content; when the stream can no longer be updated it is pushed as a new
// message so the answer is not lost.
func (r *streamReply) FinishText(ctx context.Context, messageID, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	started, ok := r.started[messageID]
	if !ok {
		return fmt.Errorf("wecom: stream %q is not open", messageID)
	}
	delete(r.started, messageID)
	if r.now().Sub(started) < streamRefreshWindow {
		err := r.respond(ctx, messageID, text, true)
		var apiErr *APIError
		// Push only when WeCom explicitly rejected the frame. A timeout may
		// still have been delivered, and a push would duplicate the answer.
		if err == nil || text == "" || !errors.As(err, &apiErr) {
			return err
		}
		r.client.log.Warn("wecom: finishing the stream was rejected; pushing the reply as a new message", slog.Any("err", err))
	}
	if text == "" {
		return nil
	}
	return r.client.push(ctx, r.chatID, r.chatType, text)
}

func (r *streamReply) respond(ctx context.Context, streamID, text string, finish bool) error {
	cn := r.client.connection()
	if cn == nil {
		return errNotConnected
	}
	return cn.request(ctx, cmdRespondMsg, r.reqID, respondBody{
		MsgType: "stream",
		Stream:  streamBody{ID: streamID, Finish: finish, Content: fitContent(text, r.locale)},
	})
}

// NewStreamFactory returns the factory main.go registers for the "wecom"
// provider. All clients it builds attach to hub while they are connected.
func NewStreamFactory(log *slog.Logger, hub *Hub) bizbridge.StreamClientFactory {
	return func(app *model.ImApp, bridge *bizbridge.Bridge) (bizbridge.StreamClient, error) {
		return NewStreamClient(app, bridge, hub, log)
	}
}
