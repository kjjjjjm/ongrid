package plugins

import "testing"

func TestHostApplicationMetricsDefaultFollowsListenEnv(t *testing.T) {
	t.Setenv("ONGRID_EDGE_TRACES_APP_METRICS_ADDR", "127.0.0.1:29464")
	fetcher := &TunnelConfigFetcher{}
	out := fetcher.applyKubernetesDefaults(map[string]PluginConfig{"traces": {Enabled: true}, "metrics": {Enabled: true}})
	if out["traces"].Spec["metrics_export_endpoint"] != "127.0.0.1:29464" || out["metrics"].Spec["application_metrics_url"] != "http://127.0.0.1:29464/metrics" {
		t.Fatalf("defaults=%+v", out)
	}
}
