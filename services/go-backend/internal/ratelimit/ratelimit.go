package ratelimit

import (
	"context"
	"sync"
	"time"
)

type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func NewPerMinute(requests int) *Limiter {
	if requests <= 0 {
		requests = 60
	}
	return &Limiter{interval: time.Minute / time.Duration(requests)}
}

func (l *Limiter) Acquire(ctx context.Context) error {
	l.mu.Lock()
	wait := time.Until(l.next)
	if wait <= 0 {
		l.next = time.Now().Add(l.interval)
		l.mu.Unlock()
		return nil
	}
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
