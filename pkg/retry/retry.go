package retry

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/types"
)

// Strategy defines retry behaviour.
type Strategy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Multiplier  float64
}

// DefaultStrategy returns sensible retry defaults.
func DefaultStrategy() Strategy {
	return Strategy{
		MaxAttempts: types.DefaultMaxRetries,
		BaseDelay:   5 * time.Second,
		MaxDelay:    10 * time.Minute,
		Multiplier:  2.0,
	}
}

// Retrier executes a function with exponential backoff.
type Retrier struct {
	strategy Strategy
	log      *logger.Logger
}

// New creates a Retrier with the given strategy.
func New(s Strategy) *Retrier {
	return &Retrier{
		strategy: s,
		log:      logger.New(types.ComponentRetry),
	}
}

// Do runs fn until it succeeds, context is cancelled, or max attempts exhausted.
// Returns the last error and the number of attempts made.
func (r *Retrier) Do(ctx context.Context, jobID string, fn func() error) (int, error) {
	var lastErr error
	for attempt := 1; attempt <= r.strategy.MaxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return attempt, nil
		}

		if attempt == r.strategy.MaxAttempts {
			break
		}

		delay := r.backoff(attempt)

		r.log.Warn("retrying",
			slog.String("job_id", jobID),
			slog.Int("attempt", attempt),
			slog.String("next_delay", delay.String()),
			slog.String("error", lastErr.Error()),
		)

		select {
		case <-ctx.Done():
			return attempt, ctx.Err()
		case <-time.After(delay):
		}
	}

	return r.strategy.MaxAttempts, lastErr
}

func (r *Retrier) backoff(attempt int) time.Duration {
	delay := float64(r.strategy.BaseDelay) * math.Pow(r.strategy.Multiplier, float64(attempt-1))
	if delay > float64(r.strategy.MaxDelay) {
		delay = float64(r.strategy.MaxDelay)
	}
	return time.Duration(delay)
}

// ShouldRetry returns true if the SMTP error is transient (4xx).
func ShouldRetry(err error) bool {
	if err == nil {
		return false
	}
	// Heuristic: 4xx codes are transient.
	msg := err.Error()
	for i := 0; i < len(msg)-2; i++ {
		if msg[i] == '4' && msg[i+1] >= '0' && msg[i+1] <= '9' && msg[i+2] >= '0' && msg[i+2] <= '9' {
			return true
		}
	}
	return false
}
