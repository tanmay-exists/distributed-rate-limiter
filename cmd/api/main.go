package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
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
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Unable to connect to Redis: %v", err)
	}

	// Strategy 1: Sliding Window Counter (Strict, low-memory strategy for sensitive endpoints like auth)
	swcLimiter, err := limiter.NewSlidingWindowCounter(ctx, rdb, 5, 60) // 5 req per 60s
	if err != nil {
		log.Fatalf("Failed to initialize SlidingWindowCounter: %v", err)
	}

	// Strategy 2: Token Bucket (Burstable strategy for public API endpoints)
	tbLimiter, err := limiter.NewTokenBucket(ctx, rdb, 15, 2) // Bucket capacity 15, 2 tokens/sec
	if err != nil {
		log.Fatalf("Failed to initialize TokenBucket: %v", err)
	}

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

	log.Printf("Server running on port %s", cfg.Port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}
