package limiter

import "context"

type Result struct {
	Allowed   bool
	Remaining int64
	ResetSec  int64
}

// Limiter defines the contract for any rate-limiting algorithm strategy.
type Limiter interface {
	Allow(ctx context.Context, identifier string) (*Result, error)
}
