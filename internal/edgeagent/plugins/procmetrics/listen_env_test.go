package procmetrics

import (
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

func TestBuildArgsUsesListenEnvAndSpecWins(t *testing.T) {
	t.Setenv("ONGRID_EDGE_PROCMETRICS_ADDR", "127.0.0.1:19256")
	if args := buildArgs(plugins.PluginConfig{}, "/tmp/p.yaml"); !contains(args, "-web.listen-address=127.0.0.1:19256") {
		t.Fatalf("env default not applied: %#v", args)
	}
	spec := plugins.PluginConfig{Spec: map[string]interface{}{"listen_address": ":29256"}}
	if args := buildArgs(spec, "/tmp/p.yaml"); !contains(args, "-web.listen-address=:29256") {
		t.Fatalf("spec must win over env: %#v", args)
	}
}
