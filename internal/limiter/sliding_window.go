package limiter

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis 7+ Function Library definition
const redisFunctionLib = `#!lua name=ratelimit_lib
redis.register_function('rate_limit_check', function(keys, args)
    local key = keys[1]
    local now = tonumber(args[1])
    local window = tonumber(args[2])
    local limit = tonumber(args[3])

    local clearBefore = now - window

    -- Remove expired records outside current window
    redis.call('ZREMRANGEBYSCORE', key, 0, clearBefore)

    -- Count active entries
    local currentRequests = redis.call('ZCARD', key)

    if currentRequests >= limit then
        return {0, currentRequests}
    end

    -- Add current request
    redis.call('ZADD', key, now, now .. '-' .. math.random(100000, 999999))
    redis.call('EXPIRE', key, math.ceil(window / 1000))

    return {1, currentRequests + 1}
end)
`

type Result struct {
	Allowed   bool
	Remaining int64
	Current   int64
}

type SlidingWindow struct {
	client    *redis.Client
	maxReqs   int64
	windowSec int64
}

func NewSlidingWindow(ctx context.Context, client *redis.Client, maxReqs, windowSec int64) (*SlidingWindow, error) {
	// Register the Redis 7 Function library on startup (REPLACE mode updates if modified)
	err := client.FunctionLoadReplace(ctx, redisFunctionLib).Err()
	if err != nil && err.Error() != "ERR Library 'ratelimit_lib' already exists" {
		// Log warning but continue if already loaded
		fmt.Printf("Notice on function load: %v\n", err)
	}

	return &SlidingWindow{
		client:    client,
		maxReqs:   maxReqs,
		windowSec: windowSec,
	}, nil
}

func (sw *SlidingWindow) Allow(ctx context.Context, identifier string) (*Result, error) {
	key := fmt.Sprintf("ratelimit:%s", identifier)
	now := time.Now().UnixNano() / int64(time.Millisecond)
	windowMs := sw.windowSec * 1000

	keys := []string{key}
	args := []interface{}{now, windowMs, sw.maxReqs}

	// Call Redis 7 Function using FCALL
	rawRes, err := sw.client.FCall(ctx, "rate_limit_check", keys, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("FCALL rate_limit_check failed: %w", err)
	}

	resSlice, ok := rawRes.([]interface{})
	if !ok || len(resSlice) < 2 {
		return nil, fmt.Errorf("invalid function return structure")
	}

	allowed := resSlice[0].(int64) == 1
	current := resSlice[1].(int64)
	remaining := sw.maxReqs - current
	if remaining < 0 {
		remaining = 0
	}

	return &Result{
		Allowed:   allowed,
		Remaining: remaining,
		Current:   current,
	}, nil
}
