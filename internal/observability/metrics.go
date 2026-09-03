package observability

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds all azath Prometheus metrics on a custom registry.
// Use NewMetrics to create an instance. Tests create independent instances
// to avoid global state.
type Metrics struct {
	Registry prometheus.Gatherer

	// SealTotal counts successful Seal RPCs.
	SealTotal prometheus.Counter
	// UnsealTotal counts Unseal RPC outcomes, labelled by reason.
	// reason values: ok, unknown_uuid, disabled, master_key_not_loaded,
	// gate_denied, gate_pending, decrypt_error, wrong_instance.
	UnsealTotal *prometheus.CounterVec
	// SealDuration observes Seal RPC latency in seconds.
	SealDuration prometheus.Histogram
	// UnsealDuration observes Unseal RPC latency in seconds.
	UnsealDuration prometheus.Histogram
	// MasterKeyLoaded is 1 when the master key is in memory, 0 otherwise.
	MasterKeyLoaded prometheus.Gauge
	// GateApprovals counts gate decisions, labelled by gate name and result.
	// result values: approved, denied, pending, error.
	GateApprovals *prometheus.CounterVec
	// RegistrySize tracks the number of devices in the registry.
	RegistrySize prometheus.Gauge
	// NotificationFailures counts notification send failures by provider.
	NotificationFailures *prometheus.CounterVec
	// RegistryLoadErrors counts failures to load or verify the device registry.
	RegistryLoadErrors prometheus.Counter
	// GateAPIErrors counts external API errors per gate.
	GateAPIErrors *prometheus.CounterVec
}

// NewMetrics creates and registers all azath metrics on a fresh registry.
// Panics if any metric fails to register (programming error, not runtime).
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		SealTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "azath_seal_total",
			Help: "Total number of successful Seal RPCs.",
		}),
		UnsealTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "azath_unseal_total",
			Help: "Total number of Unseal RPC outcomes by reason.",
		}, []string{"reason"}),
		SealDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "azath_seal_duration_seconds",
			Help: "Latency of Seal RPCs in seconds.",
			// KMS operations (AES-256-GCM) complete in microseconds to low
			// milliseconds. Buckets cover 100µs–100ms to capture p99/p999.
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1},
		}),
		UnsealDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "azath_unseal_duration_seconds",
			Help: "Latency of Unseal RPCs in seconds.",
			// Unseal includes gate checks (Telegram/phone) which can take seconds.
			// Buckets cover 100µs–30s to capture both fast and gated paths.
			Buckets: []float64{0.0001, 0.001, 0.01, 0.1, 0.5, 1, 5, 10, 30},
		}),
		MasterKeyLoaded: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "azath_master_key_loaded",
			Help: "1 if the master key is currently loaded in memory, 0 otherwise.",
		}),
		GateApprovals: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "azath_gate_approvals_total",
			Help: "Total gate decisions by gate name and result.",
		}, []string{"gate", "result"}),
		RegistrySize: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "azath_registry_size",
			Help: "Number of devices in the registry.",
		}),
		NotificationFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "azath_notification_failures_total",
			Help: "Total notification send failures by provider.",
		}, []string{"provider"}),
		RegistryLoadErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "azath_registry_load_errors_total",
			Help: "Total failures to load or verify the device registry.",
		}),
		GateAPIErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "azath_gate_api_errors_total",
			Help: "Total external API errors per gate.",
		}, []string{"gate"}),
	}

	reg.MustRegister(
		m.SealTotal,
		m.UnsealTotal,
		m.SealDuration,
		m.UnsealDuration,
		m.MasterKeyLoaded,
		m.GateApprovals,
		m.RegistrySize,
		m.NotificationFailures,
		m.RegistryLoadErrors,
		m.GateAPIErrors,
	)

	return m
}
