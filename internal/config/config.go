package config

import (
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Port               string
	RedisAddr          string
	RedisPassword      string
	RateLimitRequests  int64
	RateLimitWindowSec int64
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	requests, err := strconv.ParseInt(getEnv("RATE_LIMIT_REQUESTS", "10"), 10, 64)
	if err != nil {
		requests = 10
	}

	window, err := strconv.ParseInt(getEnv("RATE_LIMIT_WINDOW_SECONDS", "60"), 10, 64)
	if err != nil {
		window = 60
	}

	return &Config{
		Port:               getEnv("PORT", "8080"),
		RedisAddr:          getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:      getEnv("REDIS_PASSWORD", ""),
		RateLimitRequests:  requests,
		RateLimitWindowSec: window,
	}, nil
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}
