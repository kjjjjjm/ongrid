package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// capture serves reply to every request and records the last request body.
type capture struct {
	srv   *httptest.Server
	body  []byte
	query string
	sig   string
}

func newCapture(t *testing.T, reply string) *capture {
	t.Helper()
	c := &capture{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.body, _ = io.ReadAll(r.Body)
		c.query = r.URL.RawQuery
		c.sig = r.Header.Get("X-Ongrid-Signature")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *capture) payload(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(c.body, &out); err != nil {
		t.Fatalf("decode payload %s: %v", c.body, err)
	}
	return out
}

func rendered(content string, format Format) Message {
	return Message{
		Subject:  "CPU high on web-01",
		Severity: SeverityCritical,
		Rendered: &Rendered{Content: content, Format: format},
	}
}

func TestWeComSender_RenderedMarkdownUsesTheMarkdownMessageType(t *testing.T) {
	c := newCapture(t, `{"errcode":0,"errmsg":"ok"}`)
	if err := NewWeComSender("wecom", c.srv.URL, c.srv.Client()).Send(context.Background(), rendered("**CPU** 93%", FormatMarkdown)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	p := c.payload(t)
	markdown, _ := p["markdown"].(map[string]any)
	if p["msgtype"] != "markdown" || markdown["content"] != "**CPU** 93%" {
		t.Fatalf("payload = %v", p)
	}
}

func TestWeComSender_KeepsTextWithinTheBotLimit(t *testing.T) {
	c := newCapture(t, `{"errcode":0,"errmsg":"ok"}`)
	long := Message{Subject: strings.Repeat("磁盘", 2000)}
	if err := NewWeComSender("wecom", c.srv.URL, c.srv.Client()).Send(context.Background(), long); err != nil {
		t.Fatalf("Send: %v", err)
	}
	text, _ := c.payload(t)["text"].(map[string]any)
	content, _ := text["content"].(string)
	if len(content) > wecomTextMaxBytes || !utf8.ValidString(content) || !strings.HasSuffix(content, "…") {
		t.Fatalf("content: %d bytes, valid=%v", len(content), utf8.ValidString(content))
	}
}

// Bots answer HTTP 200 for most failures; the errcode in the body is the
// only signal that nothing was delivered.
func TestIMBotSenders_ReportErrcodeAsFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reply  string
		sender func(url string, c *http.Client) Sender
		want   string
	}{
		{"wecom", `{"errcode":93000,"errmsg":"invalid webhook url"}`, func(u string, c *http.Client) Sender { return NewWeComSender("w", u, c) }, "errcode=93000"},
		{"dingtalk", `{"errcode":310000,"errmsg":"sign not match"}`, func(u string, c *http.Client) Sender { return NewDingTalkSender("d", u, "s", c) }, "errcode=310000"},
		{"feishu", `{"code":19021,"msg":"sign match fail"}`, func(u string, c *http.Client) Sender { return NewFeishuSender("f", u, "s", c) }, "code=19021"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCapture(t, tc.reply)
			err := tc.sender(c.srv.URL, c.srv.Client()).Send(context.Background(), Message{Subject: "x"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestIMBotSenders_AcceptSuccessAndNonJSONReplies(t *testing.T) {
	for _, reply := range []string{`{"errcode":0,"errmsg":"ok"}`, `{"code":0,"msg":"success"}`, "ok", ""} {
		c := newCapture(t, reply)
		for _, s := range []Sender{
			NewWeComSender("w", c.srv.URL, c.srv.Client()),
			NewDingTalkSender("d", c.srv.URL, "", c.srv.Client()),
			NewFeishuSender("f", c.srv.URL, "", c.srv.Client()),
		} {
			if err := s.Send(context.Background(), Message{Subject: "x"}); err != nil {
				t.Fatalf("%s with reply %q: %v", s.Name(), reply, err)
			}
		}
	}
}

func TestDingTalkSender_RenderedMarkdownCarriesTheTitle(t *testing.T) {
	c := newCapture(t, `{"errcode":0}`)
	if err := NewDingTalkSender("d", c.srv.URL, "secret", c.srv.Client()).Send(context.Background(), rendered("### CPU\n- 93%", FormatMarkdown)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	p := c.payload(t)
	markdown, _ := p["markdown"].(map[string]any)
	if p["msgtype"] != "markdown" || markdown["title"] != "CPU high on web-01" || markdown["text"] != "### CPU\n- 93%" {
		t.Fatalf("payload = %v", p)
	}
	if !strings.Contains(c.query, "sign=") {
		t.Fatalf("signature missing from %q", c.query)
	}
}

func TestFeishuSender_RenderedMarkdownSendsASignedCard(t *testing.T) {
	c := newCapture(t, `{"code":0}`)
	if err := NewFeishuSender("f", c.srv.URL, "secret", c.srv.Client()).Send(context.Background(), rendered("**CPU** 93%", FormatMarkdown)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	p := c.payload(t)
	if p["msg_type"] != "interactive" || p["sign"] == nil || p["timestamp"] == nil {
		t.Fatalf("payload = %v", p)
	}
	card, _ := p["card"].(map[string]any)
	header, _ := card["header"].(map[string]any)
	title, _ := header["title"].(map[string]any)
	elements, _ := card["elements"].([]any)
	if title["content"] != "CPU high on web-01" || header["template"] != "red" || len(elements) != 1 {
		t.Fatalf("card = %v", card)
	}
	if el, _ := elements[0].(map[string]any); el["tag"] != "markdown" || el["content"] != "**CPU** 93%" {
		t.Fatalf("element = %v", elements[0])
	}
}

func TestFeishuSender_RenderedTextStaysText(t *testing.T) {
	c := newCapture(t, `{"code":0}`)
	if err := NewFeishuSender("f", c.srv.URL, "", c.srv.Client()).Send(context.Background(), rendered("CPU 93%", FormatText)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	p := c.payload(t)
	content, _ := p["content"].(map[string]any)
	if p["msg_type"] != "text" || content["text"] != "CPU 93%" {
		t.Fatalf("payload = %v", p)
	}
}

func TestSlackSender_RenderedContentReplacesTheFields(t *testing.T) {
	c := newCapture(t, "ok")
	if err := NewSlackSender("s", c.srv.URL, c.srv.Client()).Send(context.Background(), rendered("*CPU* 93%", FormatText)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	p := c.payload(t)
	attachments, _ := p["attachments"].([]any)
	att, _ := attachments[0].(map[string]any)
	if att["text"] != "*CPU* 93%" || att["title"] != "CPU high on web-01" || att["color"] != "#d92f2f" {
		t.Fatalf("attachment = %v", att)
	}
	if _, hasFields := att["fields"]; hasFields {
		t.Fatalf("a rendered message must not add the default fields: %v", att)
	}
}

func TestTelegramSender_RenderedContentIsTheText(t *testing.T) {
	c := newCapture(t, `{"ok":true}`)
	if err := NewTelegramSender("t", c.srv.URL, "-100", c.srv.Client()).Send(context.Background(), rendered("CPU 93%", FormatText)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if p := c.payload(t); p["text"] != "CPU 93%" || p["chat_id"] != "-100" {
		t.Fatalf("payload = %v", p)
	}
}

func TestGenericWebhook_RenderedBodyIsSentAndSigned(t *testing.T) {
	c := newCapture(t, "")
	body := `{"title": "CPU high", "value": 93.5}`
	if err := NewGenericWebhookSender("w", c.srv.URL, "secret", c.srv.Client()).Send(context.Background(), rendered(body, FormatText)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if string(c.body) != `{"title":"CPU high","value":93.5}` {
		t.Fatalf("body = %s", c.body)
	}
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(c.body)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); c.sig != want {
		t.Fatalf("signature = %q, want %q (over the bytes sent)", c.sig, want)
	}

	err := NewGenericWebhookSender("w", c.srv.URL, "", c.srv.Client()).Send(context.Background(), rendered(`{"title":`, FormatText))
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("invalid JSON body: err = %v", err)
	}
}

// The default webhook body is the Message JSON; Rendered must never leak
// into it.
func TestGenericWebhook_DefaultBodyOmitsRendered(t *testing.T) {
	b, err := json.Marshal(Message{Subject: "x", Rendered: &Rendered{Content: "secret layout"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret layout") || strings.Contains(string(b), "Rendered") {
		t.Fatalf("message JSON = %s", b)
	}
}
