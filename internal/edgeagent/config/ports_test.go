package config

import (
	"strings"
	"testing"
)

func clearListenerEnv(t *testing.T) {
	t.Helper()
	for _, l := range HostListeners() {
		t.Setenv(l.Env, "")
	}
}

func TestHostListenersDefaultsAreValidAndUnique(t *testing.T) {
	clearListenerEnv(t)
	if err := ValidateHostListeners(); err != nil {
		t.Fatalf("built-in defaults must not conflict: %v", err)
	}
	seen := map[string]bool{}
	for _, l := range HostListeners() {
		if l.Addr() != l.Default {
			t.Fatalf("%s: Addr()=%q, want default %q", l.Env, l.Addr(), l.Default)
		}
		if seen[l.Env] {
			t.Fatalf("duplicate env %s", l.Env)
		}
		seen[l.Env] = true
	}
}

func TestListenerOverride(t *testing.T) {
	clearListenerEnv(t)
	t.Setenv(ListenerHostMetrics.Env, " 127.0.0.1:19102 ")
	if got := ListenerHostMetrics.Addr(); got != "127.0.0.1:19102" {
		t.Fatalf("Addr()=%q", got)
	}
	if ListenerHostMetrics.Port() != "19102" || ListenerHostMetrics.Host() != "127.0.0.1" || ListenerHostMetrics.PortInt() != 19102 {
		t.Fatalf("port/host = %q/%q", ListenerHostMetrics.Port(), ListenerHostMetrics.Host())
	}
	if err := ValidateHostListeners(); err != nil {
		t.Fatalf("valid override rejected: %v", err)
	}
}

func TestListenerInvalidOverrideFallsBackAndFailsValidation(t *testing.T) {
	for _, bad := range []string{"9102", "127.0.0.1:0", ":70000", "host:port", "a b:1"} {
		clearListenerEnv(t)
		t.Setenv(ListenerProcMetrics.Env, bad)
		if got := ListenerProcMetrics.Addr(); got != ListenerProcMetrics.Default {
			t.Fatalf("%q: Addr()=%q, want default", bad, got)
		}
		err := ValidateHostListeners()
		if err == nil || !strings.Contains(err.Error(), ListenerProcMetrics.Env) {
			t.Fatalf("%q: want validation error naming %s, got %v", bad, ListenerProcMetrics.Env, err)
		}
	}
}

func TestValidateHostListenersDetectsConflicts(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		conflict bool
	}{
		{"wildcard vs loopback same port", map[string]string{ListenerHostMetrics.Env: ":4318"}, true},
		{"same loopback port", map[string]string{ListenerLogsHealth.Env: "127.0.0.1:13133"}, true},
		{"localhost vs 127.0.0.1", map[string]string{ListenerLogsHealth.Env: "localhost:13133"}, true},
		{"different specific hosts", map[string]string{ListenerTracesHealth.Env: "10.0.0.5:13134"}, false},
		{"moved away", map[string]string{ListenerHostMetrics.Env: ":19102", ListenerProcMetrics.Env: ":19256"}, false},
		{"diagnostics default collides", map[string]string{ListenerHostMetrics.Env: ":9101"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearListenerEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			err := ValidateHostListeners()
			if tc.conflict && (err == nil || !strings.Contains(err.Error(), "is used by both")) {
				t.Fatalf("want conflict error, got %v", err)
			}
			if !tc.conflict && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestPprofCanBeDisabled(t *testing.T) {
	clearListenerEnv(t)
	t.Setenv(ListenerPprof.Env, "off")
	if !ListenerPprof.Disabled() {
		t.Fatal("pprof should be disabled")
	}
	t.Setenv(ListenerHostMetrics.Env, ":16060")
	if err := ValidateHostListeners(); err != nil {
		t.Fatalf("disabled pprof must not reserve its port: %v", err)
	}
	t.Setenv(ListenerHostMetrics.Env, "off")
	if ListenerHostMetrics.Disabled() {
		t.Fatal("hostmetrics is not disableable through its listen address")
	}
	if err := ValidateHostListeners(); err == nil {
		t.Fatal("off is not a valid hostmetrics address")
	}
}

func TestLocalURL(t *testing.T) {
	for in, want := range map[string]string{
		":9102":          "http://127.0.0.1:9102/metrics",
		"0.0.0.0:9102":   "http://127.0.0.1:9102/metrics",
		"[::]:9102":      "http://127.0.0.1:9102/metrics",
		"127.0.0.1:9256": "http://127.0.0.1:9256/metrics",
		"10.1.2.3:19102": "http://10.1.2.3:19102/metrics",
		"[::1]:9102":     "http://[::1]:9102/metrics",
	} {
		if got := LocalURL(in, "/metrics"); got != want {
			t.Errorf("LocalURL(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestReservedPortsFollowOverrides(t *testing.T) {
	clearListenerEnv(t)
	got := ReservedPorts()
	if got["9102"] != "hostmetrics" || got["9256"] != "procmetrics" {
		t.Fatalf("default reserved ports = %v", got)
	}
	t.Setenv(ListenerHostMetrics.Env, ":19102")
	got = ReservedPorts()
	if got["19102"] != "hostmetrics" {
		t.Fatalf("moved hostmetrics not reserved: %v", got)
	}
	if _, ok := got["9102"]; ok {
		t.Fatalf("old hostmetrics port still reserved: %v", got)
	}
}
