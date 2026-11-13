package imbridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatprotocol"
)

type oneShotSender struct {
	messages []string
}

func (s *oneShotSender) SendText(_ context.Context, _, _, text string) (string, error) {
	s.messages = append(s.messages, text)
	return "sent", nil
}

func TestStreamEditor_WhenSenderCannotEdit_SendsFinalMessageOnce(t *testing.T) {
	sender := &oneShotSender{}
	editor := newStreamEditor(context.Background(), sender, "chat", "chat_id", "", "en",
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	editor.OnEvent(chatprotocol.Event{Type: chatprotocol.EventAssistant, Assistant: &chatprotocol.AssistantEvent{Content: "partial"}})
	editor.OnEvent(chatprotocol.Event{Type: chatprotocol.EventAssistant, Assistant: &chatprotocol.AssistantEvent{Content: "final"}})
	editor.OnEvent(chatprotocol.Event{Type: chatprotocol.EventDone})
	if err := editor.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	if len(sender.messages) != 1 || sender.messages[0] != "final" {
		t.Fatalf("messages = %#v, want one final response", sender.messages)
	}
}

// recordingEditor records every platform call in order. It implements
// MessageEditor and, when finalize is set, MessageFinalizer.
type recordingEditor struct {
	calls []string
}

func (s *recordingEditor) SendText(_ context.Context, _, _, text string) (string, error) {
	s.calls = append(s.calls, "send:"+text)
	return "m-1", nil
}

func (s *recordingEditor) EditText(_ context.Context, id, text string) error {
	s.calls = append(s.calls, "edit:"+id+":"+text)
	return nil
}

type finalizingEditor struct{ recordingEditor }

func (s *finalizingEditor) FinishText(_ context.Context, id, text string) error {
	s.calls = append(s.calls, "finish:"+id+":"+text)
	return nil
}

func assistant(text string) chatprotocol.Event {
	return chatprotocol.Event{Type: chatprotocol.EventAssistant, Assistant: &chatprotocol.AssistantEvent{Content: text}}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func equalCalls(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("calls = %#v, want %#v", got, want)
		}
	}
}

func TestStreamEditor_WhenSenderFinalizes_ClosesTheMessageOnFlush(t *testing.T) {
	sender := &finalizingEditor{}
	editor := newStreamEditor(context.Background(), sender, "chat", "group", "m-1", "", discard())

	editor.OnEvent(assistant("partial"))
	editor.OnEvent(chatprotocol.Event{Type: chatprotocol.EventDone})
	if err := editor.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if err := editor.Flush(); err != nil {
		t.Fatalf("second Flush() error = %v", err)
	}

	equalCalls(t, sender.calls, []string{"edit:m-1:partial", "edit:m-1:partial", "finish:m-1:partial"})
}

func TestStreamEditor_WhenSenderFinalizes_OnFatalClosesWithTheError(t *testing.T) {
	sender := &finalizingEditor{}
	editor := newStreamEditor(context.Background(), sender, "chat", "group", "m-1", "en", discard())

	if err := editor.OnFatal(errors.New("model unavailable")); err != nil {
		t.Fatalf("OnFatal() error = %v", err)
	}

	equalCalls(t, sender.calls, []string{"finish:m-1:⚠ Assistant failed: model unavailable"})
}

func TestStreamEditor_WhenRunProducesNoText_ClosesThePlaceholderAsIs(t *testing.T) {
	sender := &finalizingEditor{}
	editor := newStreamEditor(context.Background(), sender, "chat", "group", "m-1", "", discard())

	if err := editor.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	equalCalls(t, sender.calls, []string{"finish:m-1:"})
}

func TestStreamEditor_WhenPlaceholderFailed_SendsThenClosesWithoutResending(t *testing.T) {
	sender := &finalizingEditor{}
	editor := newStreamEditor(context.Background(), sender, "chat", "group", "", "", discard())

	editor.mu.Lock()
	editor.buf = "final"
	editor.mu.Unlock()
	if err := editor.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	equalCalls(t, sender.calls, []string{"send:final", "finish:m-1:"})
}

// Editable providers without a finalizer keep their behavior: the final
// flush is an ordinary edit.
func TestStreamEditor_WhenSenderOnlyEdits_FinalFlushIsAnEdit(t *testing.T) {
	sender := &recordingEditor{}
	editor := newStreamEditor(context.Background(), sender, "chat", "chat_id", "m-1", "", discard())

	editor.OnEvent(assistant("answer"))
	if err := editor.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	equalCalls(t, sender.calls, []string{"edit:m-1:answer", "edit:m-1:answer"})
}
