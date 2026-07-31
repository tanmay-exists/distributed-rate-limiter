package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rate-limiter/internal/config"
	"rate-limiter/internal/limiter"
	"rate-limiter/internal/middleware"

	"github.com/redis/go-redis/v9"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       0,
		DialTimeout:  2 * time.Second, // Timeout for establishing initial TCP connection
    ReadTimeout:  1 * time.Second, // Timeout for reading responses from Redis
    WriteTimeout: 1 * time.Second, // Timeout for sending commands to Redis
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
    log.Printf("Warning: Initial Redis connection failed (%v). Circuit breakers will start ready.", err)
	}

	// Strategy 1: Sliding Window Counter (Strict, low-memory strategy for sensitive endpoints like auth)
	rawSWCLimiter, err := limiter.NewSlidingWindowCounter(ctx, rdb, 5, 60)
	if err != nil {
		log.Fatalf("Failed to initialize SlidingWindowCounter: %v", err)
	}

	rawTBLimiter, err := limiter.NewTokenBucket(ctx, rdb, 15, 2)
	if err != nil {
		log.Fatalf("Failed to initialize TokenBucket: %v", err)
	}

	swcLimiter := limiter.NewCircuitBreakerLimiter(rawSWCLimiter, "sliding_window_auth")
	tbLimiter := limiter.NewCircuitBreakerLimiter(rawTBLimiter, "token_bucket_api")

	mux := http.NewServeMux()

	// Protected Handlers
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

	// Register routes with different rate limiter strategies
	mux.Handle("/api/v1/auth/login", middleware.RateLimit(swcLimiter)(authHandler))
	mux.Handle("/api/v1/resource", middleware.RateLimit(tbLimiter)(resourceHandler))

	server := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("Server running on port %s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Graceful Shutdown Handler
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced shutdown: %v", err)
	}

	if err := rdb.Close(); err != nil {
		log.Printf("Error closing Redis client: %v", err)
	}

	log.Println("Server stopped cleanly")
}
