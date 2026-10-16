package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

func TestOllamaManagedDeploymentHTTPDeadline(t *testing.T) {
	for _, test := range []struct {
		name             string
		timeoutMS        int
		parentTimeout    time.Duration
		wantHTTPDeadline time.Duration
	}{
		{name: "default remains bounded", wantHTTPDeadline: 180 * time.Second},
		{name: "short explicit timeout", timeoutMS: 120000, wantHTTPDeadline: 120 * time.Second},
		{name: "long explicit timeout", timeoutMS: 600000, wantHTTPDeadline: 600 * time.Second},
		{name: "earlier caller deadline", timeoutMS: 600000, parentTimeout: 30 * time.Second, wantHTTPDeadline: 30 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := managedTimeoutOllama(t, test.timeoutMS)
			var remaining time.Duration
			client.client.Transport = ollamaRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				deadline, ok := request.Context().Deadline()
				if !ok {
					t.Error("outbound request has no deadline")
				}
				remaining = time.Until(deadline)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"model","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":1}`))}, nil
			})
			ctx := t.Context()
			if test.parentTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, test.parentTimeout)
				defer cancel()
			}
			_, err := client.ChatCompletions(ctx, openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
			if err != nil {
				t.Fatal(err)
			}
			// Observe the actual outbound request budget without waiting for it.
			if remaining <= test.wantHTTPDeadline-5*time.Second || remaining > test.wantHTTPDeadline {
				t.Fatalf("outbound deadline has %s remaining, want approximately %s", remaining, test.wantHTTPDeadline)
			}
		})
	}
}

func TestOllamaManagedDeploymentPreservesCallerCancellation(t *testing.T) {
	client := managedTimeoutOllama(t, 600000)
	client.client.Transport = ollamaRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.ChatCompletions(ctx, openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation was lost: %v", err)
	}
}

func managedTimeoutOllama(t *testing.T, timeoutMS int) Ollama {
	t.Helper()
	router := New(Config{}).(*Router)
	endpoint, err := router.endpointForManagedDeploymentWithSecret(
		ModelDeployment{ID: "ollama-deployment", ProviderID: "ollama", Models: []string{"model"}, Capabilities: []string{"chat", "stream"}, RequestTimeoutMS: timeoutMS},
		ManagedProvider{ID: "ollama", Type: "ollama", BaseURL: "http://ollama.test", Enabled: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	return endpoint.Provider.(Ollama)
}
