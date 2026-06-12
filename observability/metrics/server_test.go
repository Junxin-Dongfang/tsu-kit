package metrics

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestServerMetricsEndpointReturnsPromText(t *testing.T) {
	resetForTest()
	reg := Init(Config{Namespace: "tsu", Service: "server-test"})
	reg.nodeCalls.WithLabelValues("server-test", "1001", "ok").Inc()

	server := NewServer("127.0.0.1:0", reg)
	server.Start()
	t.Cleanup(server.Close)

	res, err := http.Get("http://" + server.Addr() + "/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.StatusCode, text)
	}
	for _, needle := range []string{
		"# HELP tsu_go_goroutines",
		"tsu_node_route_calls_total",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("metrics response missing %q:\n%s", needle, text)
		}
	}
}

func TestServerLifecycleDefaults(t *testing.T) {
	resetForTest()
	server := NewServer("", nil)
	if server.Name() != "observability-metrics" {
		t.Fatalf("unexpected name: %s", server.Name())
	}
	server.Init()
	if server.Addr() != ":9091" {
		t.Fatalf("default addr = %s, want :9091", server.Addr())
	}
	server.Close()
	server.Destroy()
}
