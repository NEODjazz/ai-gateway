package provider

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// AdmissionError means that an endpoint had no execution slot and its bounded
// queue could not accept the request before the configured deadline.
type AdmissionError struct {
	Provider   string
	RetryAfter time.Duration
}

func (e *AdmissionError) Error() string {
	return fmt.Sprintf("%s provider is busy", e.Provider)
}

type admissionController struct {
	slots         chan struct{}
	queueCapacity int64
	queueTimeout  time.Duration
	queued        atomic.Int64
}

func newAdmissionController(maxParallel, queueCapacity int, queueTimeout time.Duration) *admissionController {
	if maxParallel <= 0 {
		return nil
	}
	return &admissionController{
		slots:         make(chan struct{}, maxParallel),
		queueCapacity: int64(queueCapacity),
		queueTimeout:  queueTimeout,
	}
}

func (a *admissionController) acquire(ctx context.Context, providerName string) (func(), error) {
	if a == nil {
		return func() {}, nil
	}
	select {
	case a.slots <- struct{}{}:
		return a.release, nil
	default:
	}

	if a.queueCapacity <= 0 || a.queued.Add(1) > a.queueCapacity {
		if a.queueCapacity > 0 {
			a.queued.Add(-1)
		}
		return nil, &AdmissionError{Provider: providerName, RetryAfter: retryAfterDuration(a.queueTimeout)}
	}
	defer a.queued.Add(-1)

	timer := time.NewTimer(a.queueTimeout)
	defer timer.Stop()
	select {
	case a.slots <- struct{}{}:
		return a.release, nil
	case <-timer.C:
		return nil, &AdmissionError{Provider: providerName, RetryAfter: retryAfterDuration(a.queueTimeout)}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *admissionController) release() {
	<-a.slots
}

func retryAfterDuration(queueTimeout time.Duration) time.Duration {
	if queueTimeout < time.Second {
		return time.Second
	}
	return queueTimeout
}
