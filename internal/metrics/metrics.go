package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Request counter tracking allowed vs blocked requests by strategy
	RequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rate_limiter_requests_total",
			Help: "Total number of requests processed by the rate limiter.",
		},
		[]string{"strategy", "status"}, // status: "allowed" or "blocked"
	)

	// Gauge for circuit breaker status (0=Closed, 1=Half-Open, 2=Open)
	CircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rate_limiter_circuit_breaker_state",
			Help: "Current state of the circuit breaker (0=Closed, 1=HalfOpen, 2=Open).",
		},
		[]string{"name"},
	)

	// Histogram measuring Redis operation execution duration
	RedisLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "rate_limiter_redis_latency_seconds",
			Help:    "Latency of Redis rate-limiting operations in seconds.",
			Buckets: prometheus.DefBuckets, // Standard response time buckets
		},
		[]string{"strategy"},
	)
)
