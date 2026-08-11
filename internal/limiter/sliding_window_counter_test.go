package limiter

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSlidingWindowCounter_AllowsUpToLimit(t *testing.T) {
	client := newTestRedisClient(t)
	ctx := context.Background()

	const limit = 3
	sw, err := NewSlidingWindowCounter(ctx, client, limit, 60)
	if err != nil {
		t.Fatalf("NewSlidingWindowCounter failed: %v", err)
	}

	id := uniqueIdentifier(t)

	for i := 0; i < limit; i++ {
		res, err := sw.Allow(ctx, id)
		if err != nil {
			t.Fatalf("Allow returned error: %v", err)
		}
		if !res.Allowed {
			t.Fatalf("expected request %d to be allowed within limit", i+1)
		}
	}

	res, err := sw.Allow(ctx, id)
	if err != nil {
		t.Fatalf("Allow returned error: %v", err)
	}
	if res.Allowed {
		t.Fatalf("expected request beyond limit to be rejected")
	}
}

// TestSlidingWindowCounter_ConcurrentCorrectness proves the same atomicity
// property as the token bucket test, for the sliding window strategy: N
// goroutines racing on the same identifier still produce an exact count.
func TestSlidingWindowCounter_ConcurrentCorrectness(t *testing.T) {
	client := newTestRedisClient(t)
	ctx := context.Background()

	const limit = 5
	sw, err := NewSlidingWindowCounter(ctx, client, limit, 60)
	if err != nil {
		t.Fatalf("NewSlidingWindowCounter failed: %v", err)
	}

	id := uniqueIdentifier(t)

	const concurrency = 50
	var allowed int64
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			res, err := sw.Allow(ctx, id)
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

	if allowed != limit {
		t.Fatalf("expected exactly %d allowed requests under %d-way concurrency, got %d", limit, concurrency, allowed)
	}
}
