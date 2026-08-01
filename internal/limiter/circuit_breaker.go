package limiter

import (
	"context"
	"errors"
	"log"
	"time"

	"rate-limiter/internal/metrics"

	"github.com/sony/gobreaker"
)

type CircuitBreakerLimiter struct {
	wrapped Limiter
	cb      *gobreaker.CircuitBreaker
	name    string // <-- Store the name here
}

// NewCircuitBreakerLimiter wraps any Limiter with a resilience circuit breaker layer.
func NewCircuitBreakerLimiter(wrapped Limiter, name string) *CircuitBreakerLimiter {
	st := gobreaker.Settings{
		Name:        name,
		MaxRequests: 3,                // Requests allowed in Half-Open state to test recovery
		Interval:    10 * time.Second, // Clear counter interval in Closed state
		Timeout:     5 * time.Second,  // Time spent in Open state before transitioning to Half-Open
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			// Trip to OPEN after 5 consecutive failures
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.Requests >= 5 && failureRatio >= 0.6
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			log.Printf("[Circuit Breaker: %s] State changed from %s to %s", name, from, to)

			// Map gobreaker.State to metric gauge values (0=Closed, 1=Half-Open, 2=Open)
			var stateVal float64
			switch to {
			case gobreaker.StateClosed:
				stateVal = 0
			case gobreaker.StateHalfOpen:
				stateVal = 1
			case gobreaker.StateOpen:
				stateVal = 2
			}
			metrics.CircuitBreakerState.WithLabelValues(name).Set(stateVal)
		},
	}

	// Initialize Prometheus gauge to 0 (Closed) on startup
	metrics.CircuitBreakerState.WithLabelValues(name).Set(0)

	return &CircuitBreakerLimiter{
		wrapped: wrapped,
		cb:      gobreaker.NewCircuitBreaker(st),
		name:    name, // <-- Assign the name here
	}
}

// Name returns the identifier for this circuit breaker strategy.
func (c *CircuitBreakerLimiter) Name() string {
	return c.name
}

func (c *CircuitBreakerLimiter) Allow(ctx context.Context, identifier string) (*Result, error) {
	// Execute the limiter call inside the Circuit Breaker
	res, err := c.cb.Execute(func() (interface{}, error) {
		return c.wrapped.Allow(ctx, identifier)
	})

	if err != nil {
		// Detect if the error is from the Circuit Breaker being OPEN or timing out
		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			log.Printf("[Circuit Breaker OPEN] Bypassing Redis for client %s (Failing Open instantly)", identifier)
		} else {
			log.Printf("[Rate Limiter Error] %v for client %s (Failing Open)", err, identifier)
		}

		// FAIL-OPEN FALLBACK: Allow traffic through when Redis is unreachable or circuit is OPEN
		return &Result{
			Allowed:   true,
			Remaining: 1,
			ResetSec:  0,
		}, nil
	}

	return res.(*Result), nil
}
