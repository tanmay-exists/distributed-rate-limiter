package limiter

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTokenBucket_SingleRequestConsumesOneToken(t *testing.T) {
	client := newTestRedisClient(t)
	ctx := context.Background()

	tb, err := NewTokenBucket(ctx, client, 5, 1)
	if err != nil {
		t.Fatalf("NewTokenBucket failed: %v", err)
	}

	id := uniqueIdentifier(t)

	res, err := tb.Allow(ctx, id)
	if err != nil {
		t.Fatalf("Allow returned error: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("expected first request to be allowed")
	}
	if res.Remaining != 4 {
		t.Fatalf("expected 4 tokens remaining, got %d", res.Remaining)
	}
}

func TestTokenBucket_RejectsOnceCapacityExhausted(t *testing.T) {
	client := newTestRedisClient(t)
	ctx := context.Background()

	const capacity = 3
	tb, err := NewTokenBucket(ctx, client, capacity, 0) // no refill
	if err != nil {
		t.Fatalf("NewTokenBucket failed: %v", err)
	}

	id := uniqueIdentifier(t)

	for i := 0; i < capacity; i++ {
		res, err := tb.Allow(ctx, id)
		if err != nil {
			t.Fatalf("Allow returned error: %v", err)
		}
		if !res.Allowed {
			t.Fatalf("expected request %d to be allowed within capacity", i+1)
		}
	}

	res, err := tb.Allow(ctx, id)
	if err != nil {
		t.Fatalf("Allow returned error: %v", err)
	}
	if res.Allowed {
		t.Fatalf("expected request beyond capacity to be rejected")
	}
}

// TestTokenBucket_ConcurrentCorrectness hammers a single identifier from many
// goroutines simultaneously and asserts that exactly `capacity` requests are
// allowed - no more, no less. This is the property that makes the limiter
// safe to run behind multiple horizontally-scaled API replicas: correctness
// comes from Redis's single-threaded, atomic execution of the FCALL, not
// from any coordination between application instances.
func TestTokenBucket_ConcurrentCorrectness(t *testing.T) {
	client := newTestRedisClient(t)
	ctx := context.Background()

	const capacity = 15
	// Zero refill rate so the bucket cannot regenerate tokens mid-test,
	// which would make the expected "allowed" count non-deterministic.
	tb, err := NewTokenBucket(ctx, client, capacity, 0)
	if err != nil {
		t.Fatalf("NewTokenBucket failed: %v", err)
	}

	id := uniqueIdentifier(t)

	const concurrency = 100
	var allowed int64
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			res, err := tb.Allow(ctx, id)
			if err != nil {
				t.Errorf("Allow returned error: %v", err)
				return
			}
			if res.Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	wg.Wait()

	if allowed != capacity {
		t.Fatalf("expected exactly %d allowed requests under %d-way concurrency, got %d", capacity, concurrency, allowed)
	}
}
