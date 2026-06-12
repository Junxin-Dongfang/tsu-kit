package metrics

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func metricForLabels(t *testing.T, reg *Registry, name string, labels map[string]string) *dto.Metric {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			if labelsMatch(metric, labels) {
				return metric
			}
		}
	}
	t.Fatalf("metric %s with labels %+v not found", name, labels)
	return nil
}

func metricExists(t *testing.T, reg *Registry, name string, labels map[string]string) bool {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			if labelsMatch(metric, labels) {
				return true
			}
		}
	}
	return false
}

func metricSeriesCount(t *testing.T, reg *Registry, name string, labels map[string]string) int {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	count := 0
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			if labelsMatch(metric, labels) {
				count++
			}
		}
	}
	return count
}

func counterValue(t *testing.T, reg *Registry, name string, labels map[string]string) float64 {
	t.Helper()
	return metricForLabels(t, reg, name, labels).GetCounter().GetValue()
}

func labelsMatch(metric *dto.Metric, want map[string]string) bool {
	for key, value := range want {
		found := false
		for _, label := range metric.Label {
			if label.GetName() == key && label.GetValue() == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
