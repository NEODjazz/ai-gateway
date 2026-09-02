package provider

import (
	"context"
	"math/rand/v2"
	"time"
)

const (
	retryInitialDelay = 200 * time.Millisecond
	retryMaximumDelay = 2 * time.Second
	retryJitterLimit  = 100 * time.Millisecond
	maximumRetryAfter = 60 * time.Second
)

type retryScheduler struct {
	wait   func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

func newRetryScheduler() retryScheduler {
	return retryScheduler{
		wait: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		jitter: func(limit time.Duration) time.Duration {
			if limit <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(limit) + 1))
		},
	}
}

func (s retryScheduler) delay(err error, retry int) time.Duration {
	delay := retryInitialDelay
	if retry > 0 {
		for step := 0; step < retry && delay < retryMaximumDelay; step++ {
			delay *= 2
			if delay > retryMaximumDelay {
				delay = retryMaximumDelay
			}
		}
	}
	if retryAfter := providerRetryAfter(err); retryAfter > 0 && retryAfter <= maximumRetryAfter {
		delay = retryAfter
	}
	if s.jitter != nil {
		delay += s.jitter(retryJitterLimit)
	}
	return delay
}

func (s retryScheduler) beforeRetry(ctx context.Context, err error, retry int) error {
	// Focused Router unit tests often construct a zero Router directly.
	// Production routers are initialized with newRetryScheduler in NewWithError.
	if s.wait == nil {
		return nil
	}
	delay := s.delay(err, retry)
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
		return context.DeadlineExceeded
	}
	return s.wait(ctx, delay)
}
