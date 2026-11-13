package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatprotocol"
	bizbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

const testWait = 5 * time.Second

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeWeCom stands in for wss://openws.work.weixin.qq.com. It acks subscribe
// and ping, records every other request (acking it with rejectFn's errcode),
// and lets a test push callbacks to the subscribed client.
type fakeWeCom struct {
	srv    *httptest.Server
	botID  string
	secret string

	mu       sync.Mutex
	ackPings bool
	rejectFn func(received) int
	sessions []*fakeSession

	connected chan *fakeSession
	requests  chan received
}

type received struct {
	Cmd     string          `json:"cmd"`
	Headers frameHeaders    `json:"headers"`
	Body    json.RawMessage `json:"body"`
}

type fakeSession struct {
	ws     *websocket.Conn
	mu     sync.Mutex
	closed chan struct{}
}

func (s *fakeSession) write(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ws.WriteJSON(v)
}

func newFakeWeCom(t *testing.T) *fakeWeCom {
	t.Helper()
	f := &fakeWeCom{
		botID:     "aib-test",
		secret:    "long-connection-secret",
		ackPings:  true,
		connected: make(chan *fakeSession, 8),
		requests:  make(chan received, 64),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(func() {
		f.mu.Lock()
		for _, s := range f.sessions {
			_ = s.ws.Close()
		}
		f.mu.Unlock()
		f.srv.Close()
	})
	return f
}

func (f *fakeWeCom) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func ack(reqID string, code int, msg string) map[string]any {
	return map[string]any{"headers": map[string]string{"req_id": reqID}, "errcode": code, "errmsg": msg}
}

func (f *fakeWeCom) handle(w http.ResponseWriter, r *http.Request) {
	var upgrader websocket.Upgrader
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := &fakeSession{ws: ws, closed: make(chan struct{})}
	f.mu.Lock()
	f.sessions = append(f.sessions, s)
	f.mu.Unlock()
	defer close(s.closed)
	defer ws.Close()
	for {
		var in received
		if err := ws.ReadJSON(&in); err != nil {
			return
		}
		switch in.Cmd {
		case cmdSubscribe:
			var b subscribeBody
			_ = json.Unmarshal(in.Body, &b)
			if b.BotID != f.botID || b.Secret != f.secret {
				_ = s.write(ack(in.Headers.ReqID, 40001, "invalid bot_id or secret"))
				continue
			}
			_ = s.write(ack(in.Headers.ReqID, 0, "ok"))
			select {
			case f.connected <- s:
			default:
			}
		case cmdPing:
			f.mu.Lock()
			ackPings := f.ackPings
			f.mu.Unlock()
			if ackPings {
				_ = s.write(ack(in.Headers.ReqID, 0, "ok"))
			}
		default:
			f.mu.Lock()
			reject := f.rejectFn
			f.mu.Unlock()
			code := 0
			if reject != nil {
				code = reject(in)
			}
			f.requests <- in
			_ = s.write(ack(in.Headers.ReqID, code, "rejected by fake"))
		}
	}
}

func (f *fakeWeCom) waitConnected(t *testing.T) *fakeSession {
	t.Helper()
	select {
	case s := <-f.connected:
		return s
	case <-time.After(testWait):
		t.Fatal("client never subscribed")
		return nil
	}
}

func (f *fakeWeCom) nextRequest(t *testing.T) received {
	t.Helper()
	select {
	case req := <-f.requests:
		return req
	case <-time.After(testWait):
		t.Fatal("no request reached the fake WeCom server")
		return received{}
	}
}

func pushCallback(t *testing.T, s *fakeSession, cmd, reqID, body string) {
	t.Helper()
	if err := s.write(map[string]any{
		"cmd":     cmd,
		"headers": map[string]string{"req_id": reqID},
		"body":    json.RawMessage(body),
	}); err != nil {
		t.Fatalf("push %s: %v", cmd, err)
	}
}

func decodeStream(t *testing.T, req received) streamBody {
	t.Helper()
	if req.Cmd != cmdRespondMsg {
		t.Fatalf("cmd = %q, want %q (body %s)", req.Cmd, cmdRespondMsg, req.Body)
	}
	var body respondBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("decode respond body: %v", err)
	}
	if body.MsgType != "stream" {
		t.Fatalf("msgtype = %q, want stream", body.MsgType)
	}
	return body.Stream
}

func decodeSend(t *testing.T, req received) map[string]any {
	t.Helper()
	if req.Cmd != cmdSendMsg {
		t.Fatalf("cmd = %q, want %q (body %s)", req.Cmd, cmdSendMsg, req.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("decode send body: %v", err)
	}
	return body
}

type running struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func (r *running) wait(d time.Duration) bool {
	select {
	case <-r.done:
		return true
	case <-time.After(d):
		return false
	}
}

func startClient(t *testing.T, c *StreamClient) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan struct{})}
	go func() {
		r.err = c.Run(ctx)
		close(r.done)
	}()
	t.Cleanup(func() {
		cancel()
		if !r.wait(testWait) {
			t.Error("Run did not return after cancellation")
		}
	})
	return r
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testApp(f *fakeWeCom) *model.ImApp {
	return &model.ImApp{ID: 7, Provider: model.ProviderWeCom, Mode: model.ModeStream, Name: "ops", AppID: f.botID, AppSecret: f.secret, Enabled: true}
}

func newTestClient(t *testing.T, f *fakeWeCom, app *model.ImApp, bridge *bizbridge.Bridge, hub *Hub) *StreamClient {
	t.Helper()
	c, err := NewStreamClient(app, bridge, hub, discardLogger(), WithURL(f.url()))
	if err != nil {
		t.Fatalf("NewStreamClient: %v", err)
	}
	return c
}

// bridgeRepo is the in-memory imbridge.Repo behind a real Bridge.
type bridgeRepo struct {
	app     *model.ImApp
	mu      sync.Mutex
	threads []*model.ImThread
}

func (r *bridgeRepo) GetAppByAppID(_ context.Context, provider, appID string) (*model.ImApp, error) {
	if provider != r.app.Provider || appID != r.app.AppID {
		return nil, errors.New("app not found")
	}
	return r.app, nil
}

func (r *bridgeRepo) FindThread(_ context.Context, imAppID uint64, chatID, threadID string) (*model.ImThread, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, th := range r.threads {
		if th.ImAppID == imAppID && th.ImChatID == chatID && th.ImThreadID == threadID {
			return th, nil
		}
	}
	return nil, errors.New("thread not found")
}

func (r *bridgeRepo) CreateThread(_ context.Context, th *model.ImThread) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	th.ID = uint64(len(r.threads) + 1)
	r.threads = append(r.threads, th)
	return nil
}

func (r *bridgeRepo) TouchThread(context.Context, uint64) error { return nil }

func (r *bridgeRepo) RotateThreadSession(context.Context, uint64, string) error { return nil }

// scriptedAgent answers every question with the same assistant turns.
type scriptedAgent struct {
	replies []string
	asked   chan string
}

func (a *scriptedAgent) EnsureSession(context.Context, uint64, string) (string, error) {
	return "session-1", nil
}

func (a *scriptedAgent) StreamMessage(_ context.Context, _ string, content string, emit chatprotocol.Emit) error {
	a.asked <- content
	for _, reply := range a.replies {
		emit(chatprotocol.Event{Type: chatprotocol.EventAssistant, Assistant: &chatprotocol.AssistantEvent{Content: reply}})
	}
	emit(chatprotocol.Event{Type: chatprotocol.EventDone})
	return nil
}

func newBridge(app *model.ImApp, agent *scriptedAgent) *bizbridge.Bridge {
	return bizbridge.NewBridge(&bridgeRepo{app: app}, agent, 1, discardLogger())
}

func TestStreamClient_AnswersGroupMentionInOneStreamMessage(t *testing.T) {
	f := newFakeWeCom(t)
	app := testApp(f)
	agent := &scriptedAgent{replies: []string{"正在检查", "web-01 磁盘使用率 93%"}, asked: make(chan string, 1)}
	startClient(t, newTestClient(t, f, app, newBridge(app, agent), nil))
	s := f.waitConnected(t)

	pushCallback(t, s, cmdMsgCallback, "cb-1", `{"msgid":"m-1","aibotid":"aib-test","chatid":"wrk-1",
		"chattype":"group","from":{"userid":"zhangsan"},"msgtype":"text","text":{"content":"@Ongrid 磁盘满了吗"}}`)

	select {
	case question := <-agent.asked:
		if question != "磁盘满了吗" {
			t.Fatalf("agent asked %q, want the text without the mention", question)
		}
	case <-time.After(testWait):
		t.Fatal("agent was never asked")
	}

	var frames []streamBody
	for {
		req := f.nextRequest(t)
		if req.Headers.ReqID != "cb-1" {
			t.Fatalf("reply req_id = %q, want the callback's", req.Headers.ReqID)
		}
		frame := decodeStream(t, req)
		frames = append(frames, frame)
		if frame.Finish {
			break
		}
	}
	first, last := frames[0], frames[len(frames)-1]
	if first.Content != "✦ 思考中…" || first.Finish {
		t.Fatalf("first frame = %+v, want the open thinking placeholder", first)
	}
	if last.Content != "web-01 磁盘使用率 93%" {
		t.Fatalf("final content = %q", last.Content)
	}
	for _, fr := range frames {
		if fr.ID != first.ID {
			t.Fatalf("stream ids differ: %q vs %q — the reply split into several messages", fr.ID, first.ID)
		}
	}
}

func TestStreamClient_NewSessionCommandClosesItsConfirmation(t *testing.T) {
	f := newFakeWeCom(t)
	app := testApp(f)
	agent := &scriptedAgent{asked: make(chan string, 1)}
	startClient(t, newTestClient(t, f, app, newBridge(app, agent), nil))
	s := f.waitConnected(t)

	pushCallback(t, s, cmdMsgCallback, "cb-new", `{"msgid":"m-new","chattype":"single",
		"from":{"userid":"lisi"},"msgtype":"text","text":{"content":"/new"}}`)

	opened := decodeStream(t, f.nextRequest(t))
	if opened.Finish || !strings.Contains(opened.Content, "新会话") {
		t.Fatalf("first frame = %+v, want the open confirmation", opened)
	}
	closed := decodeStream(t, f.nextRequest(t))
	if !closed.Finish || closed.ID != opened.ID || closed.Content != "" {
		t.Fatalf("second frame = %+v, want finish of stream %q keeping its content", closed, opened.ID)
	}
	if len(agent.asked) != 0 {
		t.Fatal("/new must not reach the agent")
	}
}

func TestStreamClient_DropsSendersOutsideAllowFrom(t *testing.T) {
	f := newFakeWeCom(t)
	app := testApp(f)
	app.AllowFrom = "lisi"
	agent := &scriptedAgent{replies: []string{"ok"}, asked: make(chan string, 2)}
	startClient(t, newTestClient(t, f, app, newBridge(app, agent), nil))
	s := f.waitConnected(t)

	pushCallback(t, s, cmdMsgCallback, "cb-outsider", `{"msgid":"m-a","chattype":"single",
		"from":{"userid":"zhangsan"},"msgtype":"text","text":{"content":"hi"}}`)
	pushCallback(t, s, cmdMsgCallback, "cb-allowed", `{"msgid":"m-b","chattype":"single",
		"from":{"userid":"lisi"},"msgtype":"text","text":{"content":"hi"}}`)

	// Callbacks are handled in order, so the first reply must belong to the
	// allowlisted sender: the outsider's message was dropped.
	if req := f.nextRequest(t); req.Headers.ReqID != "cb-allowed" {
		t.Fatalf("first reply answered %q, want cb-allowed", req.Headers.ReqID)
	}
}

func TestStreamClient_ReturnsWhenAnotherClientTakesTheBot(t *testing.T) {
	f := newFakeWeCom(t)
	r := startClient(t, newTestClient(t, f, testApp(f), nil, nil))
	s := f.waitConnected(t)

	pushCallback(t, s, cmdEventCallback, "ev-1", `{"msgid":"d-1","aibotid":"aib-test","msgtype":"event",
		"event":{"eventtype":"disconnected_event"}}`)

	if !r.wait(testWait) {
		t.Fatal("Run kept going after WeCom replaced the connection")
	}
	if !errors.Is(r.err, errReplaced) {
		t.Fatalf("Run error = %v, want errReplaced", r.err)
	}
}

func TestStreamClient_HeartbeatWithoutAcksEndsTheConnection(t *testing.T) {
	f := newFakeWeCom(t)
	f.ackPings = false
	c := newTestClient(t, f, testApp(f), nil, nil)
	c.pingInterval = 20 * time.Millisecond
	c.ackTimeout = 50 * time.Millisecond
	r := startClient(t, c)
	f.waitConnected(t)

	if !r.wait(testWait) {
		t.Fatal("Run did not notice the dead connection")
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "heartbeat") {
		t.Fatalf("Run error = %v, want a heartbeat failure", r.err)
	}
}

// An idle long connection parks Run in ReadMessage, which takes no context;
// cancellation must close the socket so the supervisor never has to abandon
// the goroutine (#517).
func TestStreamClient_RunReturnsWhenContextCanceledWhileIdle(t *testing.T) {
	f := newFakeWeCom(t)
	r := startClient(t, newTestClient(t, f, testApp(f), nil, nil))
	s := f.waitConnected(t)
	time.Sleep(50 * time.Millisecond)

	r.cancel()
	if !r.wait(2 * time.Second) {
		t.Fatal("Run did not return after cancellation")
	}
	if !errors.Is(r.err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", r.err)
	}
	select {
	case <-s.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not close the websocket")
	}
}

func TestStreamClient_RejectedCredentialsSurfaceTheErrcode(t *testing.T) {
	f := newFakeWeCom(t)
	app := testApp(f)
	app.AppSecret = "wrong"
	r := startClient(t, newTestClient(t, f, app, nil, nil))

	if !r.wait(testWait) {
		t.Fatal("Run did not return after a rejected subscription")
	}
	var apiErr *APIError
	if !errors.As(r.err, &apiErr) || apiErr.Op != cmdSubscribe || apiErr.Code != 40001 {
		t.Fatalf("Run error = %v, want the subscribe errcode", r.err)
	}
}

func TestProbe(t *testing.T) {
	f := newFakeWeCom(t)
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()

	if err := Probe(ctx, f.botID, f.secret, WithURL(f.url())); err != nil {
		t.Fatalf("Probe with valid credentials: %v", err)
	}
	err := Probe(ctx, f.botID, "wrong", WithURL(f.url()))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 40001 {
		t.Fatalf("Probe with a wrong secret = %v, want errcode 40001", err)
	}
	if err := Probe(ctx, "", "", WithURL(f.url())); err == nil {
		t.Fatal("Probe without credentials should fail before dialing")
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func connectedReply(t *testing.T, f *fakeWeCom, clock *fakeClock) *streamReply {
	t.Helper()
	c := newTestClient(t, f, testApp(f), nil, nil)
	startClient(t, c)
	f.waitConnected(t)
	eventually(t, "the client to go live", func() bool { return c.connection() != nil })
	return &streamReply{
		client:   c,
		reqID:    "cb-9",
		chatID:   "wrk-9",
		chatType: sendChatTypeGroup,
		now:      clock.Now,
		started:  map[string]time.Time{},
	}
}

func TestStreamReply_PushesTheAnswerOnceTheStreamWindowHasPassed(t *testing.T) {
	f := newFakeWeCom(t)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	reply := connectedReply(t, f, clock)
	ctx := context.Background()

	id, err := reply.SendText(ctx, "", "", "✦ 思考中…")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	decodeStream(t, f.nextRequest(t))

	clock.advance(10 * time.Minute)
	if err := reply.EditText(ctx, id, "partial"); err != nil {
		t.Fatalf("EditText after the window: %v", err)
	}
	if err := reply.FinishText(ctx, id, "final answer"); err != nil {
		t.Fatalf("FinishText after the window: %v", err)
	}

	// The skipped edit sent nothing, so the next request is the push.
	body := decodeSend(t, f.nextRequest(t))
	markdown, _ := body["markdown"].(map[string]any)
	if body["chatid"] != "wrk-9" || body["chat_type"] != float64(sendChatTypeGroup) ||
		body["msgtype"] != "markdown" || markdown["content"] != "final answer" {
		t.Fatalf("push body = %v", body)
	}
}

func TestStreamReply_PushesTheAnswerWhenWeComRejectsTheFinishFrame(t *testing.T) {
	f := newFakeWeCom(t)
	f.rejectFn = func(in received) int {
		if in.Cmd == cmdRespondMsg && strings.Contains(string(in.Body), `"finish":true`) {
			return 846608
		}
		return 0
	}
	reply := connectedReply(t, f, &fakeClock{now: time.Now()})
	ctx := context.Background()

	id, err := reply.SendText(ctx, "", "", "✦ 思考中…")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if err := reply.FinishText(ctx, id, "final answer"); err != nil {
		t.Fatalf("FinishText: %v", err)
	}

	decodeStream(t, f.nextRequest(t)) // placeholder
	if finish := decodeStream(t, f.nextRequest(t)); !finish.Finish {
		t.Fatalf("second frame = %+v, want the (rejected) finish frame", finish)
	}
	body := decodeSend(t, f.nextRequest(t))
	if markdown, _ := body["markdown"].(map[string]any); markdown["content"] != "final answer" {
		t.Fatalf("push body = %v", body)
	}
	if err := reply.EditText(ctx, id, "late"); err == nil {
		t.Fatal("a finished stream must not accept edits")
	}
}

func TestHub_PushesThroughTheLiveConnection(t *testing.T) {
	f := newFakeWeCom(t)
	hub := NewHub()
	ctx := context.Background()
	if err := hub.Push(ctx, 7, "wrk-1", "hi"); err == nil {
		t.Fatal("Push without a live connection should fail")
	}

	r := startClient(t, newTestClient(t, f, testApp(f), nil, hub))
	f.waitConnected(t)
	eventually(t, "the hub to see the connection", func() bool { return hub.Connected(7, f.botID, f.secret) })
	if hub.Connected(7, f.botID, "rotated-secret") {
		t.Fatal("Connected must compare the credentials the client subscribed with")
	}

	if err := hub.Push(ctx, 7, "wrk-1", "**磁盘告警**"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	body := decodeSend(t, f.nextRequest(t))
	markdown, _ := body["markdown"].(map[string]any)
	if body["chatid"] != "wrk-1" || body["msgtype"] != "markdown" || markdown["content"] != "**磁盘告警**" {
		t.Fatalf("push body = %v", body)
	}
	if _, set := body["chat_type"]; set {
		t.Fatalf("hub pushes must let WeCom resolve the chat type, got chat_type=%v", body["chat_type"])
	}

	r.cancel()
	if !r.wait(testWait) {
		t.Fatal("Run did not return")
	}
	if hub.Connected(7, f.botID, f.secret) {
		t.Fatal("hub still lists a stopped client")
	}
}

func TestNewStreamFactory_RejectsMissingCredentials(t *testing.T) {
	factory := NewStreamFactory(discardLogger(), nil)
	if _, err := factory(&model.ImApp{Provider: model.ProviderWeCom, AppID: "aib"}, nil); err == nil {
		t.Fatal("factory accepted an app without a secret")
	}
	client, err := factory(&model.ImApp{Provider: model.ProviderWeCom, AppID: "aib", AppSecret: "s"}, nil)
	if err != nil || client.ProviderName() != model.ProviderWeCom {
		t.Fatalf("factory = %v, %v", client, err)
	}
}
