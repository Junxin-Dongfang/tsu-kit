package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestInitIsIdempotentAndRegistersCollectors(t *testing.T) {
	resetForTest()
	reg1 := Init(Config{Namespace: "tsu", Service: "game-api"})
	reg2 := Init(Config{Namespace: "tsu", Service: "game-api"})
	if reg1 != reg2 {
		t.Fatal("Init must return the same registry for the same config")
	}
	if reg1.Registry != reg2.Registry {
		t.Fatal("Init must share the same prometheus.Registry")
	}
	reg1.httpRequests.WithLabelValues("game-api", "/healthz", "GET", "2xx").Add(0)
	reg1.nodeCalls.WithLabelValues("game-api", "1001", "ok").Add(0)
	reg1.gateConnections.WithLabelValues("game-api").Add(0)

	families, err := reg1.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if !hasMetricFamily(families, "tsu_go_goroutines") {
		t.Fatalf("missing prefixed Go collector metric")
	}
	if !metricExists(t, reg1, "tsu_go_goroutines", map[string]string{"service": "game-api"}) {
		t.Fatalf("Go collector metric must carry service label")
	}
	if !hasMetricFamily(families, "tsu_http_requests_total") {
		t.Fatalf("missing TSU HTTP counter")
	}
	if !hasMetricFamily(families, "tsu_node_route_calls_total") {
		t.Fatalf("missing TSU node counter")
	}
	if !hasMetricFamily(families, "tsu_gate_connections") {
		t.Fatalf("missing TSU gate gauge")
	}
}

func TestCustomMetricTypesIncrement(t *testing.T) {
	resetForTest()
	reg := Init(Config{Namespace: "tsu", Service: "metric-types"})

	reg.httpRequests.WithLabelValues("metric-types", "/healthz", "GET", "2xx").Inc()
	reg.httpInflight.WithLabelValues("metric-types").Inc()
	reg.httpDuration.WithLabelValues("metric-types", "/healthz", "GET").Observe(0.02)

	if got := testutil.ToFloat64(reg.httpRequests.WithLabelValues("metric-types", "/healthz", "GET", "2xx")); got != 1 {
		t.Fatalf("http counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(reg.httpInflight.WithLabelValues("metric-types")); got != 1 {
		t.Fatalf("http inflight = %v, want 1", got)
	}

	hist := metricForLabels(t, reg, "tsu_http_request_duration_seconds", map[string]string{
		"service": "metric-types",
		"route":   "/healthz",
		"method":  "GET",
	})
	if hist.GetHistogram().GetSampleCount() != 1 {
		t.Fatalf("histogram sample count = %d, want 1", hist.GetHistogram().GetSampleCount())
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := normalizeConfig(Config{})
	if cfg.Namespace != "tsu" || cfg.Service != "unknown" {
		t.Fatalf("unexpected normalized config: %+v", cfg)
	}
}

func hasMetricFamily(families []*dto.MetricFamily, name string) bool {
	for _, family := range families {
		if family.GetName() == name {
			return true
		}
	}
	return false
}
