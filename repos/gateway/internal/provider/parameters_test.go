package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestNativeAdaptersRejectUnrepresentableChatParameters(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	seed := int64(0)
	parallel := false
	for _, tc := range []struct {
		adapter string
		field   string
		request openai.ChatCompletionRequest
	}{
		{"anthropic", "stop", openai.ChatCompletionRequest{Stop: 42}},
		{"anthropic", "seed", openai.ChatCompletionRequest{Seed: &seed}},
		{"ollama", "tool_choice", openai.ChatCompletionRequest{ToolChoice: "required"}},
		{"ollama", "parallel_tool_calls", openai.ChatCompletionRequest{ParallelToolCalls: &parallel}},
	} {
		t.Run(tc.adapter+"/"+tc.field, func(t *testing.T) {
			var client interface {
				Client
				StreamingClient
			}
			if tc.adapter == "anthropic" {
				client = NewAnthropic(server.URL, "test", true)
			} else {
				client = NewOllama(server.URL, true)
			}
			_, err := client.ChatCompletions(context.Background(), tc.request)
			assertUnsupportedParameter(t, err, tc.field)
			_, err = client.StreamChatCompletions(context.Background(), tc.request, func(string) error { t.Error("unexpected stream output"); return nil })
			assertUnsupportedParameter(t, err, tc.field)
		})
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported request reached upstream")
	}
}

func TestNativeResponseAndEmbeddingParameterPolicy(t *testing.T) {
	for _, tc := range []struct {
		field   string
		request openai.ResponseRequest
	}{
		{"previous_response_id", openai.ResponseRequest{PreviousResponse: "resp_other"}},
	} {
		client := NewAnthropic("http://unused.invalid", "test", true)
		_, err := client.Responses(context.Background(), tc.request)
		assertUnsupportedParameter(t, err, tc.field)
		_, err = client.StreamResponses(context.Background(), tc.request, func(string, string) error { t.Error("unexpected event"); return nil })
		assertUnsupportedParameter(t, err, tc.field)
	}
	for _, tc := range []struct {
		field   string
		request openai.EmbeddingRequest
	}{
		{"user", openai.EmbeddingRequest{User: "customer"}},
		{"encoding_format", openai.EmbeddingRequest{EncodingFormat: "base64"}},
	} {
		_, err := NewOllama("http://unused.invalid", false).Embeddings(context.Background(), tc.request)
		assertUnsupportedParameter(t, err, tc.field)
	}
}

func assertUnsupportedParameter(t *testing.T, err error, param string) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.StatusCode != 400 || failure.Class != FailureClientRequest || failure.UpstreamCode != "unsupported_parameter" || failure.Param != param {
		t.Fatalf("expected terminal unsupported parameter %s, got %#v", param, err)
	}
}

func TestRouterValidatesParametersBeforeProviderModules(t *testing.T) {
	router := Router{endpoints: []Endpoint{{Name: "native", Type: "anthropic", Models: []string{"test"}, Provider: NewAnthropic("http://unused.invalid", "test", true), Capabilities: []string{"chat", "responses", "stream"}}}, modules: modules.NewPipeline([]modules.Module{rejectingModule{}}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}}
	seed := int64(1)
	req := modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "test", Seed: &seed}}
	_, err := router.ChatCompletions(context.Background(), req)
	assertUnsupportedParameter(t, err, "seed")
	_, _, err = router.StreamChatCompletions(context.Background(), req, func(string) error { return nil })
	assertUnsupportedParameter(t, err, "seed")
	req.ResponseRequest = &openai.ResponseRequest{Model: "test", PreviousResponse: "resp_old"}
	_, err = router.Responses(context.Background(), req)
	assertUnsupportedParameter(t, err, "previous_response_id")
	_, _, err = router.StreamResponses(context.Background(), req, func(string, string) error { return nil })
	assertUnsupportedParameter(t, err, "previous_response_id")
}
