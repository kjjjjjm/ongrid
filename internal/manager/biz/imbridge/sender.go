package imbridge

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatprotocol"
)

// Sender is the minimum platform-agnostic outbound surface. Providers
// that can replace an existing message may also implement MessageEditor.
type Sender interface {
	// SendText creates a new text message in the target chat.
	SendText(ctx context.Context, receiveID, receiveIDType, text string) (messageID string, err error)
}

// MessageEditor is implemented by providers such as Feishu, Telegram,
// and Slack. DingTalk's session webhook only creates messages, so it
// intentionally does not implement this interface.
type MessageEditor interface {
	// EditText replaces the body of a previously-sent text message.
	EditText(ctx context.Context, messageID, text string) error
}

// MessageFinalizer is implemented by editable providers whose messages stay
// open until they are explicitly closed. WeCom AI-bot stream replies keep a
// "generating" state until a frame with finish=true arrives, and refuse
// further edits afterwards. The bridge calls FinishText exactly once per
// message it created, in place of the last EditText. An empty text closes the
// message and keeps its current content.
type MessageFinalizer interface {
	FinishText(ctx context.Context, messageID, text string) error
}

// streamEditor turns the SSE-shape chatprotocol.Event stream into throttled
// EditText calls on the platform. It assumes the assistant text grows
// monotonically — we always send the full accumulated buffer, never
// deltas, because Feishu / DingTalk both replace rather than append on
// edit.
type streamEditor struct {
	ctx           context.Context
	sender        Sender
	editor        MessageEditor
	finalizer     MessageFinalizer // nil unless the provider must close its messages
	chatID        string
	receiveIDType string
	messageID     string // initially the placeholder id; "" if placeholder send failed
	locale        string // app.DefaultLocale, drives the OnFatal apology language
	log           *slog.Logger

	mu     sync.Mutex
	buf    string
	lastAt time.Time
	// finished is set by the final flush (Flush / OnFatal). A finalized
	// message rejects edits, so any later flush is a no-op.
	finished bool
	// throttle: every 800ms or 200 char delta
}

const (
	editIntervalMs   = 800
	editCharsTrigger = 200
)

func newStreamEditor(ctx context.Context, sender Sender, chatID, receiveIDType, placeholderMessageID, locale string, log *slog.Logger) *streamEditor {
	editor, _ := sender.(MessageEditor)
	var finalizer MessageFinalizer
	if editor != nil {
		finalizer, _ = sender.(MessageFinalizer)
	}
	return &streamEditor{
		ctx:           ctx,
		sender:        sender,
		editor:        editor,
		finalizer:     finalizer,
		chatID:        chatID,
		receiveIDType: receiveIDType,
		messageID:     placeholderMessageID,
		locale:        locale,
		log:           log,
	}
}

// OnEvent is wired as the emit callback for chatprotocol.RunStreamWithOpts.
// We only care about EventAssistant (text chunks) and EventDone
// (terminal — force-flush). Tool calls / task notifications are
// suppressed in IM for now; they'd be too noisy as inline chat
// messages.
func (e *streamEditor) OnEvent(ev chatprotocol.Event) {
	switch ev.Type {
	case chatprotocol.EventAssistant:
		// Pull the text content out of the event. chatprotocol.Event for
		// assistant carries the persisted assistant turn — we
		// concatenate its Content field. If the runtime later
		// exposes a per-chunk delta we should switch to that to
		// avoid quadratic edit traffic.
		text := assistantText(ev)
		if text == "" {
			return
		}
		e.mu.Lock()
		e.buf = text
		shouldFlush := e.editor != nil && e.shouldFlushLocked()
		e.mu.Unlock()
		if shouldFlush {
			e.flush(false)
		}
	case chatprotocol.EventDone:
		e.flush(false) // terminal: ensure last chunk lands
	default:
		// EventToolStart / EventToolEnd / EventTaskNotification —
		// surfaced in the web UI's per-turn timeline; in IM they'd
		// fragment the message and confuse users. Drop for now.
	}
}

// OnFatal is called by the bridge when the agent run aborted — we
// replace the placeholder with the error so the IM user gets a clear
// signal instead of a stale "思考中…".
func (e *streamEditor) OnFatal(err error) error {
	prefix := "⚠ 助手执行失败："
	if strings.ToLower(strings.TrimSpace(e.locale)) == "en" {
		prefix = "⚠ Assistant failed: "
	}
	e.mu.Lock()
	e.buf = prefix + err.Error()
	e.mu.Unlock()
	return e.flush(true)
}

// Flush is the bridge's final post-run nudge — ensures the last buf
// has been written even if the agent terminated without emitting Done.
// It also closes the message for providers that implement MessageFinalizer.
func (e *streamEditor) Flush() error { return e.flush(true) }

func (e *streamEditor) shouldFlushLocked() bool {
	if time.Since(e.lastAt) >= editIntervalMs*time.Millisecond {
		return true
	}
	return false
}

// flush writes the current buffer to the platform. final marks the bridge's
// last write for this reply (Flush / OnFatal); for a MessageFinalizer it
// becomes FinishText instead of EditText so the message is closed. Providers
// without a finalizer behave the same for final and intermediate flushes.
func (e *streamEditor) flush(final bool) error {
	e.mu.Lock()
	if e.finished {
		e.mu.Unlock()
		return nil
	}
	buf := e.buf
	mid := e.messageID
	e.lastAt = time.Now()
	closing := final && e.finalizer != nil
	if closing {
		e.finished = true
	}
	e.mu.Unlock()
	if buf == "" {
		if closing && mid != "" {
			// The run produced no text. Close the placeholder as it is
			// instead of leaving it in a generating state.
			return e.finish(mid, "")
		}
		return nil
	}
	if mid == "" {
		// No placeholder exists. This is the normal path for providers
		// without message editing and the fallback path after a failed
		// placeholder send.
		newID, err := e.sender.SendText(e.ctx, e.chatID, e.receiveIDType, buf)
		if err != nil {
			e.log.Warn("imbridge: fallback send failed", slog.Any("err", err))
			return err
		}
		e.mu.Lock()
		e.messageID = newID
		e.mu.Unlock()
		if closing {
			// SendText already carried buf; close without resending it.
			return e.finish(newID, "")
		}
		return nil
	}
	if e.editor == nil {
		// A one-shot provider already sent its final response. EventDone
		// and the bridge's final Flush may both arrive; do not duplicate it.
		return nil
	}
	if closing {
		return e.finish(mid, buf)
	}
	if err := e.editor.EditText(e.ctx, mid, buf); err != nil {
		e.log.Warn("imbridge: edit failed", slog.String("message_id", mid), slog.Any("err", err))
		return err
	}
	return nil
}

func (e *streamEditor) finish(messageID, text string) error {
	if err := e.finalizer.FinishText(e.ctx, messageID, text); err != nil {
		e.log.Warn("imbridge: finish failed", slog.String("message_id", messageID), slog.Any("err", err))
		return err
	}
	return nil
}

// assistantText pulls the persisted assistant text out of an
// EventAssistant payload. The agent runtime stores the full
// accumulated assistant turn (not per-token deltas) on the event so we
// can just take it.
func assistantText(ev chatprotocol.Event) string {
	// chatprotocol.Event.Assistant is set on assistant events — see
	// internal/manager/biz/aiops/chatprotocol/types.go. We avoid a direct
	// type assertion on the field shape to stay forward-compatible:
	// if the runtime adds richer payloads (citations, attachments)
	// they live on the same Event struct.
	if ev.Assistant == nil {
		return ""
	}
	return ev.Assistant.Content
}
