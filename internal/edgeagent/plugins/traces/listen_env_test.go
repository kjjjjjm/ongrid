package traces

import (
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

func TestRenderUsesListenEnvDefaults(t *testing.T) {
	t.Setenv("ONGRID_EDGE_TRACES_GRPC_ADDR", "127.0.0.1:24317")
	t.Setenv("ONGRID_EDGE_TRACES_HTTP_ADDR", "127.0.0.1:24318")
	t.Setenv("ONGRID_EDGE_TRACES_HEALTH_ADDR", "127.0.0.1:23133")
	t.Setenv("ONGRID_EDGE_TRACES_TELEMETRY_ADDR", "127.0.0.1:28888")
	out, err := render(plugins.PluginConfig{
		Enabled: true, EdgeID: 1, Endpoint: "https://tempo.example/v1/traces",
		Spec: map[string]interface{}{dockerBridgeAddressesKey: []string{"172.17.0.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	for _, want := range []string{"127.0.0.1:24317", "127.0.0.1:24318", "172.17.0.1:24317", "172.17.0.1:24318", "127.0.0.1:23133", "28888"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered config missing %q", want)
		}
	}
	for _, old := range []string{":4317", ":4318", "13133", ":8888"} {
		if strings.Contains(body, old) {
			t.Errorf("rendered config still contains default %q", old)
		}
	}
}
