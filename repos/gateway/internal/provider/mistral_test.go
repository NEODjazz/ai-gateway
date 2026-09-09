package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestMistralFIMThroughRouter(t *testing.T) {
	if NewMistral("", "", false).SupportsResponses() {
		t.Fatal("unverified Responses transport advertised")
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/proxy/v1/fim/completions" || r.Header.Get("Authorization") != "Bearer provider-key" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != "codestral-upstream" || request["prompt"] != "masked" || request["suffix"] != "return sum" || request["max_tokens"] != float64(40) || request["random_seed"] != float64(7) || request["stream"] != false {
			t.Fatalf("FIM request fields changed: %+v", request)
		}
		if _, found := request["seed"]; found {
			t.Fatal("compatible seed field leaked into native FIM request")
		}
		_, _ = fmt.Fprint(w, `{"id":"fim-1","object":"chat.completion","created":10,"model":"codestral-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"(a, b) { return sum }"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":6,"total_tokens":14}}`)
	}))
	defer server.Close()

	maxTokens := 40
	seed := int64(7)
	request := openai.CompletionRequest{Model: "codestral-public", Prompt: "func add", Suffix: "return sum", MaxTokens: &maxTokens, Seed: &seed}
	lifecycle := &completionLifecycleModule{}
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "mistral-native", Type: "mistral", BaseURL: server.URL + "/proxy", APIKey: "provider-key",
		Models: []string{"codestral-public"}, ModelAliases: map[string]string{"codestral-public": "codestral-upstream"}, Capabilities: []string{"chat"},
	}}, Modules: modules.NewPipeline([]modules.Module{lifecycle})}).(*Router)
	response, err := router.Completions(context.Background(), modules.RequestContext{
		CompletionRequest: &request, Request: openai.ChatCompletionRequest{Model: request.Model, Messages: []openai.Message{{Role: "user", Content: request.Prompt}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || lifecycle.pre != 1 || lifecycle.post != 1 || lifecycle.response == nil || lifecycle.response.Usage.TotalTokens != 14 || response.Object != "text_completion" || response.Model != "codestral-upstream" || len(response.Choices) != 1 || response.Choices[0].Text != "(a, b) { return sum }" || response.Usage.TotalTokens != 14 {
		t.Fatalf("response=%+v calls=%d", response, calls.Load())
	}
}

func TestMistralFIMStreamingThroughRouter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/fim/completions" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("unexpected stream request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"fim-stream\",\"object\":\"chat.completion.chunk\",\"created\":20,\"model\":\"codestral\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"first\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"fim-stream\",\"object\":\"chat.completion.chunk\",\"created\":20,\"model\":\"codestral\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" second\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	request := openai.CompletionRequest{Model: "codestral", Prompt: "start", Stream: true}
	lifecycle := &completionLifecycleModule{}
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "mistral", Type: "mistral", BaseURL: server.URL, Stream: true, Models: []string{"codestral"}, Capabilities: []string{"chat", "stream"}}}, Modules: modules.NewPipeline([]modules.Module{lifecycle})}).(*Router)
	payloads := []string{}
	response, streamed, err := router.StreamCompletions(context.Background(), modules.RequestContext{CompletionRequest: &request, Request: openai.ChatCompletionRequest{Model: request.Model, Stream: true, Messages: []openai.Message{{Role: "user", Content: request.Prompt}}}}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil || !streamed {
		t.Fatalf("streamed=%v err=%v", streamed, err)
	}
	if lifecycle.pre != 1 || lifecycle.post != 1 || lifecycle.response == nil || lifecycle.response.Usage.TotalTokens != 5 || len(payloads) != 2 || !strings.Contains(payloads[0], `"object":"text_completion"`) || !strings.Contains(payloads[1], `"finish_reason":"stop"`) || response.Choices[0].Text != "first second" || response.Usage.TotalTokens != 5 {
		t.Fatalf("payloads=%v response=%+v", payloads, response)
	}
}

func TestMistralFIMRejectsUnsupportedParametersBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	one, enabled := 1, true
	for _, test := range []struct {
		name, param string
		request     openai.CompletionRequest
	}{
		{name: "prompt list", param: "prompt", request: openai.CompletionRequest{Prompt: []string{"one"}}},
		{name: "best of", param: "best_of", request: openai.CompletionRequest{Prompt: "one", BestOf: &one}},
		{name: "echo", param: "echo", request: openai.CompletionRequest{Prompt: "one", Echo: &enabled}},
		{name: "n", param: "n", request: openai.CompletionRequest{Prompt: "one", N: &one}},
		{name: "logprobs", param: "logprobs", request: openai.CompletionRequest{Prompt: "one", Logprobs: &one}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewMistral(server.URL, "key", false).Completions(context.Background(), test.request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Param != test.param || failure.UpstreamCode != "unsupported_parameter" {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported request reached Mistral")
	}
}

func TestMistralFIMRejectsTruncatedStream(t *testing.T) {
	stream := `data: {"id":"fim","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}` + "\n\n"
	_, err := streamMistralFIM(strings.NewReader(stream), openai.CompletionRequest{Prompt: "p"}, nil)
	if err == nil || !strings.Contains(err.Error(), "before [DONE]") {
		t.Fatalf("truncated stream accepted: %v", err)
	}
}

func TestMistralFIMRejectsNonStringContentAndChangedStreamIdentity(t *testing.T) {
	invalidResponse := `{"id":"fim","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":{"text":"hidden"}},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	if _, err := decodeMistralFIM(strings.NewReader(invalidResponse), openai.CompletionRequest{Prompt: "p"}); err == nil {
		t.Fatal("non-string FIM content accepted")
	}
	changedIdentity := strings.Join([]string{
		`data: {"id":"first","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`,
		`data: {"id":"second","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	if _, err := streamMistralFIM(strings.NewReader(changedIdentity), openai.CompletionRequest{Prompt: "p"}, nil); err == nil || !strings.Contains(err.Error(), "changed stream ID") {
		t.Fatalf("changed stream identity accepted: %v", err)
	}
}
