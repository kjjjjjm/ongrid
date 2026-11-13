// Package wecom is the 企业微信 (WeCom) provider for the IM bridge. It uses
// the long-connection option of WeCom AI bots (智能机器人) in API mode: the
// manager dials wss://openws.work.weixin.qq.com, authenticates with the bot's
// BotID and long-connection Secret (aibot_subscribe), receives
// aibot_msg_callback frames for group @-mentions and direct chats, and answers
// with aibot_respond_msg stream frames that WeCom renders as one Markdown
// message updated in place. No public callback URL, Token or EncodingAESKey
// is needed, and the frames themselves are not encrypted.
//
// Protocol reference (智能机器人长连接):
// https://developer.work.weixin.qq.com/document/path/101463
package wecom

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	bizbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// DefaultURL is the public WeCom long-connection endpoint. Privately deployed
// WeCom installations show their own address in the admin console.
const DefaultURL = "wss://openws.work.weixin.qq.com"

// Commands from the long-connection protocol.
const (
	cmdSubscribe     = "aibot_subscribe"
	cmdPing          = "ping"
	cmdMsgCallback   = "aibot_msg_callback"
	cmdEventCallback = "aibot_event_callback"
	cmdRespondMsg    = "aibot_respond_msg"
	cmdSendMsg       = "aibot_send_msg"
)

// eventDisconnected is pushed to the old connection when another client
// subscribes with the same BotID; WeCom closes the old connection right after.
const eventDisconnected = "disconnected_event"

// maxContentBytes is WeCom's limit for stream and markdown content, in UTF-8
// bytes.
const maxContentBytes = 20480

// Chat types reported by callbacks. aibot_send_msg takes the numeric form:
// 1 = direct chat (chatid is the user's userid), 2 = group. 0 lets WeCom try
// the group form first and fall back to a direct chat.
const (
	chatTypeSingle = "single"
	chatTypeGroup  = "group"

	sendChatTypeAuto   = 0
	sendChatTypeSingle = 1
	sendChatTypeGroup  = 2
)

type frameHeaders struct {
	ReqID string `json:"req_id"`
}

// frame is the {cmd, headers, body} envelope WeCom pushes to us. Acks for our
// own requests carry no cmd: only headers.req_id plus errcode / errmsg.
type frame struct {
	Cmd     string          `json:"cmd,omitempty"`
	Headers frameHeaders    `json:"headers"`
	Body    json.RawMessage `json:"body,omitempty"`
	ErrCode int             `json:"errcode,omitempty"`
	ErrMsg  string          `json:"errmsg,omitempty"`
}

// request is a frame we send. Body is omitted for ping.
type request struct {
	Cmd     string       `json:"cmd"`
	Headers frameHeaders `json:"headers"`
	Body    any          `json:"body,omitempty"`
}

type subscribeBody struct {
	BotID  string `json:"bot_id"`
	Secret string `json:"secret"`
}

type streamBody struct {
	ID      string `json:"id"`
	Finish  bool   `json:"finish"`
	Content string `json:"content,omitempty"`
}

type respondBody struct {
	MsgType string     `json:"msgtype"`
	Stream  streamBody `json:"stream"`
}

type markdownBody struct {
	Content string `json:"content"`
}

type sendMsgBody struct {
	ChatID   string       `json:"chatid"`
	ChatType int          `json:"chat_type,omitempty"`
	MsgType  string       `json:"msgtype"`
	Markdown markdownBody `json:"markdown"`
}

type textContent struct {
	Content string `json:"content"`
}

type mixedContent struct {
	MsgItem []struct {
		MsgType string       `json:"msgtype"`
		Text    *textContent `json:"text"`
	} `json:"msg_item"`
}

// callbackBody is the subset of an aibot_msg_callback / aibot_event_callback
// body the bridge uses. Image, file and video messages are not bridged.
type callbackBody struct {
	MsgID    string `json:"msgid"`
	AIBotID  string `json:"aibotid"`
	ChatID   string `json:"chatid"` // only set for group chats
	ChatType string `json:"chattype"`
	From     struct {
		UserID string `json:"userid"`
	} `json:"from"`
	MsgType string        `json:"msgtype"`
	Text    *textContent  `json:"text"`
	Voice   *textContent  `json:"voice"` // speech already transcribed by WeCom
	Mixed   *mixedContent `json:"mixed"`
	Quote   *struct {
		MsgType string        `json:"msgtype"`
		Text    *textContent  `json:"text"`
		Voice   *textContent  `json:"voice"`
		Mixed   *mixedContent `json:"mixed"`
	} `json:"quote"`
	Event *struct {
		EventType string `json:"eventtype"`
	} `json:"event"`
	// CompatInfo marks callbacks that are not plain user messages (AI
	// assistant invocations, channels). Those need reply fields the bridge
	// does not produce, so they are skipped.
	CompatInfo json.RawMessage `json:"compat_info"`
}

// APIError is a non-zero errcode returned in the ack of one of our requests.
type APIError struct {
	Op   string
	Code int
	Msg  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("wecom %s: errcode=%d errmsg=%s", e.Op, e.Code, e.Msg)
}

// inboundFromCallback normalizes one message callback. ok is false for
// callbacks the bridge does not handle: events, non-text messages, AI
// assistant / channel invocations and messages that carry no question.
func inboundFromCallback(appID string, body callbackBody) (in bizbridge.InboundMessage, ok bool) {
	if body.Event != nil || hasCompatInfo(body.CompatInfo) {
		return bizbridge.InboundMessage{}, false
	}
	userID := strings.TrimSpace(body.From.UserID)
	chatID := strings.TrimSpace(body.ChatID)
	chatType := chatTypeSingle
	if body.ChatType == chatTypeGroup {
		chatType = chatTypeGroup
	} else {
		// Direct chats carry no chatid: the conversation is the user.
		chatID = userID
	}
	if chatID == "" {
		return bizbridge.InboundMessage{}, false
	}
	text := stripLeadingMentions(messageText(body.MsgType, body.Text, body.Voice, body.Mixed))
	if text == "" {
		return bizbridge.InboundMessage{}, false
	}
	if body.Quote != nil {
		if quoted := strings.TrimSpace(messageText(body.Quote.MsgType, body.Quote.Text, body.Quote.Voice, body.Quote.Mixed)); quoted != "" {
			text = quoteMarkdown(quoted) + "\n\n" + text
		}
	}
	return bizbridge.InboundMessage{
		Provider:      model.ProviderWeCom,
		AppID:         appID,
		ChatID:        chatID,
		OpenID:        userID,
		UserName:      userID, // callbacks carry no display name
		Text:          text,
		EventID:       body.MsgID,
		ReceiveIDType: chatType,
	}, true
}

func hasCompatInfo(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null"
}

// messageText extracts the readable text of a text, voice or mixed message.
// Images inside a mixed message are dropped; the bridge is text-only.
func messageText(msgType string, text, voice *textContent, mixed *mixedContent) string {
	switch msgType {
	case "text":
		if text != nil {
			return strings.TrimSpace(text.Content)
		}
	case "voice":
		if voice != nil {
			return strings.TrimSpace(voice.Content)
		}
	case "mixed":
		if mixed == nil {
			return ""
		}
		parts := make([]string, 0, len(mixed.MsgItem))
		for _, item := range mixed.MsgItem {
			if item.MsgType == "text" && item.Text != nil {
				if s := strings.TrimSpace(item.Text.Content); s != "" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// stripLeadingMentions removes the "@BotName " prefix WeCom puts in front of
// group messages, so "/new" still parses as a command and the agent does not
// read the bot name as part of the question. Only leading mentions are
// removed: a mention later in the sentence is part of what the user wrote.
func stripLeadingMentions(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "@") {
		i := strings.IndexFunc(s, unicode.IsSpace)
		if i < 0 {
			return "" // the message is only a mention
		}
		s = strings.TrimSpace(s[i:])
	}
	return s
}

func quoteMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

// fitContent keeps a reply within WeCom's content limit. A cut lands on a
// rune boundary and ends with a visible marker so the reader knows the reply
// is incomplete.
func fitContent(s, locale string) string {
	if len(s) <= maxContentBytes {
		return s
	}
	marker := "\n\n…（内容过长，已截断）"
	if strings.EqualFold(strings.TrimSpace(locale), "en") {
		marker = "\n\n… (truncated: reply too long)"
	}
	return truncateBytes(s, maxContentBytes-len(marker)) + marker
}

// truncateBytes returns the longest prefix of s that is at most max bytes and
// does not split a UTF-8 sequence.
func truncateBytes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// sendChatType maps a callback chat type onto the aibot_send_msg form.
func sendChatType(chatType string) int {
	switch chatType {
	case chatTypeGroup:
		return sendChatTypeGroup
	case chatTypeSingle:
		return sendChatTypeSingle
	default:
		return sendChatTypeAuto
	}
}

// newID returns prefix_<16 hex chars>. WeCom only requires req_id values to
// be unique per request; stream ids must be unique per reply.
func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error on supported platforms
	return prefix + "_" + hex.EncodeToString(b[:])
}
