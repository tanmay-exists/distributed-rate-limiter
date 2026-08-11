package limiter

import (
	"context"
	"errors"
	"testing"
)

// flakyLimiter is a test double that fails its first N calls, then succeeds.
type flakyLimiter struct {
	failures int
	calls    int
}

func (f *flakyLimiter) Name() string { return "flaky" }

func (f *flakyLimiter) Allow(ctx context.Context, identifier string) (*Result, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, errors.New("simulated datastore failure")
	}
	return &Result{Allowed: true, Remaining: 1, ResetSec: 0}, nil
}

func TestCircuitBreaker_AlwaysFailsOpenOnWrappedError(t *testing.T) {
	wrapped := &flakyLimiter{failures: 1}
	cb := NewCircuitBreakerLimiter(wrapped, "test_breaker_fail_open", "test-instance")

	res, err := cb.Allow(context.Background(), "client-1")
	if err != nil {
		t.Fatalf("Allow should never surface an error to the caller (fail-open): %v", err)
	}
	if !res.Allowed {
		t.Fatalf("expected fail-open behavior (Allowed=true) when the wrapped limiter errors")
	}
}

// TestCircuitBreaker_StopsCallingWrappedOnceOpen verifies that once enough
// failures trip the breaker to OPEN, further calls are absorbed by the
// breaker itself and never reach the wrapped limiter - this is what protects
// a struggling/unreachable Redis from being hammered by retries.
func TestCircuitBreaker_StopsCallingWrappedOnceOpen(t *testing.T) {
	wrapped := &flakyLimiter{failures: 100} // never recovers within this test
	cb := NewCircuitBreakerLimiter(wrapped, "test_breaker_trip", "test-instance")

	ctx := context.Background()

	// 5 consecutive failures with a 100% failure ratio trips the breaker
	// (ReadyToTrip requires >=5 requests and >=60% failure ratio).
	for i := 0; i < 5; i++ {
		if _, err := cb.Allow(ctx, "client-1"); err != nil {
			t.Fatalf("unexpected error on call %d: %v", i, err)
		}
	}

	callsAtTrip := wrapped.calls

	// Further calls should be short-circuited by the OPEN breaker without
	// reaching the wrapped limiter at all.
	for i := 0; i < 5; i++ {
		if _, err := cb.Allow(ctx, "client-1"); err != nil {
			t.Fatalf("unexpected error after trip: %v", err)
		}
	}

	if wrapped.calls != callsAtTrip {
		t.Fatalf("expected wrapped limiter to not be invoked while breaker is OPEN; calls went from %d to %d", callsAtTrip, wrapped.calls)
	}
}

func TestCircuitBreaker_PassesThroughSuccessfulResult(t *testing.T) {
	wrapped := &flakyLimiter{failures: 0}
	cb := NewCircuitBreakerLimiter(wrapped, "test_breaker_healthy", "test-instance")

	res, err := cb.Allow(context.Background(), "client-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("expected healthy wrapped limiter result to be allowed")
	}
}
