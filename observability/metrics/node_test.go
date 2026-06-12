package metrics

import (
	"testing"
	"time"
)

// TestNodeMetricsRecordRouteAndStatus verifies ObserveNodeRoute increments
// counters and observes the duration histogram correctly.
func TestNodeMetricsRecordRouteAndStatus(t *testing.T) {
	resetForTest()
	reg := Init(Config{Namespace: "tsu", Service: "node-test"})

	reg.ObserveNodeRoute(1001, "ok", 200*time.Microsecond)
	reg.ObserveNodeRoute(1001, "error", 300*time.Microsecond)

	if got := counterValue(t, reg, "tsu_node_route_calls_total", map[string]string{
		"service": "node-test",
		"route":   "1001",
		"status":  "ok",
	}); got != 1 {
		t.Fatalf("ok counter = %v, want 1", got)
	}
	if got := counterValue(t, reg, "tsu_node_route_calls_total", map[string]string{
		"service": "node-test",
		"route":   "1001",
		"status":  "error",
	}); got != 1 {
		t.Fatalf("error counter = %v, want 1", got)
	}

	hist := metricForLabels(t, reg, "tsu_node_route_duration_seconds", map[string]string{
		"service": "node-test",
		"route":   "1001",
	})
	if hist.GetHistogram().GetSampleCount() != 2 {
		t.Fatalf("node histogram sample count = %d, want 2", hist.GetHistogram().GetSampleCount())
	}
	if firstBucket := hist.GetHistogram().Bucket[0].GetUpperBound(); firstBucket != 0.0001 {
		t.Fatalf("first bucket = %v, want 0.0001", firstBucket)
	}
}

// TestNodeMiddlewareHelperRecordsPanicAsError verifies ObserveNodeMiddleware
// records panic paths as status="error" and re-panics.
func TestNodeMiddlewareHelperRecordsPanicAsError(t *testing.T) {
	resetForTest()
	reg := Init(Config{Namespace: "tsu", Service: "node-panic-test"})

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("expected panic to propagate")
			}
		}()
		reg.ObserveNodeMiddleware(1002, func() { panic("boom") })
	}()

	if got := counterValue(t, reg, "tsu_node_route_calls_total", map[string]string{
		"service": "node-panic-test",
		"route":   "1002",
		"status":  "error",
	}); got != 1 {
		t.Fatalf("panic counter = %v, want 1", got)
	}
}
