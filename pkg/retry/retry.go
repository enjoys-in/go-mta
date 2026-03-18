package retry

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/types"
)

// Strategy defines retry behaviour via an explicit delay schedule.
// Each entry in Schedule is the wait time before the Nth retry.
// len(Schedule) determines the maximum number of retries.
type Strategy struct {
	Schedule []time.Duration
}

// DefaultSchedule is used when no schedule is configured: 1s, 5s, 5m.
var DefaultSchedule = []time.Duration{1 * time.Second, 5 * time.Second, 5 * time.Minute}

// DefaultStrategy returns sensible retry defaults.
func DefaultStrategy() Strategy {
	return Strategy{Schedule: DefaultSchedule}
}

// ParseSchedule converts string durations (e.g. "1s", "5m") into a Strategy.
func ParseSchedule(entries []string) (Strategy, error) {
	if len(entries) == 0 {
		return DefaultStrategy(), nil
	}
	schedule := make([]time.Duration, len(entries))
	for i, s := range entries {
		d, err := time.ParseDuration(s)
		if err != nil {
			return Strategy{}, fmt.Errorf("retry: invalid schedule entry %q: %w", s, err)
		}
		schedule[i] = d
	}
	return Strategy{Schedule: schedule}, nil
}

// Retrier executes a function with schedule-based retries.
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

// MaxAttempts returns 1 (initial) + len(Schedule) retries.
func (r *Retrier) MaxAttempts() int {
	return 1 + len(r.strategy.Schedule)
}

// Do runs fn until it succeeds, context is cancelled, or schedule exhausted.
// Returns the last error and the number of attempts made.
func (r *Retrier) Do(ctx context.Context, jobID string, fn func() error) (int, error) {
	maxAttempts := r.MaxAttempts()
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return attempt, nil
		}

		if attempt == maxAttempts {
			break
		}

		delay := r.strategy.Schedule[attempt-1]

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

	return maxAttempts, lastErr
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
