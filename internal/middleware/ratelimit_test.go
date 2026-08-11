package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"rate-limiter/internal/limiter"
)

type stubLimiter struct {
	result *limiter.Result
	err    error
	name   string
}

func (s *stubLimiter) Name() string { return s.name }

func (s *stubLimiter) Allow(ctx context.Context, identifier string) (*limiter.Result, error) {
	return s.result, s.err
}

func TestRateLimit_AllowsRequestWithinLimit(t *testing.T) {
	l := &stubLimiter{name: "stub", result: &limiter.Result{Allowed: true, Remaining: 4, ResetSec: 60}}

	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/resource", nil)
	rec := httptest.NewRecorder()

	RateLimit(l, "test-instance")(next).ServeHTTP(rec, req)

	if !handlerCalled {
		t.Fatalf("expected downstream handler to be called when request is allowed")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "4" {
		t.Fatalf("expected remaining header to be 4, got %s", got)
	}
}

func TestRateLimit_BlocksRequestOverLimit(t *testing.T) {
	l := &stubLimiter{name: "stub", result: &limiter.Result{Allowed: false, Remaining: 0, ResetSec: 30}}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("downstream handler should not be called when request is blocked")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/resource", nil)
	rec := httptest.NewRecorder()

	RateLimit(l, "test-instance")(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("expected Retry-After 30, got %s", got)
	}
}

func TestRateLimit_FailsOpenOnLimiterError(t *testing.T) {
	l := &stubLimiter{name: "stub", err: context.DeadlineExceeded}

	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/resource", nil)
	rec := httptest.NewRecorder()

	RateLimit(l, "test-instance")(next).ServeHTTP(rec, req)

	if !handlerCalled {
		t.Fatalf("expected downstream handler to still be called (fail-open) on limiter error")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on fail-open, got %d", rec.Code)
	}
}

func TestExtractIdentifier_PrefersAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "abc123")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got, want := extractIdentifier(req), "key:abc123"; got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestExtractIdentifier_FallsBackToXForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")

	if got, want := extractIdentifier(req), "ip:1.2.3.4"; got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestExtractIdentifier_FallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "9.8.7.6:54321"

	if got, want := extractIdentifier(req), "ip:9.8.7.6"; got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}
