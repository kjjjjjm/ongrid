package hostmetrics

import (
	"slices"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

func TestBuildArgsListenAddress(t *testing.T) {
	t.Setenv("ONGRID_EDGE_HOSTMETRICS_ADDR", "")
	if args := buildArgs(plugins.PluginConfig{}, ""); !slices.Contains(args, "--web.listen-address=:9102") {
		t.Fatalf("default changed: %#v", args)
	}
	t.Setenv("ONGRID_EDGE_HOSTMETRICS_ADDR", "127.0.0.1:19102")
	if args := buildArgs(plugins.PluginConfig{}, ""); !slices.Contains(args, "--web.listen-address=127.0.0.1:19102") {
		t.Fatalf("env default not applied: %#v", args)
	}
	spec := plugins.PluginConfig{Spec: map[string]interface{}{"listen_address": ":29102"}}
	if args := buildArgs(spec, ""); !slices.Contains(args, "--web.listen-address=:29102") {
		t.Fatalf("spec must win over env: %#v", args)
	}
}
