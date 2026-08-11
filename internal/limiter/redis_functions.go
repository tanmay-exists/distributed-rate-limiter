package limiter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// loadFunctionWithRetry registers a Redis Function Library, retrying briefly
// to absorb the short window during startup where Sentinel may not have
// finished electing/confirming a master yet. "Already exists" is treated as
// success (idempotent redeploys/restarts).
//
// This used to be a fire-and-forget fmt.Printf that swallowed the error on
// any failure other than "already exists" - which meant a container could
// start "successfully" with its rate-limiting Lua function never actually
// registered. Every FCALL would then fail, tripping the circuit breaker
// almost immediately and silently failing every request open forever. This
// now returns a real error so startup fails loudly (see main.go's
// log.Fatalf) instead of running in a permanently-broken, always-allow state.
func loadFunctionWithRetry(ctx context.Context, client *redis.Client, lib, libName string) error {
	const maxAttempts = 6
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := client.FunctionLoadReplace(ctx, lib).Err()
		if err == nil {
			return nil
		}
		if strings.Contains(err.Error(), fmt.Sprintf("Library '%s' already exists", libName)) {
			return nil
		}

		lastErr = err
		if attempt < maxAttempts {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
	}

	return fmt.Errorf("failed to load Redis function library %q after %d attempts: %w", libName, maxAttempts, lastErr)
}
