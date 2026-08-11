package limiter

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis 7+ Function Library for Token Bucket
const tokenBucketLib = `#!lua name=token_bucket_lib
redis.register_function('tb_check', function(keys, args)
    local key = keys[1]
    local capacity = tonumber(args[1])
    local refillRate = tonumber(args[2]) -- tokens per second
    local nowSec = tonumber(args[3])
    local ttlSec = tonumber(args[4])

    local data = redis.call('HMGET', key, 'tokens', 'last_refill')
    local tokens = tonumber(data[1])
    local lastRefill = tonumber(data[2])

    if tokens == nil then
        tokens = capacity
        lastRefill = nowSec
    else
        local delta = math.max(0, nowSec - lastRefill)
        tokens = math.min(capacity, tokens + (delta * refillRate))
        lastRefill = nowSec
    end

    if tokens < 1 then
        redis.call('HMSET', key, 'tokens', tokens, 'last_refill', lastRefill)
        redis.call('EXPIRE', key, ttlSec)
        return {0, math.floor(tokens)}
    end

    tokens = tokens - 1
    redis.call('HMSET', key, 'tokens', tokens, 'last_refill', lastRefill)
    redis.call('EXPIRE', key, ttlSec)

    return {1, math.floor(tokens)}
end)
`

type TokenBucket struct {
	client     *redis.Client
	capacity   int64
	refillRate int64 // Tokens per second
}

func NewTokenBucket(ctx context.Context, client *redis.Client, capacity, refillRate int64) (*TokenBucket, error) {
	if err := loadFunctionWithRetry(ctx, client, tokenBucketLib, "token_bucket_lib"); err != nil {
		return nil, err
	}

	return &TokenBucket{
		client:     client,
		capacity:   capacity,
		refillRate: refillRate,
	}, nil
}

func (tb *TokenBucket) Allow(ctx context.Context, identifier string) (*Result, error) {
	key := fmt.Sprintf("tb:%s", identifier)
	nowSec := time.Now().Unix()
	ttlSec := int64(60)
	if tb.refillRate > 0 {
		ttlSec = (tb.capacity / tb.refillRate) + 60
	}

	keys := []string{key}
	args := []interface{}{tb.capacity, tb.refillRate, nowSec, ttlSec}

	rawRes, err := tb.client.FCall(ctx, "tb_check", keys, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("FCALL tb_check failed: %w", err)
	}

	resSlice, ok := rawRes.([]interface{})
	if !ok || len(resSlice) < 2 {
		return nil, fmt.Errorf("invalid tb_check response structure")
	}

	allowed := resSlice[0].(int64) == 1
	remaining := resSlice[1].(int64)

	return &Result{
		Allowed:   allowed,
		Remaining: remaining,
		ResetSec:  1,
	}, nil
}

func (tb *TokenBucket) Name() string {
	return "token_bucket"
}
