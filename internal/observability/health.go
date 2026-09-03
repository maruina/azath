package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// CheckFunc is a health check function. A non-nil error means the check failed.
type CheckFunc func(ctx context.Context) error

// HealthChecker manages named liveness and readiness checks.
type HealthChecker struct {
	mu     sync.RWMutex
	checks map[string]CheckFunc
	log    *slog.Logger
}

// NewHealthChecker creates an empty HealthChecker. Pass nil to discard log output.
func NewHealthChecker(logger *slog.Logger) *HealthChecker {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &HealthChecker{checks: make(map[string]CheckFunc), log: logger}
}

// Register adds a named health check. Panics on duplicate name.
func (hc *HealthChecker) Register(name string, fn CheckFunc) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if _, exists := hc.checks[name]; exists {
		panic(fmt.Sprintf("health check %q already registered", name))
	}
	hc.checks[name] = fn
}

type checkResult struct {
	Status string
}

type healthResponse struct {
	Status string `json:"status"`
}

// LivezHandler returns an HTTP handler for /healthz (liveness).
// Always returns 200 — the process is alive if it can respond at all.
func (hc *HealthChecker) LivezHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok"})
	}
}

// ReadyzHandler returns an HTTP handler for /readyz (readiness).
// Returns 503 when no checks are registered (server not yet initialised)
// or when any registered check fails.
//
// The response contains only a top-level status field. Check names and error
// details are intentionally omitted to avoid leaking internal component
// topology to external callers; full details are logged internally.
func (hc *HealthChecker) ReadyzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hc.mu.RLock()
		// Copy check map under lock; run checks without lock.
		checks := maps.Clone(hc.checks)
		hc.mu.RUnlock()

		// No checks registered means the server hasn't finished initialising.
		if len(checks) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(healthResponse{Status: "fail"})
			return
		}

		overall := "ok"
		var (
			mu sync.Mutex
			wg sync.WaitGroup
		)
		for name, fn := range checks {
			wg.Add(1)
			go func(n string, f CheckFunc) {
				defer wg.Done()
				if cr := runCheck(r.Context(), hc.log, n, f); cr.Status == "fail" {
					mu.Lock()
					overall = "fail"
					mu.Unlock()
				}
			}(name, fn)
		}
		wg.Wait()

		status := http.StatusOK
		if overall == "fail" {
			status = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(healthResponse{Status: overall})
	}
}

// runCheck executes fn and recovers from panics, turning them into failures.
// The full error is logged internally; only an opaque "check failed" is
// returned to callers to avoid leaking internal state externally.
func runCheck(ctx context.Context, logger *slog.Logger, name string, fn CheckFunc) (cr *checkResult) {
	cr = &checkResult{Status: "ok"}
	defer func() {
		// Named return allows the recovered defer to set the return value.
		if r := recover(); r != nil {
			logger.Error("health check panicked", "check", name, "panic", fmt.Sprintf("%v", r))
			cr.Status = "fail"
		}
	}()
	if err := fn(ctx); err != nil {
		logger.Error("health check failed", "check", name, "error", err)
		cr.Status = "fail"
	}
	return cr
}

// NewHTTPServer creates an *http.Server that serves /healthz, /readyz, and /metrics.
// The caller owns the server lifecycle (ListenAndServe, Shutdown).
//
// Security: this server has no authentication. It must be bound to a loopback or
// internal-only address and protected by network-level access controls (firewall
// or similar). The /metrics endpoint exposes operational topology
// (gate names, device count, key load state) that must not be reachable by untrusted
// clients.
func NewHTTPServer(addr string, hc *HealthChecker, reg prometheus.Gatherer) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/healthz", hc.LivezHandler())
	mux.Handle("/readyz", hc.ReadyzHandler())
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
