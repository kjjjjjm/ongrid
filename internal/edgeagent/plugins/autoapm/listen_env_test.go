package autoapm

import (
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	contract "github.com/ongridio/ongrid/internal/pkg/autoapm"
)

func TestCollectorConfigUsesListenEnv(t *testing.T) {
	t.Setenv("ONGRID_EDGE_AUTOAPM_GRPC_ADDR", "127.0.0.1:24317")
	t.Setenv("ONGRID_EDGE_AUTOAPM_HTTP_ADDR", "127.0.0.1:24318")
	t.Setenv("ONGRID_EDGE_AUTOAPM_HEALTH_ADDR", "127.0.0.1:24333")
	t.Setenv("ONGRID_EDGE_AUTOAPM_TELEMETRY_ADDR", "127.0.0.1:28888")
	t.Setenv("ONGRID_EDGE_AUTOAPM_METRICS_ADDR", "127.0.0.1:29465")
	spec := collectorConfig(plugins.PluginConfig{}, contract.Spec{}).Spec
	for key, want := range map[string]string{
		"grpc_endpoint": "127.0.0.1:24317", "http_endpoint": "127.0.0.1:24318",
		"health_endpoint": "127.0.0.1:24333", "collector_metrics_endpoint": "127.0.0.1:28888",
		"metrics_export_endpoint": "127.0.0.1:29465",
	} {
		if spec[key] != want {
			t.Errorf("%s=%v want %s", key, spec[key], want)
		}
	}
}
