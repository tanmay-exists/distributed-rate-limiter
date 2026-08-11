package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// All series carry an "instance" label. This is what makes per-replica
// behavior (especially independent circuit breaker trips, see
// internal/limiter/circuit_breaker.go) observable instead of implicit.
var (
	RequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rate_limiter_requests_total",
			Help: "Total number of requests processed by the rate limiter.",
		},
		[]string{"strategy", "status", "instance"}, // status: "allowed" or "blocked"
	)

	// Gauge for circuit breaker status (0=Closed, 1=Half-Open, 2=Open),
	// scoped per instance since breaker state is not shared across replicas.
	CircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rate_limiter_circuit_breaker_state",
			Help: "Current state of the circuit breaker (0=Closed, 1=HalfOpen, 2=Open), per instance.",
		},
		[]string{"name", "instance"},
	)

	RedisLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "rate_limiter_redis_latency_seconds",
			Help:    "Latency of Redis rate-limiting operations in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"strategy", "instance"},
	)
)
