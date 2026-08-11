package limiter

import (
	"context"
	"errors"
	"log"
	"time"

	"rate-limiter/internal/metrics"

	"github.com/sony/gobreaker"
)

// CircuitBreakerLimiter wraps any Limiter with a resilience circuit breaker
// layer.
//
// Known trade-off: breaker state lives in-process and is local to a single
// replica. Each API instance trips and recovers independently based on its
// own observed Redis failures - there is no shared "the datastore is down"
// signal across replicas. This is a deliberate choice, not an oversight:
// coordinating breaker state across instances (e.g. via a shared Redis key
// or pub/sub channel) would mean relying on the very datastore the breaker
// exists to protect against, adding latency and a new failure mode to solve
// a problem that mostly self-corrects. In practice, a real Redis outage
// affects every replica's connections at roughly the same time, so they
// converge to OPEN within a request cycle or two of each other. The
// "instance" label on rate_limiter_circuit_breaker_state makes this
// per-replica behavior directly observable rather than hidden.
type CircuitBreakerLimiter struct {
	wrapped    Limiter
	cb         *gobreaker.CircuitBreaker
	name       string
	instanceID string
}

// NewCircuitBreakerLimiter wraps a Limiter with a circuit breaker. instanceID
// identifies the replica this breaker belongs to (see cfg.InstanceID) and is
// attached to every log line and metric this breaker emits.
func NewCircuitBreakerLimiter(wrapped Limiter, name string, instanceID string) *CircuitBreakerLimiter {
	st := gobreaker.Settings{
		Name:        name,
		MaxRequests: 3,                // Requests allowed in Half-Open state to test recovery
		Interval:    10 * time.Second, // Clear counter interval in Closed state
		Timeout:     5 * time.Second,  // Time spent in Open state before transitioning to Half-Open
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			// Trip to OPEN after 5+ requests with a >=60% failure ratio
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.Requests >= 5 && failureRatio >= 0.6
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			log.Printf("[Circuit Breaker: %s @ %s] State changed from %s to %s", name, instanceID, from, to)

			var stateVal float64
			switch to {
			case gobreaker.StateClosed:
				stateVal = 0
			case gobreaker.StateHalfOpen:
				stateVal = 1
			case gobreaker.StateOpen:
				stateVal = 2
			}
			metrics.CircuitBreakerState.WithLabelValues(name, instanceID).Set(stateVal)
		},
	}

	// Initialize Prometheus gauge to 0 (Closed) on startup
	metrics.CircuitBreakerState.WithLabelValues(name, instanceID).Set(0)

	return &CircuitBreakerLimiter{
		wrapped:    wrapped,
		cb:         gobreaker.NewCircuitBreaker(st),
		name:       name,
		instanceID: instanceID,
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
			log.Printf("[Circuit Breaker OPEN @ %s] Bypassing Redis for client %s (Failing Open instantly)", c.instanceID, identifier)
		} else {
			log.Printf("[Rate Limiter Error @ %s] %v for client %s (Failing Open)", c.instanceID, err, identifier)
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
