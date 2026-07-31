package limiter

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis 7+ Function Library for Sliding Window Counter
const slidingWindowCounterLib = `#!lua name=sw_counter_lib
redis.register_function('sw_counter_check', function(keys, args)
    local curKey = keys[1]
    local prevKey = keys[2]
    local nowMs = tonumber(args[1])
    local windowMs = tonumber(args[2])
    local limit = tonumber(args[3])
    local ttlSec = tonumber(args[4])

    local curCount = tonumber(redis.call('GET', curKey) or '0')
    local prevCount = tonumber(redis.call('GET', prevKey) or '0')

    -- Calculate current window progress (0.0 to 1.0)
    local windowStartMs = nowMs - (nowMs % windowMs)
    local timeIntoCurrentWindow = nowMs - windowStartMs
    local weight = (windowMs - timeIntoCurrentWindow) / windowMs

    -- Weighted estimation
    local estimatedCount = math.floor(prevCount * weight + curCount)

    if estimatedCount >= limit then
        return {0, math.max(0, limit - estimatedCount)}
    end

    redis.call('INCR', curKey)
    redis.call('EXPIRE', curKey, ttlSec)

    return {1, math.max(0, limit - (estimatedCount + 1))}
end)
`

type SlidingWindowCounter struct {
	client    *redis.Client
	maxReqs   int64
	windowSec int64
}

func NewSlidingWindowCounter(ctx context.Context, client *redis.Client, maxReqs, windowSec int64) (*SlidingWindowCounter, error) {
	err := client.FunctionLoadReplace(ctx, slidingWindowCounterLib).Err()
	if err != nil && err.Error() != "ERR Library 'sw_counter_lib' already exists" {
		fmt.Printf("Notice on sw_counter_lib load: %v\n", err)
	}

	return &SlidingWindowCounter{
		client:    client,
		maxReqs:   maxReqs,
		windowSec: windowSec,
	}, nil
}

func (sw *SlidingWindowCounter) Allow(ctx context.Context, identifier string) (*Result, error) {
	nowMs := time.Now().UnixNano() / int64(time.Millisecond)
	windowMs := sw.windowSec * 1000
	currentBucket := nowMs / windowMs
	previousBucket := currentBucket - 1

	curKey := fmt.Sprintf("swc:%s:%d", identifier, currentBucket)
	prevKey := fmt.Sprintf("swc:%s:%d", identifier, previousBucket)
	ttlSec := sw.windowSec * 2

	keys := []string{curKey, prevKey}
	args := []interface{}{nowMs, windowMs, sw.maxReqs, ttlSec}

	rawRes, err := sw.client.FCall(ctx, "sw_counter_check", keys, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("FCALL sw_counter_check failed: %w", err)
	}

	resSlice, ok := rawRes.([]interface{})
	if !ok || len(resSlice) < 2 {
		return nil, fmt.Errorf("invalid sw_counter_check response structure")
	}

	allowed := resSlice[0].(int64) == 1
	remaining := resSlice[1].(int64)

	return &Result{
		Allowed:   allowed,
		Remaining: remaining,
		ResetSec:  sw.windowSec,
	}, nil
}
