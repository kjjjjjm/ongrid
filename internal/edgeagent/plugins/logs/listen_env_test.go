package logs

import (
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

func TestRenderUsesListenEnvDefaults(t *testing.T) {
	t.Setenv("ONGRID_EDGE_LOGS_HEALTH_ADDR", "127.0.0.1:23134")
	t.Setenv("ONGRID_EDGE_LOGS_TELEMETRY_ADDR", "127.0.0.1:28889")
	out, err := render(plugins.PluginConfig{Enabled: true, EdgeID: 1, Endpoint: "https://manager.example.com/loki/api/v1/push"})
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	if !strings.Contains(body, "127.0.0.1:23134") || !strings.Contains(body, "28889") {
		t.Fatalf("overrides not rendered:\n%s", body)
	}
	if strings.Contains(body, "13134") || strings.Contains(body, "port: 8889") {
		t.Fatalf("default ports still rendered:\n%s", body)
	}
}
