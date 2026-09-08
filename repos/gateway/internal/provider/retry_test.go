package provider

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

type retrySequenceClient struct {
	errors []error
	calls  int
}

func (c *retrySequenceClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	index := c.calls
	c.calls++
	if index < len(c.errors) && c.errors[index] != nil {
		return openai.ChatCompletionResponse{}, c.errors[index]
	}
	return openai.ChatCompletionResponse{ID: "ok"}, nil
}

func (c *retrySequenceClient) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, errors.New("not used")
}

func TestRetryAfterHeaderParsing(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		headers  http.Header
		expected time.Duration
	}{
		{name: "seconds", headers: http.Header{"Retry-After": []string{"1.5"}}, expected: 1500 * time.Millisecond},
		{name: "milliseconds", headers: http.Header{"Retry-After-Ms": []string{"250"}}, expected: 250 * time.Millisecond},
		{name: "http date", headers: http.Header{"Retry-After": []string{now.Add(3 * time.Second).Format(http.TimeFormat)}}, expected: 3 * time.Second},
		{name: "invalid", headers: http.Header{"Retry-After": []string{"tomorrow"}}, expected: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if actual := retryAfterFromHeaders(test.headers, now); actual != test.expected {
				t.Fatalf("delay=%s expected=%s", actual, test.expected)
			}
		})
	}
}

func TestResponseStatusErrorPreservesRetryAfter(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"Retry-After": []string{"2"}},
	}
	err := responseStatusError("test", response)
	var providerErr *Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected provider error, got %T: %v", err, err)
	}
	if providerErr.RetryAfter != 2*time.Second {
		t.Fatalf("retry after=%s", providerErr.RetryAfter)
	}
}

func TestRetrySchedulerUsesProviderDelayAndCappedExponentialBackoff(t *testing.T) {
	scheduler := retryScheduler{jitter: func(time.Duration) time.Duration { return 25 * time.Millisecond }}
	providerDelay := &Error{Class: FailureRateLimit, RetryAfter: 1500 * time.Millisecond, Err: errors.New("limited")}
	if delay := scheduler.delay(providerDelay, 0); delay != 1525*time.Millisecond {
		t.Fatalf("provider delay=%s", delay)
	}
	for retry, expected := range []time.Duration{225 * time.Millisecond, 425 * time.Millisecond, 825 * time.Millisecond, 1625 * time.Millisecond, 2025 * time.Millisecond} {
		if delay := scheduler.delay(statusError("test", http.StatusServiceUnavailable), retry); delay != expected {
			t.Fatalf("retry=%d delay=%s expected=%s", retry, delay, expected)
		}
	}
	tooLong := &Error{Class: FailureRateLimit, RetryAfter: 2 * time.Minute, Err: errors.New("limited")}
	if delay := scheduler.delay(tooLong, 0); delay != 225*time.Millisecond {
		t.Fatalf("unbounded Retry-After was trusted: %s", delay)
	}
}

func TestCallChatWaitsBetweenRetries(t *testing.T) {
	client := &retrySequenceClient{errors: []error{
		&Error{Class: FailureUnavailable, RetryAfter: 1500 * time.Millisecond, Err: errors.New("first")},
		statusError("test", http.StatusServiceUnavailable),
	}}
	var delays []time.Duration
	router := Router{
		health: newEndpointHealthTracker(),
		retry: retryScheduler{
			wait:   func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil },
			jitter: func(time.Duration) time.Duration { return 0 },
		},
	}
	response, retries, err := router.callChat(context.Background(), Endpoint{Name: "test", MaxRetries: 2, Provider: client}, openai.ChatCompletionRequest{Model: "model"})
	if err != nil || response.ID != "ok" || retries != 2 || client.calls != 3 {
		t.Fatalf("response=%+v retries=%d calls=%d err=%v", response, retries, client.calls, err)
	}
	if len(delays) != 2 || delays[0] != 1500*time.Millisecond || delays[1] != 400*time.Millisecond {
		t.Fatalf("delays=%v", delays)
	}
}

func TestRetryStopsBeforeParentDeadline(t *testing.T) {
	client := &retrySequenceClient{errors: []error{statusError("test", http.StatusServiceUnavailable)}}
	waits := 0
	router := Router{
		health: newEndpointHealthTracker(),
		retry: retryScheduler{
			wait:   func(context.Context, time.Duration) error { waits++; return nil },
			jitter: func(time.Duration) time.Duration { return 0 },
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, retries, err := router.callChat(ctx, Endpoint{Name: "test", MaxRetries: 2, Provider: client}, openai.ChatCompletionRequest{Model: "model"})
	if !errors.Is(err, context.DeadlineExceeded) || retries != 0 || client.calls != 1 || waits != 0 {
		t.Fatalf("retries=%d calls=%d waits=%d err=%v", retries, client.calls, waits, err)
	}
}

func TestRetryAfterRejectsOverflowAndNonFiniteValues(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for _, raw := range []string{"1e30", "+Inf", "Infinity", "NaN", "9223372036854775807"} {
		for _, field := range []string{"Retry-After", "Retry-After-Ms"} {
			headers := http.Header{field: []string{raw}}
			if delay := retryAfterFromHeaders(headers, now); delay != 0 {
				t.Errorf("field=%s raw=%s delay=%v", field, raw, delay)
			}
		}
		headers := http.Header{"Retry-After-Ms": []string{raw}, "Retry-After": []string{"2"}}
		if delay := retryAfterFromHeaders(headers, now); delay != 2*time.Second {
			t.Errorf("invalid milliseconds blocked seconds fallback: raw=%s delay=%v", raw, delay)
		}
	}
}
