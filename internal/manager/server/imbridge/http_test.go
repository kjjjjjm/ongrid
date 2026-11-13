package imbridge

import (
	"encoding/json"
	"strings"
	"testing"

	bizbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	"github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/wecom"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// The stream block is opt-in: an app the supervisor has not observed yet
// must not look like a stopped client, so the field stays out of the JSON.
func TestToAppDTOWithStream(t *testing.T) {
	health := bizbridge.StreamHealth{State: bizbridge.StreamStateRunning}
	for _, tc := range []struct {
		name      string
		known     bool
		wantField bool
	}{
		{"observed_app", true, true},
		{"unobserved_app", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(toAppDTOWithStream(&model.ImApp{ID: 7}, health, tc.known))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got := strings.Contains(string(raw), `"stream"`); got != tc.wantField {
				t.Fatalf("stream field present = %v, want %v (json: %s)", got, tc.wantField, raw)
			}
		})
	}
}

func TestToAppDTOWithStream_WhenObserved_CarriesStateAndError(t *testing.T) {
	got := toAppDTOWithStream(&model.ImApp{ID: 7}, bizbridge.StreamHealth{
		State:     bizbridge.StreamStateRetrying,
		LastError: "dial tcp: connection refused",
	}, true)

	if got.Stream == nil {
		t.Fatal("stream block missing")
	}
	if got.Stream.State != bizbridge.StreamStateRetrying {
		t.Fatalf("state = %q, want %q", got.Stream.State, bizbridge.StreamStateRetrying)
	}
	if got.Stream.LastError != "dial tcp: connection refused" {
		t.Fatalf("last_error = %q", got.Stream.LastError)
	}
}

// Without a hub (or for other providers) the credentials test always runs a
// real probe; only a connected WeCom bot may skip it.
func TestLiveWeComConnection_WithoutHubAlwaysProbes(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil)
	for _, app := range []*model.ImApp{
		{ID: 1, Provider: model.ProviderWeCom, Mode: model.ModeStream, AppID: "aib", AppSecret: "s", Enabled: true},
		{ID: 2, Provider: model.ProviderSlack, Mode: model.ModeStream, AppID: "T1", AppSecret: "{}", Enabled: true},
	} {
		if h.liveWeComConnection(app) {
			t.Fatalf("app %d reported live without a hub", app.ID)
		}
	}
	h.SetWeComHub(wecom.NewHub())
	if h.liveWeComConnection(&model.ImApp{ID: 1, Provider: model.ProviderWeCom, AppID: "aib", AppSecret: "s"}) {
		t.Fatal("an empty hub reported a live connection")
	}
}
