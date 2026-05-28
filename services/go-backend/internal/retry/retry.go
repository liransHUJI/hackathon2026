package retry

import (
	"context"
	"time"
)

func WithBackoff(ctx context.Context, attempts int, baseDelay time.Duration, fn func() error) error {
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := fn(); err != nil {
			last = err
		} else {
			return nil
		}
		delay := baseDelay * (1 << attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return last
}
