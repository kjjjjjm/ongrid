package imbridge

import (
	"context"
	"strings"
	"testing"

	managerimbridgemodel "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

type imAppGetterStub struct{ app *managerimbridgemodel.ImApp }

func (s imAppGetterStub) GetApp(context.Context, uint64) (*managerimbridgemodel.ImApp, error) {
	return s.app, nil
}

func TestIMMessageSender_WhenAppDisabled_RejectsBeforeProviderSend(t *testing.T) {
	sender := IMMessageSender{Apps: imAppGetterStub{app: &managerimbridgemodel.ImApp{Name: "ops", Enabled: false}}}
	err := sender.SendIMGroupMessage(context.Background(), 1, "oc_group", "hello")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error = %v, want disabled app", err)
	}
}

func TestIMMessageSender_WhenAppMissing_ReturnsNotFound(t *testing.T) {
	sender := IMMessageSender{Apps: imAppGetterStub{}}
	err := sender.SendIMGroupMessage(context.Background(), 42, "oc_group", "hello")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want missing app", err)
	}
}

type weComPushRecorder struct {
	appID        uint64
	chatID, text string
	calls        int
}

func (r *weComPushRecorder) Push(_ context.Context, imAppID uint64, chatID, markdown string) error {
	r.calls++
	r.appID, r.chatID, r.text = imAppID, chatID, markdown
	return nil
}

func TestIMMessageSender_WhenWeCom_PushesThroughTheLiveConnection(t *testing.T) {
	pusher := &weComPushRecorder{}
	sender := IMMessageSender{
		Apps:  imAppGetterStub{app: &managerimbridgemodel.ImApp{ID: 9, Name: "ops", Provider: managerimbridgemodel.ProviderWeCom, Enabled: true}},
		WeCom: pusher,
	}
	if err := sender.SendIMGroupMessage(context.Background(), 9, "wrk-group", "**hello**"); err != nil {
		t.Fatalf("SendIMGroupMessage() error = %v", err)
	}
	if pusher.calls != 1 || pusher.appID != 9 || pusher.chatID != "wrk-group" || pusher.text != "**hello**" {
		t.Fatalf("push = %+v", pusher)
	}
}

func TestIMMessageSender_WhenWeComNotWired_ExplainsWhy(t *testing.T) {
	sender := IMMessageSender{Apps: imAppGetterStub{app: &managerimbridgemodel.ImApp{ID: 9, Name: "ops", Provider: managerimbridgemodel.ProviderWeCom, Enabled: true}}}
	err := sender.SendIMGroupMessage(context.Background(), 9, "wrk-group", "hello")
	if err == nil || !strings.Contains(err.Error(), "long connection") {
		t.Fatalf("error = %v, want the missing long connection explained", err)
	}
}
