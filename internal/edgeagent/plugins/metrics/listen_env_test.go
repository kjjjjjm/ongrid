package metrics

import (
	"slices"
	"testing"
)

func TestDefaultTargetsFollowExporterListenEnv(t *testing.T) {
	t.Setenv("ONGRID_EDGE_HOSTMETRICS_ADDR", ":19102")
	t.Setenv("ONGRID_EDGE_PROCMETRICS_ADDR", "10.0.0.8:19256")
	view, err := parseSpec(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://127.0.0.1:19102/metrics", "http://10.0.0.8:19256/metrics"}
	if !slices.Equal(view.URLs, want) {
		t.Fatalf("URLs=%v want %v", view.URLs, want)
	}
	view, err = parseSpec(map[string]interface{}{"target_urls": []interface{}{"http://127.0.0.1:1/metrics"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(view.URLs, []string{"http://127.0.0.1:1/metrics"}) {
		t.Fatalf("explicit target_urls must win: %v", view.URLs)
	}
}
