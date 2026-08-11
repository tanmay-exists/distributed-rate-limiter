package limiter

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// newTestRedisClient connects to a live Redis 7+ instance for integration
// tests that exercise real FCALL execution (Lua functions cannot be
// faithfully faked by an in-memory mock). Set TEST_REDIS_ADDR to run these;
// otherwise they're skipped. CI sets this against a redis:7-alpine service
// container - see .github/workflows/ci.yml.
func newTestRedisClient(t *testing.T) *redis.Client {
	t.Helper()

	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set; skipping integration test (requires a live Redis 7+ instance)")
	}

	client := redis.NewClient(&redis.Options{Addr: addr})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("could not connect to test redis at %s: %v", addr, err)
	}

	t.Cleanup(func() {
		_ = client.Close()
	})

	return client
}

func uniqueIdentifier(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test:%s:%d", t.Name(), time.Now().UnixNano())
}
