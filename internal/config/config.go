package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port       string
	InstanceID string

	// RedisMode is "standalone" (single Redis instance, used in local/dev
	// setups and CI) or "sentinel" (Sentinel-managed master/replica pair,
	// used in the multi-instance docker-compose stack). Sentinel mode is
	// what removes Redis as a single point of failure: go-redis asks the
	// Sentinel quorum for the current master before connecting and
	// re-resolves it automatically on failover.
	RedisMode          string
	RedisAddr          string // used when RedisMode == "standalone"
	RedisSentinelAddrs []string
	RedisMasterName    string
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

	instanceID := getEnv("INSTANCE_ID", "")
	if instanceID == "" {
		if host, err := os.Hostname(); err == nil && host != "" {
			instanceID = host
		} else {
			instanceID = "unknown"
		}
	}

	var sentinelAddrs []string
	if raw := getEnv("REDIS_SENTINEL_ADDRS", ""); raw != "" {
		for _, a := range strings.Split(raw, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				sentinelAddrs = append(sentinelAddrs, a)
			}
		}
	}

	return &Config{
		Port:               getEnv("PORT", "8080"),
		InstanceID:         instanceID,
		RedisMode:          getEnv("REDIS_MODE", "standalone"),
		RedisAddr:          getEnv("REDIS_ADDR", "localhost:6379"),
		RedisSentinelAddrs: sentinelAddrs,
		RedisMasterName:    getEnv("REDIS_MASTER_NAME", "mymaster"),
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
