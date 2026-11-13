package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// dialTimeout caps the WebSocket + TLS handshake.
	dialTimeout = 10 * time.Second
	// defaultAckTimeout caps the wait for WeCom to acknowledge one request.
	defaultAckTimeout = 10 * time.Second
	// writeTimeout caps one frame write on a congested socket.
	writeTimeout = 10 * time.Second
	// defaultPingInterval follows WeCom's recommendation of one ping every 30s.
	defaultPingInterval = 30 * time.Second
	// maxMissedPings consecutive pings without an ack mark the connection dead.
	maxMissedPings = 2
)

var (
	errConnClosed   = errors.New("wecom: connection closed")
	errNotConnected = errors.New("wecom: long connection is not established")
)

// conn is one authenticated long connection. Requests and acks are matched by
// req_id: WeCom echoes the req_id of each request and leaves cmd empty in the
// ack.
type conn struct {
	ws         *websocket.Conn
	log        *slog.Logger
	ackTimeout time.Duration

	writeMu sync.Mutex // gorilla/websocket supports one concurrent writer

	mu      sync.Mutex
	pending map[string]chan frame
	cause   error // why the connection closed; nil while it is open

	done      chan struct{}
	closeOnce sync.Once
}

// dialConfig carries the connection settings shared by StreamClient and Probe.
type dialConfig struct {
	url        string
	ackTimeout time.Duration // zero: defaultAckTimeout
	log        *slog.Logger  // nil: slog.Default()
}

// connect dials the endpoint and subscribes with the bot credentials. A
// returned conn is authenticated: WeCom has acknowledged the subscription.
func connect(ctx context.Context, cfg dialConfig, botID, secret string) (*conn, error) {
	dialer := *websocket.DefaultDialer // keeps Proxy: http.ProxyFromEnvironment
	dialer.HandshakeTimeout = dialTimeout
	ws, _, err := dialer.DialContext(ctx, cfg.url, nil)
	if err != nil {
		return nil, fmt.Errorf("wecom ws dial: %w", err)
	}
	if cfg.log == nil {
		cfg.log = slog.Default()
	}
	if cfg.ackTimeout <= 0 {
		cfg.ackTimeout = defaultAckTimeout
	}
	c := &conn{
		ws:         ws,
		log:        cfg.log,
		ackTimeout: cfg.ackTimeout,
		pending:    map[string]chan frame{},
		done:       make(chan struct{}),
	}
	// ReadMessage takes no context, so closing the socket is the only way to
	// unblock the handshake read when ctx is cancelled.
	stop := context.AfterFunc(ctx, func() { c.close(ctx.Err()) })
	defer stop()
	if err := c.subscribe(ctx, botID, secret); err != nil {
		c.close(err)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return c, nil
}

// subscribe sends aibot_subscribe and reads frames until its ack arrives.
// WeCom pushes no callbacks before the subscription succeeds, so nothing else
// needs dispatching here.
func (c *conn) subscribe(ctx context.Context, botID, secret string) error {
	reqID := newID(cmdSubscribe)
	if err := c.write(request{
		Cmd:     cmdSubscribe,
		Headers: frameHeaders{ReqID: reqID},
		Body:    subscribeBody{BotID: botID, Secret: secret},
	}); err != nil {
		return fmt.Errorf("wecom %s: %w", cmdSubscribe, err)
	}
	deadline := time.Now().Add(c.ackTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.ws.SetReadDeadline(deadline); err != nil {
		return fmt.Errorf("wecom %s: %w", cmdSubscribe, err)
	}
	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			return fmt.Errorf("wecom %s: waiting for ack: %w", cmdSubscribe, err)
		}
		var f frame
		if json.Unmarshal(raw, &f) != nil || f.Cmd != "" || f.Headers.ReqID != reqID {
			continue
		}
		if f.ErrCode != 0 {
			return &APIError{Op: cmdSubscribe, Code: f.ErrCode, Msg: f.ErrMsg}
		}
		return c.ws.SetReadDeadline(time.Time{})
	}
}

// request sends one frame and waits for its ack. A non-zero errcode is
// returned as *APIError. Only one request per req_id may wait at a time.
func (c *conn) request(ctx context.Context, op, reqID string, body any) error {
	ch := make(chan frame, 1)
	c.mu.Lock()
	if c.cause != nil {
		cause := c.cause
		c.mu.Unlock()
		return fmt.Errorf("wecom %s: %w", op, cause)
	}
	if _, busy := c.pending[reqID]; busy {
		c.mu.Unlock()
		return fmt.Errorf("wecom %s: request %s is already waiting for an ack", op, reqID)
	}
	c.pending[reqID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, reqID)
		c.mu.Unlock()
	}()

	if err := c.write(request{Cmd: op, Headers: frameHeaders{ReqID: reqID}, Body: body}); err != nil {
		return fmt.Errorf("wecom %s: %w", op, err)
	}
	timer := time.NewTimer(c.ackTimeout)
	defer timer.Stop()
	select {
	case f := <-ch:
		if f.ErrCode != 0 {
			return &APIError{Op: op, Code: f.ErrCode, Msg: f.ErrMsg}
		}
		return nil
	case <-c.done:
		return fmt.Errorf("wecom %s: closed before the ack: %w", op, c.err())
	case <-timer.C:
		return fmt.Errorf("wecom %s: no ack within %s", op, c.ackTimeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// next returns the next callback frame. Acks are handed to the request
// waiting for them; frames that are not JSON are logged and skipped.
func (c *conn) next() (frame, error) {
	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			if cause := c.err(); cause != nil {
				return frame{}, cause
			}
			return frame{}, fmt.Errorf("wecom ws read: %w", err)
		}
		var f frame
		if err := json.Unmarshal(raw, &f); err != nil {
			c.log.Warn("wecom: undecodable frame", slog.Any("err", err))
			continue
		}
		if f.Cmd == "" {
			c.deliver(f)
			continue
		}
		return f, nil
	}
}

// deliver hands an ack to its waiting request. Acks that arrive after the
// request gave up are dropped.
func (c *conn) deliver(f frame) {
	c.mu.Lock()
	ch, ok := c.pending[f.Headers.ReqID]
	c.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- f:
	default: // a duplicate ack; the first one already answered the request
	}
}

// heartbeat pings every interval and closes the connection after
// maxMissedPings consecutive pings fail, so a silently dead connection turns
// into a read error instead of hanging.
func (c *conn) heartbeat(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	missed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-ticker.C:
		}
		err := c.request(ctx, cmdPing, newID(cmdPing), nil)
		if err == nil {
			missed = 0
			continue
		}
		missed++
		c.log.Warn("wecom: heartbeat failed", slog.Int("missed", missed), slog.Any("err", err))
		if missed >= maxMissedPings {
			c.close(fmt.Errorf("wecom heartbeat: %d pings in a row failed: %w", missed, err))
			return
		}
	}
}

func (c *conn) write(v request) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

// close shuts the socket down once and records why; requests waiting for an
// ack return immediately. gorilla documents Close as safe to call
// concurrently with ReadMessage.
func (c *conn) close(cause error) {
	c.closeOnce.Do(func() {
		if cause == nil {
			cause = errConnClosed
		}
		c.mu.Lock()
		c.cause = cause
		c.mu.Unlock()
		close(c.done)
		_ = c.ws.Close()
	})
}

func (c *conn) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cause
}
