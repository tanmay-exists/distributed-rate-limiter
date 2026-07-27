package limiter

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Embedded Lua script for atomic execution within Redis
const slidingWindowLuaScript = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])

local clearBefore = now - window

-- Remove timestamps outside the active window
redis.call('ZREMRANGEBYSCORE', key, 0, clearBefore)

-- Count remaining requests in window
local currentRequests = redis.call('ZCARD', key)

if currentRequests >= limit then
    return {0, currentRequests}
end

-- Record the new request with current timestamp as score and unique ID as member
redis.call('ZADD', key, now, now .. '-' .. math.random(100000, 999999))

-- Set TTL on key to auto-clean unused entries
redis.call('EXPIRE', key, math.ceil(window))

return {1, currentRequests + 1}
`

type Result struct {
	Allowed   bool
	Remaining int64
	Current   int64
}

type SlidingWindow struct {
	client     *redis.Client
	scriptHash string
	maxReqs    int64
	windowSec  int64
}

func NewSlidingWindow(client *redis.Client, maxReqs, windowSec int64) *SlidingWindow {
	// Pre-compile/load script into Redis script cache to save bandwidth on EVAL
	hash, err := client.ScriptLoad(context.Background(), slidingWindowLuaScript).Result()
	if err != nil {
		// Fallback to storing raw script string if pre-load fails
		hash = ""
	}

	return &SlidingWindow{
		client:     client,
		scriptHash: hash,
		maxReqs:    maxReqs,
		windowSec:  windowSec,
	}
}

func (sw *SlidingWindow) Allow(ctx context.Context, identifier string) (*Result, error) {
	key := fmt.Sprintf("ratelimit:%s", identifier)
	now := time.Now().UnixNano() / int64(time.Millisecond)
	windowMs := sw.windowSec * 1000

	keys := []string{key}
	args := []interface{}{now, windowMs, sw.maxReqs}

	var rawRes interface{}
	var err error

	// Try EVALSHA first for performance; fallback to EVAL on NOSCRIPT error
	if sw.scriptHash != "" {
		rawRes, err = sw.client.EvalSha(ctx, sw.scriptHash, keys, args...).Result()
		if err != nil && err.Error() == "NOSCRIPT No matching script. Please use EVAL." {
			rawRes, err = sw.client.Eval(ctx, slidingWindowLuaScript, keys, args...).Result()
		}
	} else {
		rawRes, err = sw.client.Eval(ctx, slidingWindowLuaScript, keys, args...).Result()
	}

	if err != nil {
		return nil, fmt.Errorf("redis script execution failed: %w", err)
	}

	resSlice, ok := rawRes.([]interface{})
	if !ok || len(resSlice) < 2 {
		return nil, fmt.Errorf("unexpected script output shape")
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
