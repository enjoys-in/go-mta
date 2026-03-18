package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/enjoys-in/go-mta/pkg/cache"
	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/types"
)

// Limiter enforces per-domain / per-IP send rate limits backed by Dragonfly.
type Limiter struct {
	cache  cache.Cache
	limit  int64
	window time.Duration
	log    *logger.Logger
}

// New creates a rate limiter.
//
//	limit  – max requests per window
//	window – sliding window duration (e.g. 1*time.Second)
func New(c cache.Cache, limit int64, window time.Duration) *Limiter {
	return &Limiter{
		cache:  c,
		limit:  limit,
		window: window,
		log:    logger.New(types.ComponentRateLimit),
	}
}

// Allow checks whether a send to the given key (domain or IP) is permitted.
// Returns true if within the limit; false if rate-limited.
func (l *Limiter) Allow(ctx context.Context, key string) (bool, error) {
	cacheKey := fmt.Sprintf("gomta:rl:%s", key)

	count, err := l.cache.Incr(ctx, cacheKey)
	if err != nil {
		return false, fmt.Errorf("ratelimit: incr: %w", err)
	}

	// First request in the window — set TTL.
	if count == 1 {
		if err := l.cache.Expire(ctx, cacheKey, l.window); err != nil {
			return false, fmt.Errorf("ratelimit: expire: %w", err)
		}
	}

	if count > l.limit {
		l.log.Warn("rate limited",
			slog.String("key", key),
			slog.Int64("count", count),
			slog.Int64("limit", l.limit),
		)
		return false, nil
	}

	return true, nil
}

// Reset clears the counter for a key.
func (l *Limiter) Reset(ctx context.Context, key string) error {
	return l.cache.Del(ctx, fmt.Sprintf("gomta:rl:%s", key))
}
