package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Listener describes one local TCP listener owned by the host Edge agent or
// one of its plugin subprocesses. The address comes from Env when set and
// from Default otherwise, so hosts with port conflicts or stricter port
// policies can relocate every Edge listener from /etc/ongrid-edge/ongrid-edge.env.
//
// Explicit plugin specs pushed by the Manager (for example
// hostmetrics.listen_address or traces.http_endpoint) still take precedence;
// these values only replace the built-in defaults.
type Listener struct {
	// Name is a short, stable identifier used in error messages.
	Name string
	// Env is the environment variable that overrides Default.
	Env string
	// Default is the built-in host:port.
	Default string
	// Disableable listeners accept "off" / "disabled" / "none".
	Disableable bool
}

// Host Edge listeners. Kubernetes data-plane pods keep their Chart-managed
// ports and are not covered here.
var (
	ListenerDiagnostics      = Listener{Name: "edge diagnostics /metrics", Env: "ONGRID_EDGE_METRICS_ADDR", Default: ":9101"}
	ListenerPprof            = Listener{Name: "edge pprof", Env: "ONGRID_EDGE_PPROF_ADDR", Default: "127.0.0.1:16060", Disableable: true}
	ListenerHostMetrics      = Listener{Name: "hostmetrics node_exporter", Env: "ONGRID_EDGE_HOSTMETRICS_ADDR", Default: ":9102"}
	ListenerProcMetrics      = Listener{Name: "procmetrics process-exporter", Env: "ONGRID_EDGE_PROCMETRICS_ADDR", Default: ":9256"}
	ListenerTracesGRPC       = Listener{Name: "traces OTLP gRPC", Env: "ONGRID_EDGE_TRACES_GRPC_ADDR", Default: "127.0.0.1:4317"}
	ListenerTracesHTTP       = Listener{Name: "traces OTLP HTTP", Env: "ONGRID_EDGE_TRACES_HTTP_ADDR", Default: "127.0.0.1:4318"}
	ListenerTracesHealth     = Listener{Name: "traces health_check", Env: "ONGRID_EDGE_TRACES_HEALTH_ADDR", Default: "127.0.0.1:13133"}
	ListenerTracesTelemetry  = Listener{Name: "traces collector telemetry", Env: "ONGRID_EDGE_TRACES_TELEMETRY_ADDR", Default: "127.0.0.1:8888"}
	ListenerTracesAppMetrics = Listener{Name: "traces application metrics exporter", Env: "ONGRID_EDGE_TRACES_APP_METRICS_ADDR", Default: "127.0.0.1:9464"}
	ListenerLogsHealth       = Listener{Name: "logs health_check", Env: "ONGRID_EDGE_LOGS_HEALTH_ADDR", Default: "127.0.0.1:13134"}
	ListenerLogsTelemetry    = Listener{Name: "logs collector telemetry", Env: "ONGRID_EDGE_LOGS_TELEMETRY_ADDR", Default: "127.0.0.1:8889"}
	ListenerAutoAPMGRPC      = Listener{Name: "autoapm OTLP gRPC", Env: "ONGRID_EDGE_AUTOAPM_GRPC_ADDR", Default: "127.0.0.1:14317"}
	ListenerAutoAPMHTTP      = Listener{Name: "autoapm OTLP HTTP", Env: "ONGRID_EDGE_AUTOAPM_HTTP_ADDR", Default: "127.0.0.1:14318"}
	ListenerAutoAPMHealth    = Listener{Name: "autoapm health_check", Env: "ONGRID_EDGE_AUTOAPM_HEALTH_ADDR", Default: "127.0.0.1:14333"}
	ListenerAutoAPMTelemetry = Listener{Name: "autoapm collector telemetry", Env: "ONGRID_EDGE_AUTOAPM_TELEMETRY_ADDR", Default: "127.0.0.1:18888"}
	ListenerAutoAPMMetrics   = Listener{Name: "autoapm metrics exporter", Env: "ONGRID_EDGE_AUTOAPM_METRICS_ADDR", Default: "127.0.0.1:9465"}
)

// HostListeners returns every configurable host Edge listener in a stable order.
func HostListeners() []Listener {
	return []Listener{
		ListenerDiagnostics, ListenerPprof,
		ListenerHostMetrics, ListenerProcMetrics,
		ListenerTracesGRPC, ListenerTracesHTTP, ListenerTracesHealth, ListenerTracesTelemetry, ListenerTracesAppMetrics,
		ListenerLogsHealth, ListenerLogsTelemetry,
		ListenerAutoAPMGRPC, ListenerAutoAPMHTTP, ListenerAutoAPMHealth, ListenerAutoAPMTelemetry, ListenerAutoAPMMetrics,
	}
}

// Raw returns the trimmed environment override, or "" when unset.
func (l Listener) Raw() string { return strings.TrimSpace(os.Getenv(l.Env)) }

// Overridden reports whether the operator set the environment variable.
func (l Listener) Overridden() bool { return l.Raw() != "" }

// Disabled reports whether a disableable listener was turned off.
func (l Listener) Disabled() bool {
	if !l.Disableable {
		return false
	}
	switch strings.ToLower(l.Raw()) {
	case "off", "disabled", "disable", "none", "false", "0":
		return true
	}
	return false
}

// Addr returns the effective host:port. Invalid overrides fall back to the
// default; ValidateHostListeners reports them at startup so operators see the
// mistake instead of silently binding the old port.
func (l Listener) Addr() string {
	raw := l.Raw()
	if raw == "" || l.Disabled() {
		return l.Default
	}
	if _, err := ParseListenAddr(raw); err != nil {
		return l.Default
	}
	return raw
}

// Port returns the effective port number as a string.
func (l Listener) Port() string {
	_, port, _ := net.SplitHostPort(l.Addr())
	return port
}

// Host returns the effective bind host ("" means all interfaces).
func (l Listener) Host() string {
	host, _, _ := net.SplitHostPort(l.Addr())
	return host
}

// PortInt returns the effective port number.
func (l Listener) PortInt() int {
	n, _ := strconv.Atoi(l.Port())
	return n
}

// ParseListenAddr validates host:port with a 1..65535 port and returns the port.
func ParseListenAddr(addr string) (int, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return 0, fmt.Errorf("must be host:port or :port: %w", err)
	}
	if strings.ContainsAny(host, " /") {
		return 0, fmt.Errorf("invalid host %q", host)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("port must be 1..65535")
	}
	return n, nil
}

// LocalURL turns a listen address into the URL a local client should dial.
// Wildcard binds (":9102", "0.0.0.0:9102", "[::]:9102") are reached via
// 127.0.0.1; a specific bind address is used as-is.
func LocalURL(listenAddr, path string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return "http://" + listenAddr + path
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + path
}

// ValidateHostListeners checks every override for syntax and every effective
// address for port conflicts. Two listeners conflict when they use the same
// port and their hosts overlap (either one is a wildcard, or both are equal).
func ValidateHostListeners() error {
	var errs []error
	type bound struct {
		l    Listener
		host string
	}
	byPort := map[int][]bound{}
	for _, l := range HostListeners() {
		if raw := l.Raw(); raw != "" && !l.Disabled() {
			if _, err := ParseListenAddr(raw); err != nil {
				errs = append(errs, fmt.Errorf("%s=%q (%s): %w", l.Env, raw, l.Name, err))
				continue
			}
		}
		if l.Disabled() {
			continue
		}
		host, _, _ := net.SplitHostPort(l.Addr())
		byPort[l.PortInt()] = append(byPort[l.PortInt()], bound{l: l, host: host})
	}
	ports := make([]int, 0, len(byPort))
	for p := range byPort {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	for _, p := range ports {
		group := byPort[p]
		for i := 0; i < len(group); i++ {
			for j := i + 1; j < len(group); j++ {
				if hostsOverlap(group[i].host, group[j].host) {
					errs = append(errs, fmt.Errorf("port %d is used by both %s (%s=%s) and %s (%s=%s)",
						p, group[i].l.Name, group[i].l.Env, group[i].l.Addr(), group[j].l.Name, group[j].l.Env, group[j].l.Addr()))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// ReservedPorts maps effective host Edge ports to their owner names. Plugins
// that accept operator-chosen ports (databasemetrics exporters) use it to
// reject collisions with built-in listeners.
func ReservedPorts() map[string]string {
	out := map[string]string{}
	for _, l := range []Listener{ListenerHostMetrics, ListenerProcMetrics} {
		out[l.Port()] = strings.Fields(l.Name)[0]
	}
	for _, l := range HostListeners() {
		if l.Disabled() {
			continue
		}
		if _, ok := out[l.Port()]; !ok {
			out[l.Port()] = l.Name
		}
	}
	return out
}

func hostsOverlap(a, b string) bool {
	wild := func(h string) bool {
		ip := net.ParseIP(h)
		return h == "" || (ip != nil && ip.IsUnspecified())
	}
	if wild(a) || wild(b) {
		return true
	}
	if a == b {
		return true
	}
	la, lb := a == "localhost", b == "localhost"
	ipa, ipb := net.ParseIP(a), net.ParseIP(b)
	if la && ipb != nil && ipb.IsLoopback() || lb && ipa != nil && ipa.IsLoopback() {
		return true
	}
	return ipa != nil && ipb != nil && ipa.Equal(ipb)
}
