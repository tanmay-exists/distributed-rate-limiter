package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "rate-limiter/internal/metrics"

	"rate-limiter/internal/config"
	"rate-limiter/internal/limiter"
	"rate-limiter/internal/middleware"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

func main() {
	// 1. Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 2. Initialize Redis Client with Connection Pool timeouts
	redisOpts := &redis.Options{
		Addr:         cfg.RedisAddr,
		Password:     cfg.RedisPassword,
		DB:           0,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
		PoolSize:     10, // Max active connections
	}

	// Enable TLS for cloud Redis providers (e.g., Upstash) if requested via ENV
	if os.Getenv("REDIS_USE_TLS") == "true" {
		redisOpts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	rdb := redis.NewClient(redisOpts)

	// Initial connectivity ping
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer pingCancel()

	if err := rdb.Ping(pingCtx).Err(); err != nil {
		log.Printf("[WARNING] Redis connection failed on startup (%v). Circuit breakers will start ready.", err)
	} else {
		log.Println("[INFO] Successfully connected to Redis.")
	}

	// 3. Initialize Limiter Strategies
	initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer initCancel()

	rawSWCLimiter, err := limiter.NewSlidingWindowCounter(initCtx, rdb, 5, 60) // 5 req / 60s
	if err != nil {
		log.Fatalf("Failed to initialize SlidingWindowCounter: %v", err)
	}

	rawTBLimiter, err := limiter.NewTokenBucket(initCtx, rdb, 15, 2) // Cap 15, 2 tokens/sec
	if err != nil {
		log.Fatalf("Failed to initialize TokenBucket: %v", err)
	}

	// 4. Wrap Strategies in Circuit Breakers
	swcLimiter := limiter.NewCircuitBreakerLimiter(rawSWCLimiter, "sliding_window_auth")
	tbLimiter := limiter.NewCircuitBreakerLimiter(rawTBLimiter, "token_bucket_api")

	// 5. Setup Routes
	mux := http.NewServeMux()

	authHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"Authentication successful"}`))
	})

	resourceHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"Resource retrieved successfully"}`))
	})

	// Root path handler to avoid 404 when visiting base domain
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"online","service":"Distributed Rate Limiter API"}`))
	})

	// Health check endpoint for Load Balancers (AWS ELB / K8s probes)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"OK"}`))
	})

	// Expose Prometheus Metrics Endpoint
	mux.Handle("/metrics", promhttp.Handler())

	mux.Handle("/api/v1/auth/login", middleware.RateLimit(swcLimiter)(authHandler))
	mux.Handle("/api/v1/resource", middleware.RateLimit(tbLimiter)(resourceHandler))

	// 6. Configure HTTP Server
	server := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 7. Start Server in a separate Goroutine
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("[INFO] Server starting on port %s", cfg.Port)
		serverErrors <- server.ListenAndServe()
	}()

	// 8. Trap OS Shutdown Signals (Ctrl+C / Docker Stop)
	shutdownSignal := make(chan os.Signal, 1)
	signal.Notify(shutdownSignal, os.Interrupt, syscall.SIGTERM)

	// Block until a signal or server error occurs
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[FATAL] Unhandled HTTP server error: %v", err)
		}
	case sig := <-shutdownSignal:
		log.Printf("[INFO] Shutdown signal received (%v). Initiating graceful shutdown...", sig)

		// Create a 15-second deadline for active requests to complete
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()

		// Stop accepting new connections and drain active ones
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("[ERROR] Could not stop server gracefully: %v", err)
			if err := server.Close(); err != nil {
				log.Printf("[ERROR] Force close error: %v", err)
			}
		}

		// Close Redis connection pool
		if err := rdb.Close(); err != nil {
			log.Printf("[ERROR] Error closing Redis connections: %v", err)
		} else {
			log.Println("[INFO] Redis connection pool closed cleanly.")
		}

		log.Println("[INFO] Server exited gracefully.")
	}
}
