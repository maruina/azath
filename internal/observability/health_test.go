package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maruina/azath/internal/observability"
)

// parseBody unmarshals the recorder body into T, failing the test on error.
func parseBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v\nraw: %s", err, rec.Body.String())
	}
	return body
}

func TestLivezHandler_Always200(t *testing.T) {
	t.Parallel()
	hc := observability.NewHealthChecker(nil)
	// Register a failing check — livez should still return 200.
	hc.Register("always_fail", func(_ context.Context) error {
		return errors.New("broken")
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	hc.LivezHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	body := parseBody[map[string]any](t, rec)
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", body["status"])
	}
}

func TestReadyzHandler_AllChecksPass(t *testing.T) {
	t.Parallel()
	hc := observability.NewHealthChecker(nil)
	hc.Register("check_a", func(_ context.Context) error { return nil })
	hc.Register("check_b", func(_ context.Context) error { return nil })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	hc.ReadyzHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	body := parseBody[map[string]string](t, rec)
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", body["status"])
	}
	// Check names must not appear in the external response (topology leak prevention).
	if _, ok := body["checks"]; ok {
		t.Error("response must not contain check names in external response")
	}
}

func TestReadyzHandler_OneCheckFails(t *testing.T) {
	t.Parallel()
	hc := observability.NewHealthChecker(nil)
	hc.Register("passing", func(_ context.Context) error { return nil })
	hc.Register("failing", func(_ context.Context) error { return errors.New("db unreachable") })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	hc.ReadyzHandler()(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
	body := parseBody[map[string]string](t, rec)
	if body["status"] != "fail" {
		t.Errorf("expected status=fail, got %q", body["status"])
	}
	// Check names and error detail must not appear in the external response.
	if _, ok := body["checks"]; ok {
		t.Error("response must not contain check names in external response")
	}
}

func TestReadyzHandler_NoChecks(t *testing.T) {
	t.Parallel()
	hc := observability.NewHealthChecker(nil)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	hc.ReadyzHandler()(rec, req)

	// No checks registered means the server hasn't finished initialising.
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
	body := parseBody[map[string]any](t, rec)
	if body["status"] != "fail" {
		t.Errorf("expected status=fail, got %q", body["status"])
	}
}

func TestRegister_DuplicatePanics(t *testing.T) {
	t.Parallel()
	hc := observability.NewHealthChecker(nil)
	hc.Register("dup", func(_ context.Context) error { return nil })

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate register, got none")
		}
	}()
	hc.Register("dup", func(_ context.Context) error { return nil })
}

func TestReadyzHandler_PanickingCheck(t *testing.T) {
	t.Parallel()
	hc := observability.NewHealthChecker(nil)
	hc.Register("panicky", func(_ context.Context) error {
		panic("unexpected panic")
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	hc.ReadyzHandler()(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for panicking check, got %d", rec.Code)
	}
}

func TestNewHTTPServer_Routes(t *testing.T) {
	t.Parallel()
	hc := observability.NewHealthChecker(nil)
	hc.Register("test", func(_ context.Context) error { return nil })
	m := observability.NewMetrics()
	srv := observability.NewHTTPServer(":0", hc, m.Registry)

	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("route %q: expected 200, got %d", path, rec.Code)
		}
	}
}

func TestReadyzHandler_FailureIsLogged(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, err := observability.NewLogger("debug", "json", observability.WithWriter(&buf))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	hc := observability.NewHealthChecker(logger)
	hc.Register("db", func(_ context.Context) error {
		return errors.New("connection refused")
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	hc.ReadyzHandler()(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
	// External response must be opaque — only top-level status, no check names.
	body := parseBody[map[string]any](t, rec)
	if body["status"] != "fail" {
		t.Errorf("expected status=fail, got %q", body["status"])
	}
	if _, ok := body["checks"]; ok {
		t.Error("response must not contain check names in external response")
	}
	// Full error must appear in logs.
	if !strings.Contains(buf.String(), "connection refused") {
		t.Errorf("expected error detail in log output, got: %s", buf.String())
	}
}
