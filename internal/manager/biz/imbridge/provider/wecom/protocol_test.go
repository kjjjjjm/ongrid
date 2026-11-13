package wecom

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

func decodeCallback(t *testing.T, raw string) callbackBody {
	t.Helper()
	var body callbackBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode callback: %v", err)
	}
	return body
}

func TestInboundFromCallback_GroupTextStripsLeadingMention(t *testing.T) {
	body := decodeCallback(t, `{
		"msgid": "m-1", "aibotid": "aib-1", "chatid": "wrk-group", "chattype": "group",
		"from": {"userid": "zhangsan"}, "msgtype": "text",
		"text": {"content": "@Ongrid助手 web-01 的磁盘满了吗？@lisi 也看一下"}
	}`)

	in, ok := inboundFromCallback("aib-1", body)
	if !ok {
		t.Fatal("group text message was not bridged")
	}
	if in.Provider != model.ProviderWeCom || in.AppID != "aib-1" {
		t.Fatalf("provider/app = %q/%q", in.Provider, in.AppID)
	}
	if in.ChatID != "wrk-group" || in.ReceiveIDType != chatTypeGroup {
		t.Fatalf("chat = %q (%s), want the group chatid", in.ChatID, in.ReceiveIDType)
	}
	if in.OpenID != "zhangsan" || in.EventID != "m-1" {
		t.Fatalf("sender/event = %q/%q", in.OpenID, in.EventID)
	}
	// Only the leading mention goes; the later one is part of the question.
	if in.Text != "web-01 的磁盘满了吗？@lisi 也看一下" {
		t.Fatalf("text = %q", in.Text)
	}
}

func TestInboundFromCallback_DirectChatUsesSenderAsChat(t *testing.T) {
	body := decodeCallback(t, `{
		"msgid": "m-2", "chattype": "single", "from": {"userid": "lisi"},
		"msgtype": "text", "text": {"content": "/new"}
	}`)

	in, ok := inboundFromCallback("aib-1", body)
	if !ok {
		t.Fatal("direct message was not bridged")
	}
	if in.ChatID != "lisi" || in.ReceiveIDType != chatTypeSingle {
		t.Fatalf("chat = %q (%s), want the sender userid", in.ChatID, in.ReceiveIDType)
	}
	if in.Text != "/new" {
		t.Fatalf("text = %q", in.Text)
	}
}

func TestInboundFromCallback_VoiceMixedAndQuote(t *testing.T) {
	voice := decodeCallback(t, `{"msgid":"v","chattype":"single","from":{"userid":"u"},
		"msgtype":"voice","voice":{"content":"查一下告警"}}`)
	if in, ok := inboundFromCallback("aib", voice); !ok || in.Text != "查一下告警" {
		t.Fatalf("voice: ok=%v text=%q", ok, in.Text)
	}

	mixed := decodeCallback(t, `{"msgid":"x","chattype":"group","chatid":"g","from":{"userid":"u"},
		"msgtype":"mixed","mixed":{"msg_item":[
			{"msgtype":"text","text":{"content":"@bot 这张图里的报错"}},
			{"msgtype":"image","image":{"url":"https://example.invalid/i"}},
			{"msgtype":"text","text":{"content":"是什么原因"}}]}}`)
	if in, ok := inboundFromCallback("aib", mixed); !ok || in.Text != "这张图里的报错\n是什么原因" {
		t.Fatalf("mixed: ok=%v text=%q", ok, in.Text)
	}

	quoted := decodeCallback(t, `{"msgid":"q","chattype":"group","chatid":"g","from":{"userid":"u"},
		"msgtype":"text","text":{"content":"@bot 这个报错怎么处理"},
		"quote":{"msgtype":"text","text":{"content":"OOMKilled\nexit code 137"}}}`)
	in, ok := inboundFromCallback("aib", quoted)
	if !ok {
		t.Fatal("quoted message was not bridged")
	}
	if want := "> OOMKilled\n> exit code 137\n\n这个报错怎么处理"; in.Text != want {
		t.Fatalf("quote: text = %q, want %q", in.Text, want)
	}
}

func TestInboundFromCallback_RejectsWhatTheBridgeCannotAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"image", `{"msgid":"i","chattype":"single","from":{"userid":"u"},"msgtype":"image","image":{"url":"https://example.invalid"}}`},
		{"only a mention", `{"msgid":"o","chattype":"group","chatid":"g","from":{"userid":"u"},"msgtype":"text","text":{"content":"@bot"}}`},
		{"group without chatid", `{"msgid":"n","chattype":"group","from":{"userid":"u"},"msgtype":"text","text":{"content":"hi"}}`},
		{"event", `{"msgid":"e","from":{"userid":"u"},"msgtype":"event","event":{"eventtype":"enter_chat"}}`},
		{"ai assistant invocation", `{"msgid":"c","chattype":"single","from":{"userid":"u"},"msgtype":"text","text":{"content":"hi"},"compat_info":{"source":"ai_assistant"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if in, ok := inboundFromCallback("aib", decodeCallback(t, tc.raw)); ok {
				t.Fatalf("bridged %+v, want it skipped", in)
			}
		})
	}
}

func TestFitContent_TruncatesOnRuneBoundaryWithMarker(t *testing.T) {
	short := "磁盘使用率 93%"
	if got := fitContent(short, ""); got != short {
		t.Fatalf("short content changed: %q", got)
	}

	long := strings.Repeat("磁", maxContentBytes) // 3 bytes per rune
	got := fitContent(long, "")
	if len(got) > maxContentBytes {
		t.Fatalf("len = %d, want <= %d", len(got), maxContentBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a UTF-8 sequence")
	}
	if !strings.HasSuffix(got, "已截断）") {
		t.Fatalf("missing zh truncation marker: %q", got[len(got)-40:])
	}
	if en := fitContent(long, "en"); !strings.HasSuffix(en, "(truncated: reply too long)") {
		t.Fatalf("missing en truncation marker: %q", en[len(en)-40:])
	}
}

func TestSendChatType(t *testing.T) {
	if sendChatType(chatTypeGroup) != sendChatTypeGroup || sendChatType(chatTypeSingle) != sendChatTypeSingle ||
		sendChatType("") != sendChatTypeAuto {
		t.Fatal("chat type mapping changed")
	}
}
