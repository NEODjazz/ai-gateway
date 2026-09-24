package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestOllamaMapsMaxCompletionTokensToNumPredict(t *testing.T) {
	limit := 321
	options := ollamaRequestOptions(openai.ChatCompletionRequest{MaxCompletionTokens: &limit})
	if options.NumPredict == nil || *options.NumPredict != limit {
		t.Fatalf("max_completion_tokens was not mapped: %+v", options)
	}
}

func TestOllamaFinishReasonMapping(t *testing.T) {
	for _, test := range []struct {
		reason       string
		hasToolCalls bool
		want         string
	}{
		{reason: "", want: "stop"},
		{reason: "", hasToolCalls: true, want: "tool_calls"},
		{reason: "stop", hasToolCalls: true, want: "tool_calls"},
		{reason: "length", hasToolCalls: true, want: "length"},
	} {
		if got, err := ollamaFinishReason(test.reason, test.hasToolCalls); err != nil || got != test.want {
			t.Fatalf("reason=%q tools=%t mapped to %q, want %q; err=%v", test.reason, test.hasToolCalls, got, test.want, err)
		}
	}
}

func TestOllamaFinishReasonRejectsUnknownValues(t *testing.T) {
	for _, reason := range []string{"eos", "error", "content_filter"} {
		if got, err := ollamaFinishReason(reason, false); err == nil || got != "" {
			t.Fatalf("reason=%q mapped to %q with err=%v", reason, got, err)
		}
	}
}

func TestOllamaChatRejectsUnknownUpstreamFinishReason(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"bad"},"done":true,"done_reason":"eos","prompt_eval_count":2,"eval_count":1}`))
			}))
			t.Cleanup(server.Close)
			client := NewOllama(server.URL, true)
			request := openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
			var err error
			if stream {
				var chunks []string
				_, err = client.StreamChatCompletions(t.Context(), request, func(chunk string) error {
					chunks = append(chunks, chunk)
					return nil
				})
				if len(chunks) != 0 {
					t.Fatalf("invalid terminal chunk was forwarded: %v", chunks)
				}
			} else {
				_, err = client.ChatCompletions(t.Context(), request)
			}
			if err == nil || !strings.Contains(err.Error(), "finish reason") {
				t.Fatalf("unknown upstream finish reason accepted: %v", err)
			}
		})
	}
}

func TestNormalizeOllamaToolCallsAssignsDistinctIDs(t *testing.T) {
	message := openai.Message{ToolCalls: []openai.ToolCall{{}, {}, {ID: "provider-id"}}}
	normalizeOllamaToolCalls(&message)
	first, second, preserved := message.ToolCalls[0], message.ToolCalls[1], message.ToolCalls[2]
	if !strings.HasPrefix(first.ID, "call_") || !strings.HasPrefix(second.ID, "call_") || first.ID == second.ID || preserved.ID != "provider-id" {
		t.Fatalf("tool call IDs were not normalized: %+v", message.ToolCalls)
	}
	for _, call := range message.ToolCalls {
		if call.Type != "function" {
			t.Fatalf("tool call type was not normalized: %+v", call)
		}
	}
}

func TestNormalizeOllamaBaseURL(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{input: "https://ollama.com", want: "https://ollama.com"},
		{input: "https://ollama.com/api/", want: "https://ollama.com"},
		{input: "https://ollama.com/v1", want: "https://ollama.com"},
		{input: "https://proxy.example.test/ollama/api", want: "https://proxy.example.test/ollama"},
		{input: "https://proxy.example.test/ollama/v1", want: "https://proxy.example.test/ollama"},
		{input: "https://api", want: "https://api"},
	} {
		if got := normalizeOllamaBaseURL(test.input); got != test.want {
			t.Errorf("base URL %q normalized to %q, want %q", test.input, got, test.want)
		}
	}
}

func TestOllamaManagedCredentialAuthenticatesInference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cloud-token" {
			t.Errorf("missing Ollama bearer credential on %s", r.URL.Path)
			http.Error(w, "missing credential", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/chat":
			var upstream ollamaChatRequest
			if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
				t.Error(err)
				return
			}
			if upstream.Stream {
				_, _ = w.Write([]byte("{\"model\":\"test-model\",\"message\":{\"role\":\"assistant\",\"content\":\"hello\"}}\n"))
				_, _ = w.Write([]byte("{\"model\":\"test-model\",\"done\":true,\"prompt_eval_count\":2,\"eval_count\":1}\n"))
				return
			}
			_, _ = w.Write([]byte(`{"model":"test-model","message":{"role":"assistant","content":"hello"},"done":true,"prompt_eval_count":2,"eval_count":1}`))
		case "/api/embed":
			_, _ = w.Write([]byte(`{"model":"test-model","embeddings":[[0.1,0.2]],"prompt_eval_count":2}`))
		case "/v1/completions":
			_, _ = w.Write([]byte(`{"id":"cmpl-ollama","object":"text_completion","created":7,"model":"test-model","choices":[{"index":0,"text":"done","finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	router := New(Config{CredentialEncryptionKey: []byte("ollama-cloud-test-key")}).(*Router)
	endpoint, err := router.endpointForManagedDeploymentWithSecret(ModelDeployment{ID: "ollama-deployment", ProviderID: "ollama", Models: []string{"test-model"}, Capabilities: []string{"chat", "stream", "embeddings", "completions"}}, ManagedProvider{ID: "ollama", Type: "ollama", BaseURL: server.URL + "/api", Enabled: true}, "cloud-token")
	if err != nil {
		t.Fatal(err)
	}
	client, ok := endpoint.Provider.(Ollama)
	if !ok {
		t.Fatalf("unexpected Ollama client type: %T", endpoint.Provider)
	}
	if _, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test-model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test-model", Stream: true, Messages: []openai.Message{{Role: "user", Content: "hello"}}}, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "test-model", Input: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Completions(t.Context(), openai.CompletionRequest{Model: "test-model", Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
}

func TestOllamaDiscoveryNormalizesAPIBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cloud-token" {
			t.Errorf("unexpected Ollama discovery request: %s headers=%v", r.URL.String(), r.Header)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"test-model"}]}`))
		case "/api/show":
			var body struct {
				Model string `json:"model"`
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "test-model" {
				t.Errorf("unexpected model detail request: %s", r.URL.String())
			}
			_, _ = w.Write([]byte(`{"capabilities":["completion","tools","vision"]}`))
		default:
			t.Errorf("unexpected discovery path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	router := New(Config{CredentialEncryptionKey: []byte("ollama-discovery-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "ollama", Type: "ollama", BaseURL: server.URL + "/api", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "ollama-token", ProviderID: "ollama", Secret: "cloud-token"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "ollama", "ollama-token")
	if err != nil || len(models) != 1 || models[0].ID != "test-model" || !slices.Equal(models[0].Capabilities, []string{"chat", "completions", "responses", "stream", "tools", "vision"}) {
		t.Fatalf("models=%+v err=%v", models, err)
	}
}

func TestOllamaDiscoveryDoesNotAdvertiseUnknownCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"embedding-model"},{"name":"unknown-model"}]}`))
		case "/api/show":
			var body struct {
				Model string `json:"model"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid model detail request")
			}
			if body.Model == "embedding-model" {
				_, _ = w.Write([]byte(`{"capabilities":["embedding","thinking"]}`))
			} else {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "ollama", Type: "ollama", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "ollama", "")
	if err != nil || len(models) != 2 || !slices.Equal(models[0].Capabilities, []string{"embeddings"}) || len(models[1].Capabilities) != 0 {
		t.Fatalf("models=%+v err=%v", models, err)
	}
}

func TestOllamaProbeOnlyListsModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("probe requested model details: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"model"}]}`))
	}))
	t.Cleanup(server.Close)
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "ollama", Type: "ollama", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	probe, err := router.TestProvider(t.Context(), "ollama", "")
	if err != nil || probe.Status != "available" || probe.ModelCount != 1 {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
}

func TestOllamaDiscoveryBoundsModelDetailRequests(t *testing.T) {
	var detailRequests atomic.Int32
	tags := struct {
		Models []map[string]string `json:"models"`
	}{Models: make([]map[string]string, 129)}
	for index := range tags.Models {
		tags.Models[index] = map[string]string{"name": fmt.Sprintf("model-%03d", index)}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			if err := json.NewEncoder(w).Encode(tags); err != nil {
				t.Error(err)
			}
		case "/api/show":
			detailRequests.Add(1)
			_, _ = w.Write([]byte(`{"capabilities":["completion"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "ollama", Type: "ollama", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "ollama", "")
	if err != nil || len(models) != 129 || detailRequests.Load() != 128 || len(models[128].Capabilities) != 0 {
		t.Fatalf("models=%d detail requests=%d last capabilities=%v err=%v", len(models), detailRequests.Load(), models[128].Capabilities, err)
	}
}

func TestOllamaDiscoveryRequiresModelsField(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		wantErr bool
	}{
		{name: "missing", payload: `{}`, wantErr: true},
		{name: "null", payload: `{"models":null}`, wantErr: true},
		{name: "upstream error envelope", payload: `{"error":"temporarily unavailable"}`, wantErr: true},
		{name: "empty list", payload: `{"models":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			models, err := parseDiscoveredModels("ollama", []byte(test.payload))
			if (err != nil) != test.wantErr || !test.wantErr && len(models) != 0 {
				t.Fatalf("models=%v err=%v", models, err)
			}
		})
	}
}

func TestOllamaProbeRejectsMalformedModelList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("unexpected discovery path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"error":"temporarily unavailable"}`))
	}))
	t.Cleanup(server.Close)
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "ollama", Type: "ollama", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	probe, err := router.TestProvider(t.Context(), "ollama", "")
	if !errors.Is(err, ErrProviderProbeFailed) || probe.Status != "unavailable" {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
}

func TestOllamaLocalRequestsDoNotSendAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("unexpected local authorization: %q", got)
		}
		_, _ = w.Write([]byte(`{"model":"test-model","message":{"role":"assistant","content":"hello"},"done":true,"prompt_eval_count":2,"eval_count":1}`))
	}))
	t.Cleanup(server.Close)
	if _, err := NewOllama(server.URL, false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test-model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestOllamaEmbeddingsRejectUpstreamTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path=%q", r.URL.Path)
		}
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if got := string(request["truncate"]); got != "false" {
			t.Errorf("truncate=%q, want explicit false", got)
		}
		if string(request["input"]) == `"too long"` {
			http.Error(w, `{"error":"input exceeds context window"}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"model":"embed-model","embeddings":[[0.1,0.2]],"prompt_eval_count":2}`))
	}))
	t.Cleanup(server.Close)
	response, err := NewOllama(server.URL, false).Embeddings(t.Context(), openai.EmbeddingRequest{Model: "embed-model", Input: "full input"})
	if err != nil || response.Usage.PromptTokens != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if _, err := NewOllama(server.URL, false).Embeddings(t.Context(), openai.EmbeddingRequest{Model: "embed-model", Input: "too long"}); err == nil {
		t.Fatal("upstream context-window rejection was ignored")
	}
}

func TestOllamaBearerCredentialDoesNotFollowRedirect(t *testing.T) {
	var forwarded atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Store(true) }))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)
	if _, err := newOllamaWithToken(source.URL, "cloud-token", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test-model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}); err == nil {
		t.Fatal("credential-bearing redirect was accepted")
	}
	if forwarded.Load() {
		t.Fatal("credential reached redirect target")
	}
}

func TestOllamaCompletionsUsesProviderContract(t *testing.T) {
	var upstream openAICompatibleCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"cmpl-ollama","object":"text_completion","created":7,"model":"phi3","choices":[{"index":0,"text":"done","finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	defer server.Close()
	maxTokens := 12
	response, err := NewOllama(server.URL, false).Completions(t.Context(), openai.CompletionRequest{Model: "phi3", Prompt: "complete", MaxTokens: &maxTokens})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Prompt != "complete" || upstream.MaxTokens == nil || *upstream.MaxTokens != 12 || upstream.Stream {
		t.Fatalf("unexpected upstream request: %+v", upstream)
	}
	if response.Choices[0].Text != "done" || response.Usage.TotalTokens != 4 {
		t.Fatalf("unexpected completion response: %+v", response)
	}
}

func TestOllamaCompletionsRejectIgnoredParametersBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	one, echo := 1, false
	for _, test := range []struct {
		name    string
		param   string
		request openai.CompletionRequest
	}{
		{name: "best_of", param: "best_of", request: openai.CompletionRequest{BestOf: &one}},
		{name: "echo", param: "echo", request: openai.CompletionRequest{Echo: &echo}},
		{name: "logit_bias", param: "logit_bias", request: openai.CompletionRequest{LogitBias: map[string]int{}}},
		{name: "n", param: "n", request: openai.CompletionRequest{N: &one}},
		{name: "user", param: "user", request: openai.CompletionRequest{User: "client-user"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := test.request
			request.Model = "test-model"
			request.Prompt = "complete"
			client := NewOllama(server.URL, true)
			_, err := client.Completions(t.Context(), request)
			assertUnsupportedParameter(t, err, test.param)
			request.Stream = true
			_, err = client.StreamCompletions(t.Context(), request, func(string) error { return nil })
			assertUnsupportedParameter(t, err, test.param)
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("ignored completion parameters reached Ollama: calls=%d", calls.Load())
	}
}

func TestOllamaStreamsProviderCompletions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upstream openAICompatibleCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		if !upstream.Stream {
			t.Fatal("native completion stream was not requested")
		}
		if upstream.StreamOptions == nil || !upstream.StreamOptions.IncludeUsage {
			t.Fatal("completion stream usage was not requested")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"cmpl-ollama\",\"object\":\"text_completion\",\"created\":7,\"model\":\"phi3\",\"choices\":[{\"index\":0,\"text\":\"hello\",\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	var payloads []string
	response, err := NewOllama(server.URL, true).StreamCompletions(t.Context(), openai.CompletionRequest{Model: "phi3", Prompt: "complete", Stream: true}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 2 || response.Choices[0].Text != "hello" || response.Usage.TotalTokens != 4 {
		t.Fatalf("unexpected completion stream: payloads=%v response=%+v", payloads, response)
	}
}

func TestOllamaCompletionsRequireExactUsage(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		wantError   bool
	}{
		{"missing", "", true},
		{"prompt missing", `,"usage":{"completion_tokens":2,"total_tokens":2}`, true},
		{"completion missing", `,"usage":{"prompt_tokens":3,"total_tokens":3}`, true},
		{"total missing", `,"usage":{"prompt_tokens":3,"completion_tokens":2}`, true},
		{"inconsistent", `,"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":6}`, true},
		{"reported zero", `,"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, false},
		{"reported counts", `,"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`, false},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/completions" {
						t.Errorf("unexpected Ollama Completions path: %s", r.URL.Path)
					}
					if stream {
						var request openAICompatibleCompletionRequest
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.StreamOptions == nil || !request.StreamOptions.IncludeUsage {
							t.Errorf("Ollama Completions did not request stream usage: %+v err=%v", request, err)
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprint(w, `data: {"id":"cmpl-1","object":"text_completion","created":1,"model":"model","choices":[{"index":0,"text":"ok","finish_reason":"stop"}]}`+"\n\n")
						_, _ = fmt.Fprint(w, `data: {"id":"cmpl-1","object":"text_completion","created":1,"model":"model","choices":[]`+tc.usage+`}`+"\n\n")
						_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
					} else {
						_, _ = fmt.Fprint(w, `{"id":"cmpl-1","object":"text_completion","created":1,"model":"model","choices":[{"index":0,"text":"ok","finish_reason":"stop"}]`+tc.usage+`}`)
					}
				}))
				t.Cleanup(server.Close)
				client := NewOllama(server.URL, stream)
				request := openai.CompletionRequest{Model: "model", Prompt: "hello", Stream: stream}
				var response openai.CompletionResponse
				var err error
				deliveredUsage := false
				if stream {
					response, err = client.StreamCompletions(t.Context(), request, func(payload string) error {
						if strings.Contains(payload, `"usage":{`) {
							deliveredUsage = true
						}
						return nil
					})
				} else {
					response, err = client.Completions(t.Context(), request)
				}
				if tc.wantError {
					if err == nil || !strings.Contains(err.Error(), "usage") {
						t.Fatalf("invalid Ollama Completions usage accepted: response=%+v err=%v", response, err)
					}
					if deliveredUsage {
						t.Fatal("invalid Ollama Completions usage chunk was forwarded")
					}
				} else if err != nil || !response.UsageReported {
					t.Fatalf("valid Ollama Completions usage rejected: response=%+v err=%v", response, err)
				} else if stream && !deliveredUsage {
					t.Fatal("valid Ollama Completions usage chunk was not forwarded")
				}
			})
		}
	}
}

func TestOllamaChatCompletions(t *testing.T) {
	promptTokens, completionTokens := 4, 2
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var request ollamaChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "test-model" {
			t.Fatalf("unexpected model: %s", request.Model)
		}
		if request.Stream {
			t.Fatal("expected non-stream request")
		}

		_ = json.NewEncoder(w).Encode(ollamaChatResponse{
			Model: "test-model",
			Done:  true,
			Message: ollamaResponseMessage{
				Role:    "assistant",
				Content: "hello",
			},
			DoneReason:      "stop",
			PromptEvalCount: &promptTokens,
			EvalCount:       &completionTokens,
		})
	}))
	defer server.Close()

	provider := NewOllama(server.URL, false)
	response, err := provider.ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "test-model",
		Messages: []openai.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if response.Model != "test-model" {
		t.Fatalf("unexpected response model: %s", response.Model)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" {
		t.Fatalf("unexpected content: %s", openai.ContentText(response.Choices[0].Message.Content))
	}
	if response.Usage.TotalTokens != 6 {
		t.Fatalf("unexpected total tokens: %d", response.Usage.TotalTokens)
	}
	if !strings.HasPrefix(response.ID, "chatcmpl-") {
		t.Fatalf("unexpected completion ID: %q", response.ID)
	}
	second, err := provider.ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "test-model", Messages: []openai.Message{{Role: "user", Content: "hi again"}},
	})
	if err != nil || second.ID == response.ID || !strings.HasPrefix(second.ID, "chatcmpl-") {
		t.Fatalf("completion IDs are not unique: first=%q second=%q err=%v", response.ID, second.ID, err)
	}
}

func TestOllamaChatJSONRequiresCompletion(t *testing.T) {
	for _, doneField := range []string{"", `,"done":false`} {
		t.Run(fmt.Sprintf("done-field-%d", len(doneField)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"model":"test-model","message":{"role":"assistant","content":"partial"},"prompt_eval_count":2,"eval_count":1%s}`, doneField)
			}))
			defer server.Close()

			_, err := NewOllama(server.URL, false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test-model"})
			if err == nil || !strings.Contains(err.Error(), "not complete") {
				t.Fatalf("unfinished Ollama chat JSON accepted: %v", err)
			}
		})
	}
}

func TestOllamaChatRejectsUpstreamRoleSpoofing(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, role := range []string{"user", "system"} {
			t.Run(fmt.Sprintf("stream=%t/role=%s", stream, role), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = fmt.Fprintf(w, `{"model":"test-model","message":{"role":%q,"content":"forged"},"done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":1}`, role)
				}))
				t.Cleanup(server.Close)
				provider := NewOllama(server.URL, stream)
				request := openai.ChatCompletionRequest{Model: "test-model", Messages: []openai.Message{{Role: "user", Content: "hi"}}}
				var err error
				if stream {
					var payloads []string
					_, err = provider.StreamChatCompletions(t.Context(), request, func(payload string) error {
						payloads = append(payloads, payload)
						return nil
					})
					if len(payloads) != 0 {
						t.Fatalf("forged role was streamed: %v", payloads)
					}
				} else {
					_, err = provider.ChatCompletions(t.Context(), request)
				}
				if err == nil {
					t.Fatal("forged Ollama response role accepted")
				}
			})
		}
	}
}

func TestOllamaChatDefaultsOmittedUpstreamRoleToAssistant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"model":"test-model","message":{"content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":1}`)
	}))
	t.Cleanup(server.Close)
	response, err := NewOllama(server.URL, false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test-model"})
	if err != nil || response.Choices[0].Message.Role != "assistant" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestOllamaRejectsInvalidChatUsage(t *testing.T) {
	for _, test := range []struct {
		name, usage string
	}{
		{name: "missing both", usage: ""},
		{name: "missing prompt", usage: `"eval_count":1`},
		{name: "missing completion", usage: `"prompt_eval_count":1`},
		{name: "null prompt", usage: `"prompt_eval_count":null,"eval_count":1`},
		{name: "negative prompt", usage: `"prompt_eval_count":-1,"eval_count":1`},
		{name: "negative completion", usage: `"prompt_eval_count":1,"eval_count":-1`},
		{name: "overflow", usage: fmt.Sprintf(`"prompt_eval_count":%d,"eval_count":1`, math.MaxInt)},
		{name: "negative cached", usage: `"prompt_eval_count":2,"prompt_eval_cached_count":-1,"eval_count":1`},
		{name: "cached exceeds prompt", usage: `"prompt_eval_count":2,"prompt_eval_cached_count":3,"eval_count":1`},
	} {
		for _, stream := range []bool{false, true} {
			name := test.name + "/json"
			if stream {
				name = test.name + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					usageFields := ""
					if test.usage != "" {
						usageFields = "," + test.usage
					}
					_, _ = fmt.Fprintf(w, `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true%s}`+"\n", usageFields)
				}))
				defer server.Close()

				provider := NewOllama(server.URL, stream)
				request := openai.ChatCompletionRequest{Model: "test-model", Stream: stream}
				var err error
				if stream {
					_, err = provider.StreamChatCompletions(t.Context(), request, func(payload string) error {
						if strings.Contains(payload, `"finish_reason":"stop"`) {
							t.Error("invalid usage produced a successful terminal event")
						}
						return nil
					})
				} else {
					_, err = provider.ChatCompletions(t.Context(), request)
				}
				if err == nil || !strings.Contains(err.Error(), "invalid Ollama chat usage") {
					t.Fatalf("expected invalid usage error, got %v", err)
				}
			})
		}
	}
}

func TestOllamaChatStreamValidatesTerminalUsageBeforeForwarding(t *testing.T) {
	for _, test := range []struct {
		name       string
		usage      string
		wantError  bool
		wantChunks int
	}{
		{name: "missing usage", usage: "", wantError: true},
		{name: "invalid usage", usage: `,"prompt_eval_count":-1,"eval_count":1`, wantError: true},
		{name: "valid usage", usage: `,"prompt_eval_count":2,"eval_count":1`, wantChunks: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"model":"test-model","message":{"role":"assistant","content":"terminal text"},"done":true%s}`+"\n", test.usage)
			}))
			t.Cleanup(server.Close)
			var payloads []string
			_, err := NewOllama(server.URL, true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test-model", Stream: true}, func(payload string) error {
				payloads = append(payloads, payload)
				return nil
			})
			if (err != nil) != test.wantError || len(payloads) != test.wantChunks {
				t.Fatalf("err=%v payloads=%v", err, payloads)
			}
			if !test.wantError && (!strings.Contains(payloads[0], "terminal text") || !strings.Contains(payloads[1], `"finish_reason":"stop"`)) {
				t.Fatalf("valid terminal content was not forwarded: %v", payloads)
			}
		})
	}
}

func TestOllamaChatPreservesCachedPromptUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "json"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintln(w, `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":10,"prompt_eval_cached_count":7,"eval_count":3}`)
			}))
			t.Cleanup(server.Close)
			provider := NewOllama(server.URL, stream)
			request := openai.ChatCompletionRequest{Model: "test-model", Stream: stream}
			var response openai.ChatCompletionResponse
			var err error
			if stream {
				response, err = provider.StreamChatCompletions(t.Context(), request, func(string) error { return nil })
			} else {
				response, err = provider.ChatCompletions(t.Context(), request)
			}
			if err != nil || response.Usage.PromptTokens != 10 || response.Usage.CompletionTokens != 3 || response.Usage.TotalTokens != 13 || response.Usage.PromptTokensDetails == nil || response.Usage.PromptTokensDetails.CachedTokens != 7 {
				t.Fatalf("response usage=%+v err=%v", response.Usage, err)
			}
		})
	}
}

func TestOllamaChatUsageMaxBoundary(t *testing.T) {
	promptTokens, completionTokens := math.MaxInt, 0
	usage, err := ollamaChatUsage(&promptTokens, nil, &completionTokens)
	if err != nil || usage.TotalTokens != math.MaxInt {
		t.Fatalf("unexpected boundary usage: %+v, %v", usage, err)
	}
	promptTokens = 0
	usage, err = ollamaChatUsage(&promptTokens, nil, &completionTokens)
	if err != nil || usage.TotalTokens != 0 {
		t.Fatalf("valid zero usage rejected: %+v, %v", usage, err)
	}
}

func TestOllamaChatResponseReadLimit(t *testing.T) {
	var response ollamaChatResponse
	err := decodeOllamaChatResponse(strings.NewReader(strings.Repeat(" ", maxChatCompletionResponseBytes+1)), &response)
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("oversized Ollama chat response accepted: %v", err)
	}
	err = decodeOllamaChatResponse(strings.NewReader(`{"model":"test"}{"model":"second"}`), &response)
	if err == nil {
		t.Fatal("multiple Ollama chat response objects accepted")
	}
}

func TestOllamaChatStreamReadLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat(" ", maxResponseStreamBytes+1)))
	}))
	defer server.Close()

	forwarded := 0
	_, err := NewOllama(server.URL, true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test-model", Stream: true}, func(string) error {
		forwarded++
		return nil
	})
	if !errors.Is(err, errResponseStreamTooLarge) || forwarded != 0 {
		t.Fatalf("oversized Ollama chat stream accepted: err=%v forwarded=%d", err, forwarded)
	}
}

func TestOllamaChatStreamRequiresTerminalChunk(t *testing.T) {
	for _, body := range []string{
		"",
		`{"model":"test-model","message":{"role":"assistant","content":"partial"},"done":false}` + "\n",
	} {
		t.Run(fmt.Sprintf("bytes-%d", len(body)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			var payloads []string
			_, err := NewOllama(server.URL, true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test-model", Stream: true}, func(payload string) error {
				payloads = append(payloads, payload)
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "without a terminal chunk") {
				t.Fatalf("unfinished Ollama stream accepted: %v", err)
			}
			for _, payload := range payloads {
				if strings.Contains(payload, `"finish_reason":"stop"`) {
					t.Fatalf("unfinished stream emitted success: %v", payloads)
				}
			}
		})
	}
}

func TestOllamaNativeReasoningRoundTrip(t *testing.T) {
	var upstream ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"model":"qwen3","message":{"role":"assistant","thinking":"new plan","content":"answer"},"done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":1}`))
	}))
	defer server.Close()

	response, err := NewOllama(server.URL, false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "qwen3",
		Messages: []openai.Message{
			{Role: "user", Content: "question"},
			{Role: "assistant", ReasoningContent: "prior plan", Content: "prior answer"},
		},
		ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Think != "high" || len(upstream.Messages) != 2 || upstream.Messages[1].Thinking != "prior plan" {
		t.Fatalf("native reasoning request was not preserved: %+v", upstream)
	}
	if response.Choices[0].Message.ReasoningContent != "new plan" || openai.ContentText(response.Choices[0].Message.Content) != "answer" {
		t.Fatalf("native reasoning response was not preserved: %+v", response)
	}
}

func TestOllamaStreamsNativeReasoning(t *testing.T) {
	var upstream ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("{\"model\":\"qwen3\",\"message\":{\"role\":\"assistant\",\"thinking\":\"plan \"}}\n"))
		_, _ = w.Write([]byte("{\"model\":\"qwen3\",\"message\":{\"role\":\"assistant\",\"thinking\":\"more\",\"content\":\"answer\"}}\n"))
		_, _ = w.Write([]byte("{\"model\":\"qwen3\",\"done\":true,\"done_reason\":\"stop\",\"prompt_eval_count\":2,\"eval_count\":1}\n"))
	}))
	defer server.Close()

	var payloads []string
	response, err := NewOllama(server.URL, true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "qwen3", Stream: true, Messages: []openai.Message{{Role: "user", Content: "question"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "medium"},
	}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Think != "medium" {
		t.Fatalf("native reasoning level=%#v", upstream.Think)
	}
	if response.Choices[0].Message.ReasoningContent != "plan more" || openai.ContentText(response.Choices[0].Message.Content) != "answer" {
		t.Fatalf("native reasoning stream was not collected: %+v", response)
	}
	joined := strings.Join(payloads, "\n")
	if !strings.Contains(joined, `"reasoning_content":"plan "`) || !strings.Contains(joined, `"reasoning_content":"more"`) || !strings.Contains(joined, `"content":"answer"`) {
		t.Fatalf("native reasoning stream was not translated: %s", joined)
	}
}

func TestOllamaReasoningEffortContract(t *testing.T) {
	for _, level := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "default"} {
		t.Run(level, func(t *testing.T) {
			request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: level}}
			if err := (Ollama{}).ValidateChatParameters(request); err != nil {
				t.Fatalf("supported reasoning level rejected: %v", err)
			}
			if level == "none" {
				if disabled, ok := ollamaThink(level).(bool); !ok || disabled {
					t.Fatalf("none mapped to %#v", ollamaThink(level))
				}
			} else if level == "default" {
				if ollamaThink(level) != nil {
					t.Fatalf("default mapped to %#v", ollamaThink(level))
				}
			} else if level == "minimal" {
				if ollamaThink(level) != "low" {
					t.Fatalf("minimal mapped to %#v", ollamaThink(level))
				}
			} else if level == "xhigh" {
				if ollamaThink(level) != "max" {
					t.Fatalf("xhigh mapped to %#v", ollamaThink(level))
				}
			} else if ollamaThink(level) != level {
				t.Fatalf("%s mapped to %#v", level, ollamaThink(level))
			}
		})
	}
}

func TestOllamaReasoningEffortAliasesReachNativeChat(t *testing.T) {
	for _, test := range []struct{ effort, wantThink string }{{"minimal", "low"}, {"xhigh", "max"}} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.effort, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					if string(body["think"]) != fmt.Sprintf("%q", test.wantThink) {
						t.Errorf("native think=%s, want %q", body["think"], test.wantThink)
					}
					if stream {
						_, _ = fmt.Fprintln(w, `{"model":"m","done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":0}`)
					} else {
						_, _ = fmt.Fprint(w, `{"model":"m","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":1}`)
					}
				}))
				t.Cleanup(server.Close)
				request := openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: test.effort}}
				client := NewOllama(server.URL, true)
				var err error
				if stream {
					_, err = client.StreamChatCompletions(t.Context(), request, func(string) error { return nil })
				} else {
					_, err = client.ChatCompletions(t.Context(), request)
				}
				if err != nil || request.ReasoningEffort != test.effort {
					t.Fatalf("err=%v original effort=%q", err, request.ReasoningEffort)
				}
			})
		}
	}
}

func TestOllamaChatDefaultReasoningUsesModelDefault(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if _, supplied := body["think"]; supplied {
					t.Errorf("model default was overridden: %s", body["think"])
				}
				if stream {
					_, _ = fmt.Fprintln(w, `{"model":"m","done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":0}`)
				} else {
					_, _ = fmt.Fprint(w, `{"model":"m","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":1}`)
				}
			}))
			t.Cleanup(server.Close)
			request := openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "default"}}
			client := NewOllama(server.URL, true)
			var err error
			if stream {
				_, err = client.StreamChatCompletions(t.Context(), request, func(string) error { return nil })
			} else {
				_, err = client.ChatCompletions(t.Context(), request)
			}
			if err != nil || request.ReasoningEffort != "default" {
				t.Fatalf("err=%v original effort=%q", err, request.ReasoningEffort)
			}
		})
	}
}

func TestOllamaRejectsOversizedReasoningResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaChatResponse{Model: "qwen3", Done: true, Message: ollamaResponseMessage{Role: "assistant", Thinking: strings.Repeat("x", openai.MaxChatReasoningContentBytes+1)}})
	}))
	defer server.Close()

	if _, err := NewOllama(server.URL, false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "qwen3"}); err == nil || !strings.Contains(err.Error(), "reasoning content") {
		t.Fatalf("oversized native reasoning was not rejected: %v", err)
	}
}

func TestOllamaRejectsOversizedReasoningStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		remaining := openai.MaxChatReasoningContentBytes + 1
		for remaining > 0 {
			size := min(remaining, 32<<10)
			_ = json.NewEncoder(w).Encode(ollamaChatResponse{Model: "qwen3", Message: ollamaResponseMessage{Role: "assistant", Thinking: strings.Repeat("x", size)}})
			remaining -= size
		}
	}))
	defer server.Close()

	_, err := NewOllama(server.URL, true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "qwen3", Stream: true}, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "reasoning stream exceeds limit") {
		t.Fatalf("oversized native reasoning stream was not rejected: %v", err)
	}
}

func TestOllamaNormalizesToolArgumentsAndForwardsOptions(t *testing.T) {
	var upstream ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"model":"llama3.2:latest","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"weather.get","arguments":{"city":"Moscow"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":1}`))
	}))
	defer server.Close()

	maxTokens := 32
	temperature := 0.0
	topP := 0.7
	topK := 40
	minP := 0.05
	seed := int64(42)
	response, err := NewOllama(server.URL, false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "llama3.2:latest",
		Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather.get"}}},
		Messages: []openai.Message{{
			Role: "assistant", ToolCalls: []openai.ToolCall{{
				ID: "previous", Type: "function",
				Function: openai.FunctionCall{Name: "weather.get", Arguments: `{"city":"Kazan"}`},
			}},
		}},
		MaxTokens: &maxTokens, Temperature: &temperature, TopP: &topP, Seed: &seed,
		ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &topK, MinP: &minP, ReasoningEffort: "none"},
		Stop:                  []string{"END"},
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments, ok := upstream.Messages[0].ToolCalls[0].Function.Arguments.(map[string]any)
	if !ok || arguments["city"] != "Kazan" {
		t.Fatalf("tool arguments were not converted to an Ollama object: %#v", upstream.Messages[0].ToolCalls[0].Function.Arguments)
	}
	if upstream.Options.NumPredict == nil || *upstream.Options.NumPredict != 32 ||
		upstream.Options.Temperature == nil || *upstream.Options.Temperature != 0 ||
		upstream.Options.TopP == nil || *upstream.Options.TopP != 0.7 ||
		upstream.Options.TopK == nil || *upstream.Options.TopK != 40 ||
		upstream.Options.MinP == nil || *upstream.Options.MinP != 0.05 ||
		upstream.Options.Seed == nil || *upstream.Options.Seed != 42 {
		t.Fatalf("generation options were not forwarded: %+v", upstream.Options)
	}
	if disabled, ok := upstream.Think.(bool); !ok || disabled {
		t.Fatalf("reasoning disable was not forwarded: %#v", upstream.Think)
	}
	if len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 ||
		response.Choices[0].FinishReason != "tool_calls" ||
		!strings.HasPrefix(response.Choices[0].Message.ToolCalls[0].ID, "call_") ||
		response.Choices[0].Message.ToolCalls[0].Type != "function" ||
		response.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("Ollama tool arguments were not normalized: %+v", response)
	}
}

func TestOllamaStreamsNativeToolCalls(t *testing.T) {
	var upstream ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("{\"model\":\"llama3.2:latest\",\"message\":{\"role\":\"assistant\",\"tool_calls\":[{\"function\":{\"name\":\"weather.get\",\"arguments\":{\"city\":\"Moscow\"}}}]}}\n"))
		_, _ = w.Write([]byte("{\"model\":\"llama3.2:latest\",\"done\":true,\"done_reason\":\"stop\",\"prompt_eval_count\":2,\"eval_count\":1}\n"))
	}))
	defer server.Close()

	var payloads []string
	topK := 20
	minP := 0.1
	response, err := NewOllama(server.URL, true).StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "llama3.2:latest", Stream: true, Messages: []openai.Message{{Role: "user", Content: "weather"}},
		Tools:                 []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather.get"}}},
		ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &topK, MinP: &minP, ReasoningEffort: "none"},
	}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Type != "function" || response.Choices[0].FinishReason != "tool_calls" || !strings.HasPrefix(response.Choices[0].Message.ToolCalls[0].ID, "call_") {
		t.Fatalf("streamed tool call was not accumulated: %+v", response)
	}
	if upstream.Options.TopK == nil || *upstream.Options.TopK != 20 || upstream.Options.MinP == nil || *upstream.Options.MinP != 0.1 {
		t.Fatalf("streaming generation options were not forwarded: %+v", upstream.Options)
	}
	if disabled, ok := upstream.Think.(bool); !ok || disabled {
		t.Fatalf("streaming reasoning disable was not forwarded: %#v", upstream.Think)
	}
	if len(payloads) != 2 || !strings.Contains(payloads[0], `"type":"function"`) ||
		!strings.Contains(payloads[0], `"role":"assistant"`) ||
		!strings.Contains(payloads[0], `"id":"`+response.Choices[0].Message.ToolCalls[0].ID+`"`) ||
		!strings.Contains(payloads[1], `"finish_reason":"tool_calls"`) ||
		!strings.Contains(payloads[0], `"arguments":"{\"city\":\"Moscow\"}"`) {
		t.Fatalf("unexpected streamed tool payloads: %v", payloads)
	}
}

func TestOllamaEmptyChatStreamEmitsAssistantRole(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, `{"model":"test-model","message":{"role":"assistant"},"done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":0}`)
	}))
	t.Cleanup(server.Close)
	var payloads []string
	response, err := NewOllama(server.URL, true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "test-model", Stream: true, Messages: []openai.Message{{Role: "user", Content: "hello"}},
	}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 || !strings.Contains(payloads[0], `"role":"assistant"`) || !strings.Contains(payloads[0], `"finish_reason":"stop"`) {
		t.Fatalf("empty stream omitted assistant role: %v", payloads)
	}
	if response.Usage.PromptTokens != 2 || response.Usage.CompletionTokens != 0 {
		t.Fatalf("empty stream usage=%+v", response.Usage)
	}
}

func TestOllamaChatStreamRejectsChangedModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, `{"model":"model-a","message":{"role":"assistant","content":"first"},"done":false}`)
		_, _ = fmt.Fprintln(w, `{"model":"model-b","message":{"role":"assistant","content":"second"},"done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":2}`)
	}))
	t.Cleanup(server.Close)
	var payloads []string
	_, err := NewOllama(server.URL, true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "model-a", Stream: true, Messages: []openai.Message{{Role: "user", Content: "hello"}},
	}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "model changed") {
		t.Fatalf("changed upstream model was accepted: %v", err)
	}
	if len(payloads) != 1 || strings.Contains(payloads[0], "second") {
		t.Fatalf("changed model content reached client: %v", payloads)
	}
}

func TestOllamaNativeLogprobsRoundTrip(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			var upstream ollamaChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
					t.Fatal(err)
				}
				chunk := `{"model":"llama-test","message":{"role":"assistant","content":"é"},"logprobs":[{"token":"é","logprob":-0.1,"bytes":[195,169],"top_logprobs":[{"token":"e","logprob":-0.2,"bytes":[101]}]}]}`
				if streaming {
					_, _ = fmt.Fprintln(w, chunk)
					_, _ = fmt.Fprintln(w, `{"model":"llama-test","done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":1}`)
					return
				}
				_, _ = fmt.Fprint(w, strings.TrimSuffix(chunk, "}")+`,"done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":1}`)
			}))
			defer server.Close()

			enabled := true
			top := 1
			request := openai.ChatCompletionRequest{Model: "llama-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Stream: streaming, ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &enabled, TopLogprobs: &top}}
			var chunks []string
			var response openai.ChatCompletionResponse
			var err error
			client := NewOllama(server.URL, streaming)
			if streaming {
				response, err = client.StreamChatCompletions(context.Background(), request, func(payload string) error {
					chunks = append(chunks, payload)
					return nil
				})
			} else {
				response, err = client.ChatCompletions(context.Background(), request)
			}
			if err != nil || upstream.Logprobs == nil || !*upstream.Logprobs || upstream.TopLogprobs == nil || *upstream.TopLogprobs != 1 {
				t.Fatalf("request=%+v response=%+v err=%v", upstream, response, err)
			}
			logprobs := response.Choices[0].Logprobs
			if logprobs == nil || len(logprobs.Content) != 1 || logprobs.Content[0].Token != "é" || logprobs.Content[0].Logprob != -0.1 || fmt.Sprint(logprobs.Content[0].Bytes) != "[195 169]" || len(logprobs.Content[0].TopLogprobs) != 1 {
				t.Fatalf("logprobs were not normalized: %+v", response)
			}
			if streaming && (len(chunks) != 2 || !strings.Contains(chunks[0], `"role":"assistant"`) || !strings.Contains(chunks[0], `"logprobs":{"content"`)) {
				t.Fatalf("stream logprobs were not preserved: %v", chunks)
			}
		})
	}
}

func TestOllamaRejectsInvalidLogprobs(t *testing.T) {
	token := "x"
	for name, items := range map[string][]ollamaLogprob{
		"positive probability":  {{ollamaTokenLogprob: ollamaTokenLogprob{Token: token, Logprob: 0.1}}},
		"mismatched bytes":      {{ollamaTokenLogprob: ollamaTokenLogprob{Token: token, Logprob: -0.1, Bytes: []int{121}}}},
		"too many alternatives": {{ollamaTokenLogprob: ollamaTokenLogprob{Token: token, Logprob: -0.1}, TopLogprobs: make([]ollamaTokenLogprob, 21)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ollamaChoiceLogprobs(items); err == nil {
				t.Fatal("invalid Ollama logprobs accepted")
			}
		})
	}
}

func TestOllamaRejectsInvalidNativeSamplingOptions(t *testing.T) {
	invalidTopK := -1
	invalidMinP := 1.1
	invalidTopLogprobs := 21
	validTopLogprobs := 1
	logprobs := true
	for _, test := range []struct {
		name    string
		request openai.ChatCompletionRequest
		param   string
	}{
		{name: "top_k", request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &invalidTopK}}, param: "top_k"},
		{name: "min_p", request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{MinP: &invalidMinP}}, param: "min_p"},
		{name: "top_logprobs range", request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &logprobs, TopLogprobs: &invalidTopLogprobs}}, param: "top_logprobs"},
		{name: "top_logprobs dependency", request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{TopLogprobs: &validTopLogprobs}}, param: "top_logprobs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var failure *Error
			err := NewOllama("http://unused.invalid", false).ValidateChatParameters(test.request)
			if !errors.As(err, &failure) || failure.UpstreamCode != "invalid_parameter" || failure.Param != test.param {
				t.Fatalf("invalid sampling option was not rejected: %v", err)
			}
		})
	}
}

func TestOllamaConvertsVisionContentToNativeImages(t *testing.T) {
	input := []openai.Message{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "describe"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo=", "detail": "auto"}},
	}}}
	if err := (Ollama{}).ValidateChatParameters(openai.ChatCompletionRequest{Messages: input}); err != nil {
		t.Fatal(err)
	}
	messages, err := ollamaMessages(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Content != "describe" || len(messages[0].Images) != 1 || messages[0].Images[0] != "iVBORw0KGgo=" {
		t.Fatalf("unexpected native Ollama vision message: %+v", messages)
	}
}

func TestOllamaRejectsInvalidVisionBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected upstream call", http.StatusInternalServerError)
	}))
	defer server.Close()

	request := openai.ChatCompletionRequest{Model: "llava", Messages: []openai.Message{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "describe"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,invalid!"}},
	}}}}
	provider := NewOllama(server.URL, true)
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{"json", func() error { _, err := provider.ChatCompletions(context.Background(), request); return err }},
		{"stream", func() error { _, err := provider.StreamChatCompletions(context.Background(), request, nil); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var failure *Error
			if err := test.run(); !errors.As(err, &failure) || failure.Class != FailureClientRequest || !errors.Is(err, openai.ErrInvalidImage) {
				t.Fatalf("invalid image was not rejected as a client error: %v", err)
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("invalid image reached upstream %d times", got)
	}
}

func TestOllamaRejectsUnmappableChatMessageContent(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected upstream call", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	const imageURL = "data:image/png;base64,iVBORw0KGgo="
	for _, test := range []struct {
		name    string
		message openai.Message
		param   string
	}{
		{"unknown part", openai.Message{Role: "user", Content: []any{map[string]any{"type": "input_audio", "data": "audio"}}}, "messages.content"},
		{"malformed text", openai.Message{Role: "user", Content: []any{map[string]any{"type": "text", "text": 7}}}, "messages.content"},
		{"top-level object", openai.Message{Role: "user", Content: map[string]any{"type": "text", "text": "hello"}}, "messages.content"},
		{"high image detail", openai.Message{Role: "user", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL, "detail": "high"}}}}, "messages.content.image_url.detail"},
		{"unknown image field", openai.Message{Role: "user", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL, "crop": true}}}}, "messages.content.image_url"},
		{"message name", openai.Message{Role: "user", Content: "hello", Name: "alice"}, "messages.name"},
		{"message annotations", openai.Message{Role: "assistant", Content: "answer", Annotations: []openai.ChatAnnotation{{}}}, "messages.annotations"},
		{"reasoning blocks", openai.Message{Role: "assistant", Content: "answer", Reasoning: []openai.ReasoningBlock{{}}}, "messages.reasoning"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{test.message}}
			provider := NewOllama(server.URL, true)
			for _, run := range []func() error{
				func() error { _, err := provider.ChatCompletions(t.Context(), request); return err },
				func() error { _, err := provider.StreamChatCompletions(t.Context(), request, nil); return err },
			} {
				var failure *Error
				if err := run(); !errors.As(err, &failure) || failure.Class != FailureClientRequest || failure.Param != test.param {
					t.Fatalf("unmappable message was not rejected as a client error for %s: %v", test.param, err)
				}
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("unmappable message reached upstream %d times", got)
	}
}

func TestOllamaRejectsUnmappableChatResponseFormats(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected upstream call", http.StatusInternalServerError)
	}))
	defer server.Close()

	strictFalse := false
	for _, test := range []struct {
		name   string
		format openai.ResponseFormat
	}{
		{"unknown type", openai.ResponseFormat{Type: "xml"}},
		{"missing schema", openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "result"}}},
		{"schema with text", openai.ResponseFormat{Type: "text", JSONSchema: &openai.JSONSchemaFormat{Schema: map[string]any{"type": "object"}}}},
		{"non-strict schema", openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "result", Schema: map[string]any{"type": "object"}, Strict: &strictFalse}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ResponseFormat: &test.format}
			provider := NewOllama(server.URL, true)
			for _, run := range []func() error{
				func() error { _, err := provider.ChatCompletions(t.Context(), request); return err },
				func() error { _, err := provider.StreamChatCompletions(t.Context(), request, nil); return err },
			} {
				var failure *Error
				if err := run(); !errors.As(err, &failure) || failure.Class != FailureClientRequest || failure.Param != "response_format" {
					t.Fatalf("unmappable format was not rejected as a client error: %v", err)
				}
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("unmappable response format reached upstream %d times", got)
	}
}

func TestOllamaEmbeddingsMapsNativeContract(t *testing.T) {
	promptTokens := 4
	var upstream ollamaEmbeddingRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(ollamaEmbeddingResponse{Model: "nomic-embed", Embeddings: [][]float64{{0.25, 0.75}, {0.5, 0.5}}, PromptEvalCount: &promptTokens})
	}))
	defer server.Close()
	dimensions := 2
	response, err := NewOllama(server.URL, false).Embeddings(context.Background(), openai.EmbeddingRequest{Model: "nomic-embed", Input: []any{"one", "two"}, Dimensions: &dimensions})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Dimensions == nil || *upstream.Dimensions != 2 {
		t.Fatalf("dimensions not forwarded: %+v", upstream)
	}
	if len(response.Data) != 2 || response.Data[1].Index != 1 || response.Usage.TotalTokens != 4 {
		t.Fatalf("unexpected mapped response: %+v", response)
	}
}

func TestOllamaStreamsChatCompletions(t *testing.T) {
	promptTokens, completionTokens := 4, 2
	var upstreamRequest ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(ollamaChatResponse{
			Model:   "test-model",
			Message: ollamaResponseMessage{Role: "assistant", Content: "hel"},
		})
		_ = json.NewEncoder(w).Encode(ollamaChatResponse{
			Model:   "test-model",
			Message: ollamaResponseMessage{Role: "assistant", Content: "lo"},
		})
		_ = json.NewEncoder(w).Encode(ollamaChatResponse{
			Model:           "test-model",
			Done:            true,
			DoneReason:      "stop",
			PromptEvalCount: &promptTokens,
			EvalCount:       &completionTokens,
		})
	}))
	defer server.Close()

	var payloads []string
	provider := NewOllama(server.URL, true)
	response, err := provider.StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model:  "test-model",
		Stream: true,
		Messages: []openai.Message{
			{Role: "user", Content: "hello"},
		},
	}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !upstreamRequest.Stream {
		t.Fatal("expected upstream stream to be enabled")
	}
	if len(payloads) != 3 {
		t.Fatalf("expected three streamed payloads, got %d: %v", len(payloads), payloads)
	}
	if !strings.HasPrefix(response.ID, "chatcmpl-") {
		t.Fatalf("unexpected streamed completion ID: %q", response.ID)
	}
	for _, payload := range payloads {
		if !strings.Contains(payload, `"id":"`+response.ID+`"`) {
			t.Fatalf("stream chunk has a different completion ID: %s", payload)
		}
	}
	if !strings.Contains(payloads[0], `"content":"hel"`) || !strings.Contains(payloads[1], `"content":"lo"`) {
		t.Fatalf("unexpected streamed payloads: %v", payloads)
	}
	if !strings.Contains(payloads[2], `"finish_reason":"stop"`) {
		t.Fatalf("expected finish payload, got %s", payloads[2])
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" {
		t.Fatalf("unexpected content: %s", openai.ContentText(response.Choices[0].Message.Content))
	}
	if response.Usage.TotalTokens != 6 {
		t.Fatalf("unexpected total tokens: %d", response.Usage.TotalTokens)
	}
}

func TestOllamaResponses(t *testing.T) {
	maxOutputTokens := 11
	temperature := 0.2
	topP := 0.8
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var request openAICompatibleResponseRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "test-model" {
			t.Fatalf("unexpected model: %s", request.Model)
		}
		if request.Stream {
			t.Fatal("expected non-stream responses request")
		}
		if request.MaxOutputTokens == nil || *request.MaxOutputTokens != maxOutputTokens ||
			request.Temperature == nil || *request.Temperature != temperature ||
			request.TopP == nil || *request.TopP != topP || len(request.Tools) != 1 {
			t.Fatalf("Responses fields were not forwarded: %+v", request)
		}

		_ = json.NewEncoder(w).Encode(openai.ResponseResponse{
			ID:     "resp-test",
			Object: "response",
			Status: "completed",
			Model:  "test-model",
			Output: []openai.ResponseOutputItem{
				{
					Type:   "message",
					Status: "completed",
					Role:   "assistant",
					Content: []openai.ResponseOutputContent{
						{Type: "output_text", Text: "pong"},
					},
				},
			},
			Usage: openai.ResponseUsage{
				InputTokens:  3,
				OutputTokens: 1,
				TotalTokens:  4,
			},
		})
	}))
	defer server.Close()

	provider := NewOllama(server.URL, false)
	response, err := provider.Responses(context.Background(), openai.ResponseRequest{
		Model: "test-model", Input: "ping", Stream: true,
		Tools:           []openai.ResponseTool{{Type: "function", Name: "weather.get"}},
		MaxOutputTokens: &maxOutputTokens, Temperature: &temperature, TopP: &topP,
	})
	if err != nil {
		t.Fatal(err)
	}

	if response.OutputText != "pong" {
		t.Fatalf("unexpected output_text: %s", response.OutputText)
	}
	if response.Usage.TotalTokens != 4 {
		t.Fatalf("unexpected total tokens: %d", response.Usage.TotalTokens)
	}
}

func TestOllamaResponsesRejectInvalidUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"resp-test","object":"response","status":"completed","model":"test-model","output":[],"usage":{"input_tokens":-1,"output_tokens":1,"total_tokens":0}}`))
	}))
	defer server.Close()

	_, err := NewOllama(server.URL, false).Responses(t.Context(), openai.ResponseRequest{Model: "test-model", Input: "ping"})
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("invalid Ollama Responses usage accepted: %v", err)
	}
}

const ollamaResponseTestJSON = `{"id":"r","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
const ollamaResponseTestTerminal = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"

func TestOllamaResponsesRequireExactUsage(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		wantError   bool
	}{
		{"missing", "", true},
		{"input missing", `,"usage":{"output_tokens":2,"total_tokens":2}`, true},
		{"output missing", `,"usage":{"input_tokens":3,"total_tokens":3}`, true},
		{"total missing", `,"usage":{"input_tokens":3,"output_tokens":2}`, true},
		{"inconsistent total", `,"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":4}`, true},
		{"reported zero", `,"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"id":"resp-test","object":"response","status":"completed","model":"test-model","output":[]` + tc.usage + `}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			_, err := NewOllama(server.URL, false).Responses(t.Context(), openai.ResponseRequest{Model: "test-model", Input: "ping"})
			if (err != nil) != tc.wantError {
				t.Fatalf("usage=%s err=%v, wantError=%v", tc.usage, err, tc.wantError)
			}
		})
	}
}

func TestOllamaStreamsResponses(t *testing.T) {
	var upstreamRequest openAICompatibleResponseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.output_text.delta` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","response_id":"resp-test","delta":"pon"}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.output_text.delta` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","response_id":"resp-test","delta":"g"}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.completed` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp-test","object":"response","status":"completed","model":"test-model","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"pong"}]}],"output_text":"pong","usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	var events []string
	var payloads []string
	provider := NewOllama(server.URL, true)
	response, err := provider.StreamResponses(context.Background(), openai.ResponseRequest{
		Model:  "test-model",
		Input:  "ping",
		Stream: true,
	}, func(event string, payload string) error {
		events = append(events, event)
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !upstreamRequest.Stream {
		t.Fatal("expected upstream stream to be enabled")
	}
	if len(payloads) != 3 {
		t.Fatalf("expected three streamed payloads, got %d: %v", len(payloads), payloads)
	}
	if events[0] != "response.output_text.delta" || events[2] != "response.completed" {
		t.Fatalf("unexpected events: %v", events)
	}
	if response.OutputText != "pong" {
		t.Fatalf("unexpected output_text: %s", response.OutputText)
	}
	if response.Usage.TotalTokens != 4 {
		t.Fatalf("unexpected streamed usage: %+v", response.Usage)
	}
}

func TestOllamaStreamsResponsesRejectInvalidTerminalUsage(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
	}{
		{"missing", ""},
		{"output missing", `,"usage":{"input_tokens":3,"total_tokens":3}`},
		{"inconsistent total", `,"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":3}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n"))
				_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp-test","status":"completed"` + tc.usage + `}}` + "\n\n"))
			}))
			defer server.Close()
			var events []string
			_, err := NewOllama(server.URL, true).StreamResponses(t.Context(), openai.ResponseRequest{Model: "test-model", Input: "ping"}, func(event, _ string) error {
				events = append(events, event)
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "usage") {
				t.Fatalf("invalid terminal usage accepted: %v", err)
			}
			if len(events) != 1 || events[0] != "response.output_text.delta" {
				t.Fatalf("terminal success was forwarded without valid usage: %v", events)
			}
		})
	}
}
