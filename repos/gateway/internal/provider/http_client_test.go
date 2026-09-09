package provider

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

func TestProviderHTTPClientRejectsRedirects(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	response, err := newProviderHTTPClient(time.Second).Get(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || targetCalls.Load() != 0 {
		t.Fatalf("status=%d target calls=%d", response.StatusCode, targetCalls.Load())
	}
}

func TestCredentialedProviderAdaptersDoNotFollowRedirects(t *testing.T) {
	var sourceCalls, targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	for name, client := range map[string]Client{
		"openai-compatible": NewOpenAICompatible(source.URL, "provider-key", false),
		"anthropic":         NewAnthropic(source.URL, "provider-key", false),
		"mistral":           NewMistral(source.URL, "provider-key", false),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.ChatCompletions(t.Context(), request); err == nil {
				t.Fatal("redirect response accepted")
			}
		})
	}
	if sourceCalls.Load() != 3 || targetCalls.Load() != 0 {
		t.Fatalf("source calls=%d target calls=%d", sourceCalls.Load(), targetCalls.Load())
	}
}
