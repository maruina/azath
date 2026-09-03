package observability_test

import (
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/maruina/azath/internal/observability"
)

// metricFamilies gathers all metric families from m.Registry.
func metricFamilies(t *testing.T, m *observability.Metrics) map[string]*dto.MetricFamily {
	t.Helper()
	mfs, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("registry.Gather: %v", err)
	}
	out := make(map[string]*dto.MetricFamily, len(mfs))
	for _, mf := range mfs {
		out[mf.GetName()] = mf
	}
	return out
}

// labelExists reports whether any metric in mf has a label with the given name and value.
func labelExists(mf *dto.MetricFamily, name, value string) bool {
	for _, metric := range mf.GetMetric() {
		for _, label := range metric.GetLabel() {
			if label.GetName() == name && label.GetValue() == value {
				return true
			}
		}
	}
	return false
}

func TestNewMetrics_Registration(t *testing.T) {
	t.Parallel()
	m := observability.NewMetrics()
	// Seed CounterVec/HistogramVec with a placeholder so Gather returns them.
	// Scalar metrics (Counter, Gauge, Histogram) always appear with zero values.
	m.UnsealTotal.WithLabelValues("__test__")
	m.GateApprovals.WithLabelValues("__test__", "__test__")
	m.NotificationFailures.WithLabelValues("__test__")
	m.GateAPIErrors.WithLabelValues("__test__")
	families := metricFamilies(t, m)

	expected := []string{
		"azath_seal_total",
		"azath_unseal_total",
		"azath_seal_duration_seconds",
		"azath_unseal_duration_seconds",
		"azath_master_key_loaded",
		"azath_gate_approvals_total",
		"azath_registry_size",
		"azath_notification_failures_total",
		"azath_registry_load_errors_total",
		"azath_gate_api_errors_total",
	}
	for _, name := range expected {
		if _, ok := families[name]; !ok {
			t.Errorf("expected metric %q not registered", name)
		}
	}
}

func TestUnsealTotal_ReasonLabel(t *testing.T) {
	t.Parallel()
	m := observability.NewMetrics()
	m.UnsealTotal.WithLabelValues("disabled").Inc()

	families := metricFamilies(t, m)
	mf, ok := families["azath_unseal_total"]
	if !ok {
		t.Fatal("azath_unseal_total not found")
	}
	if !labelExists(mf, "reason", "disabled") {
		t.Error(`did not find azath_unseal_total{reason="disabled"}`)
	}
}

func TestGateApprovals_Labels(t *testing.T) {
	t.Parallel()
	m := observability.NewMetrics()
	m.GateApprovals.WithLabelValues("telegram", "approved").Inc()

	families := metricFamilies(t, m)
	mf, ok := families["azath_gate_approvals_total"]
	if !ok {
		t.Fatal("azath_gate_approvals_total not found")
	}
	found := false
	for _, metric := range mf.GetMetric() {
		labels := map[string]string{}
		for _, lp := range metric.GetLabel() {
			labels[lp.GetName()] = lp.GetValue()
		}
		if labels["gate"] == "telegram" && labels["result"] == "approved" {
			found = true
			break
		}
	}
	if !found {
		t.Error(`did not find azath_gate_approvals_total{gate="telegram",result="approved"}`)
	}
}

func TestNotificationFailures_Labels(t *testing.T) {
	t.Parallel()
	m := observability.NewMetrics()
	m.NotificationFailures.WithLabelValues("telegram").Inc()

	families := metricFamilies(t, m)
	mf, ok := families["azath_notification_failures_total"]
	if !ok {
		t.Fatal("azath_notification_failures_total not found")
	}
	if !labelExists(mf, "provider", "telegram") {
		t.Error(`did not find azath_notification_failures_total{provider="telegram"}`)
	}
}

func TestGateAPIErrors_Labels(t *testing.T) {
	t.Parallel()
	m := observability.NewMetrics()
	m.GateAPIErrors.WithLabelValues("phone").Inc()

	families := metricFamilies(t, m)
	mf, ok := families["azath_gate_api_errors_total"]
	if !ok {
		t.Fatal("azath_gate_api_errors_total not found")
	}
	if !labelExists(mf, "gate", "phone") {
		t.Error(`did not find azath_gate_api_errors_total{gate="phone"}`)
	}
}

func TestMetrics_IndependentRegistries(t *testing.T) {
	t.Parallel()
	m1 := observability.NewMetrics()
	m2 := observability.NewMetrics()

	m1.SealTotal.Inc()
	m1.SealTotal.Inc()

	// m2's SealTotal should still be 0.
	families := metricFamilies(t, m2)
	mf, ok := families["azath_seal_total"]
	if !ok {
		t.Fatal("azath_seal_total not found in m2")
	}
	for _, metric := range mf.GetMetric() {
		if v := metric.GetCounter().GetValue(); v != 0 {
			t.Errorf("m2.SealTotal should be 0, got %v", v)
		}
	}
}
