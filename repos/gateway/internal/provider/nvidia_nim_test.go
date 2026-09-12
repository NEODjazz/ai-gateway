package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestNVIDIANIMInferenceContracts(t *testing.T) {
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path]++
		if r.Header.Get("Authorization") != "Bearer nim-key" {
			t.Fatalf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "model" {
				t.Fatalf("path=%q body=%#v", r.URL.Path, body)
			}
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		case "/v1/completions":
			_, _ = fmt.Fprint(w, `{"id":"completion","object":"text_completion","model":"model","choices":[{"index":0,"text":"ok","finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		case "/v1/responses":
			_, _ = fmt.Fprint(w, `{"id":"response","object":"response","status":"completed","model":"model","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
		case "/v1/embeddings":
			_, _ = fmt.Fprint(w, `{"object":"list","model":"model","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
		case "/v1/ranking":
			if query, ok := body["query"].(map[string]any); !ok || query["text"] != "query" || body["truncate"] != "END" {
				t.Fatalf("ranking body=%#v", body)
			}
			passages, ok := body["passages"].([]any)
			if !ok || len(passages) != 2 || passages[0].(map[string]any)["text"] != "first" {
				t.Fatalf("ranking passages=%#v", body["passages"])
			}
			_, _ = fmt.Fprint(w, `{"rankings":[{"index":1,"logit":2.5},{"index":0,"logit":-1.25}],"usage":{"prompt_tokens":7,"total_tokens":7}}`)
		case "/v1/models":
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"model-b"},{"id":"model-a"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewNVIDIANIM(server.URL, "nim-key", true)
	chat, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || chat.Usage.TotalTokens != 3 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	completion, err := client.Completions(t.Context(), openai.CompletionRequest{Model: "model", Prompt: "hello"})
	if err != nil || completion.Usage.TotalTokens != 3 {
		t.Fatalf("completion=%+v err=%v", completion, err)
	}
	response, err := client.Responses(t.Context(), openai.ResponseRequest{Model: "model", Input: "hello"})
	if err != nil || response.Usage.TotalTokens != 3 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	embedding, err := client.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "model", Input: "hello"})
	if err != nil || embedding.Usage.TotalTokens != 2 || len(embedding.Data) != 1 {
		t.Fatalf("embedding=%+v err=%v", embedding, err)
	}
	topN := 1
	reranked, err := client.Rerank(t.Context(), openai.RerankRequest{Model: "model", Query: "query", Documents: []any{"first", "second"}, TopN: &topN, Truncate: "END"})
	if err != nil || len(reranked.Results) != 1 || reranked.Results[0].Index != 1 || reranked.Results[0].RelevanceScore != 2.5 || reranked.Meta == nil || reranked.Meta.Tokens == nil || reranked.Meta.Tokens.InputTokens != 7 {
		t.Fatalf("reranked=%+v err=%v", reranked, err)
	}

	router := New(Config{CredentialEncryptionKey: []byte("nvidia-nim-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "nim", Type: "nvidia-nim", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "nim-key", ProviderID: "nim", Secret: "nim-key"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "nim", "nim-key")
	if err != nil || len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-b" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/completions", "/v1/responses", "/v1/embeddings", "/v1/ranking", "/v1/models"} {
		if seen[path] != 1 {
			t.Fatalf("path %s called %d times", path, seen[path])
		}
	}
}

func TestNVIDIANIMCapabilityProfileIsBounded(t *testing.T) {
	for _, profile := range ManagedProviderCapabilityProfiles() {
		if profile.Type != "nvidia-nim" {
			continue
		}
		expectedOperations := []string{"chat", "completions", "responses", "count_tokens", "embeddings", "rerank", "stream"}
		expectedCapabilities := []string{"chat", "completions", "responses", "embeddings", "rerank", "stream", "tools", "structured_output", "vision", "audio_input", "video_input"}
		if !slices.Equal(profile.Operations, expectedOperations) || !slices.Equal(profile.Capabilities, expectedCapabilities) {
			t.Fatalf("profile=%+v", profile)
		}
		if !slices.Equal(profile.RerankParameters.SupportedOptions, []string{"top_n", "return_documents", "truncate"}) || !slices.Equal(profile.RerankParameters.DocumentForms, []string{"text"}) {
			t.Fatalf("rerank profile=%+v", profile.RerankParameters)
		}
		if slices.Contains(profile.ChatParameters.SupportedOptions, "reasoning_effort") || len(profile.ChatParameters.ReasoningEffort) != 0 {
			t.Fatalf("provider-wide reasoning profile=%+v", profile.ChatParameters)
		}
		expectedModels := []ProviderChatModelParameterPolicy{
			{Model: "nvidia/nemotron-3-super-120b-a12b", SupportedOptions: []string{"reasoning_effort"}, ReasoningEffort: []string{"none", "low", "high"}},
			{Model: "nvidia/nemotron-3-ultra-550b-a55b", SupportedOptions: []string{"reasoning_effort"}, ReasoningEffort: []string{"none", "medium", "high"}},
		}
		if !slices.EqualFunc(profile.ChatModelParameters, expectedModels, func(left, right ProviderChatModelParameterPolicy) bool {
			return left.Model == right.Model && slices.Equal(left.SupportedOptions, right.SupportedOptions) && slices.Equal(left.ReasoningEffort, right.ReasoningEffort)
		}) {
			t.Fatalf("model profiles=%+v", profile.ChatModelParameters)
		}
		return
	}
	t.Fatal("NVIDIA NIM capability profile is missing")
}

func TestNVIDIANIMReasoningEffortPolicyAndWireContract(t *testing.T) {
	for _, test := range []struct {
		model string
		value string
		valid bool
	}{
		{"nvidia/nemotron-3-super-120b-a12b", "none", true},
		{"nvidia/nemotron-3-super-120b-a12b", "low", true},
		{"nvidia/nemotron-3-super-120b-a12b", "high", true},
		{"nvidia/nemotron-3-super-120b-a12b", "medium", false},
		{"nvidia/nemotron-3-ultra-550b-a55b", "none", true},
		{"nvidia/nemotron-3-ultra-550b-a55b", "medium", true},
		{"nvidia/nemotron-3-ultra-550b-a55b", "high", true},
		{"nvidia/nemotron-3-ultra-550b-a55b", "low", false},
		{"model", "high", false},
	} {
		err := (NVIDIANIM{}).ValidateChatParameters(openai.ChatCompletionRequest{
			Model: test.model, Messages: []openai.Message{{Role: "user", Content: "question"}},
			ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: test.value},
		})
		if (err == nil) != test.valid {
			t.Fatalf("model=%s value=%s valid=%t err=%v", test.model, test.value, test.valid, err)
		}
	}

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["reasoning_effort"] != "medium" {
			t.Fatalf("body=%#v err=%v", body, err)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"nvidia/nemotron-3-ultra-550b-a55b","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer server.Close()
	client := NewNVIDIANIM(server.URL, "key", false)
	request := openai.ChatCompletionRequest{
		Model: "nvidia/nemotron-3-ultra-550b-a55b", Messages: []openai.Message{{Role: "user", Content: "question"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "medium"},
	}
	if _, err := client.ChatCompletions(t.Context(), request); err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	request.Model = "unknown"
	if _, err := client.ChatCompletions(t.Context(), request); err == nil || calls != 1 {
		t.Fatalf("unsupported request reached upstream: calls=%d err=%v", calls, err)
	}
}

func TestNVIDIANIMNativeMessagesAndCountTokens(t *testing.T) {
	var messagesCalls, chatCalls, countCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer nim-key" || r.Header.Get("X-API-Key") != "" {
			t.Fatalf("headers=%v", r.Header)
		}
		if strings.HasPrefix(r.URL.Path, "/v1/messages") && r.Header.Get("Anthropic-Version") != "2023-06-01" {
			t.Fatalf("native headers=%v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatCalls++
			_, _ = fmt.Fprint(w, `{"id":"chat","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"compatible"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		case "/v1/messages":
			messagesCalls++
			if body["max_tokens"] == nil || body["messages"] == nil {
				t.Fatalf("native body=%#v", body)
			}
			if stream, _ := body["stream"].(bool); stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "event: message_start\n"+`data: {"type":"message_start","message":{"id":"native-stream","model":"model","usage":{"input_tokens":2}}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"native"}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
				return
			}
			_, _ = fmt.Fprint(w, `{"id":"native","type":"message","role":"assistant","model":"model","content":[{"type":"text","text":"native"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
		case "/v1/messages/count_tokens":
			countCalls++
			if _, exists := body["max_tokens"]; exists {
				t.Fatalf("count body includes generation limit: %#v", body)
			}
			_, _ = fmt.Fprint(w, `{"input_tokens":7}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "nim", Type: "nvidia-nim", BaseURL: server.URL, APIKey: "nim-key", Models: []string{"model"}, Stream: true}}}).(*Router)
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	compatible, err := router.ChatCompletions(t.Context(), modules.RequestContext{Request: request})
	if err != nil || compatible.Choices[0].Message.Content != "compatible" {
		t.Fatalf("compatible=%+v err=%v", compatible, err)
	}
	native, err := router.ChatCompletions(t.Context(), modules.RequestContext{Request: request, Metadata: map[string]string{"gateway.api_type": "messages"}})
	if err != nil || native.Choices[0].Message.Content != "native" || native.Usage.TotalTokens != 3 {
		t.Fatalf("native=%+v err=%v", native, err)
	}
	var chunks []string
	request.Stream = true
	streamedResponse, streamed, err := router.StreamChatCompletions(t.Context(), modules.RequestContext{Request: request, Metadata: map[string]string{"gateway.api_type": "messages"}}, func(payload string) error {
		chunks = append(chunks, payload)
		return nil
	})
	if err != nil || !streamed || streamedResponse.Usage.TotalTokens != 3 || !strings.Contains(strings.Join(chunks, ""), "native") {
		t.Fatalf("streamed=%v response=%+v chunks=%q err=%v", streamed, streamedResponse, chunks, err)
	}
	result, err := router.CountTokens(t.Context(), modules.RequestContext{Request: request})
	if err != nil || result.InputTokens != 7 || result.Source != "nvidia-nim" {
		t.Fatalf("count=%+v err=%v", result, err)
	}
	if chatCalls != 1 || messagesCalls != 2 || countCalls != 1 {
		t.Fatalf("chat=%d messages=%d count=%d", chatCalls, messagesCalls, countCalls)
	}
}

var _ MessagesClient = NVIDIANIM{}
var _ StreamingMessagesClient = NVIDIANIM{}
var _ TokenCountClient = NVIDIANIM{}

func TestNVIDIANIMRejectsUnsupportedServiceTierBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewNVIDIANIM(server.URL, "", false)
	_, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "priority"}})
	if err == nil || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestNVIDIANIMResponseLifecycleContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer nim-key" || r.URL.Path != "/v1/responses/resp_123" && r.URL.Path != "/v1/responses/resp_123/cancel" {
			t.Fatalf("request=%s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/responses/resp_123":
			_, _ = fmt.Fprint(w, `{"id":"resp_123","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
		case "POST /v1/responses/resp_123/cancel":
			if r.ContentLength > 0 {
				t.Fatalf("cancel content length=%d", r.ContentLength)
			}
			_, _ = fmt.Fprint(w, `{"id":"resp_123","status":"cancelled","output":[],"usage":{"input_tokens":2,"output_tokens":0,"total_tokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewNVIDIANIM(server.URL, "nim-key", false)
	retrieved, err := client.RetrieveResponse(t.Context(), "resp_123")
	if err != nil || retrieved.OutputText != "done" || retrieved.Usage.TotalTokens != 3 {
		t.Fatalf("retrieved=%+v err=%v", retrieved, err)
	}
	cancelled, err := client.CancelResponse(t.Context(), "resp_123")
	if err != nil || cancelled.Status != "cancelled" || cancelled.Usage.TotalTokens != 2 {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
}

var _ responseRetrieveClient = NVIDIANIM{}
var _ responseCancelClient = NVIDIANIM{}
var _ RerankClient = NVIDIANIM{}

func TestNVIDIANIMRerankRejectsUnsupportedInputsBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewNVIDIANIM(server.URL, "nim-key", false)
	one := 1
	tests := []openai.RerankRequest{
		{Model: "model", Query: "query", Documents: []any{map[string]any{"text": "document"}}},
		{Model: "model", Query: "query", Documents: []any{"document"}, RankFields: []string{"text"}},
		{Model: "model", Query: "query", Documents: []any{"document"}, MaxChunksPerDoc: &one},
		{Model: "model", Query: "query", Documents: []any{"document"}, MaxTokensPerDoc: &one},
		{Model: "model", Query: "query", Documents: []any{"document"}, Truncate: "MIDDLE"},
		{Model: "model", Query: "query", Documents: make([]any, nvidiaNIMMaxRerankPassages+1)},
	}
	for _, request := range tests {
		if _, err := client.Rerank(t.Context(), request); err == nil {
			t.Fatalf("unsupported request accepted: %+v", request)
		}
	}
	if called {
		t.Fatal("invalid request reached upstream")
	}
}

func TestNVIDIANIMRerankRequiresExactUsageAndRankings(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing usage", body: `{"rankings":[{"index":0,"logit":1}]}`},
		{name: "zero usage", body: `{"rankings":[{"index":0,"logit":1}],"usage":{"prompt_tokens":0,"total_tokens":0}}`},
		{name: "inconsistent usage", body: `{"rankings":[{"index":0,"logit":1}],"usage":{"prompt_tokens":2,"total_tokens":3}}`},
		{name: "missing ranking", body: `{"rankings":[],"usage":{"prompt_tokens":2,"total_tokens":2}}`},
		{name: "duplicate ranking", body: `{"rankings":[{"index":0,"logit":2},{"index":0,"logit":1}],"usage":{"prompt_tokens":2,"total_tokens":2}}`},
		{name: "unordered ranking", body: `{"rankings":[{"index":0,"logit":1},{"index":1,"logit":2}],"usage":{"prompt_tokens":2,"total_tokens":2}}`},
		{name: "trailing data", body: `{"rankings":[{"index":0,"logit":1}],"usage":{"prompt_tokens":2,"total_tokens":2}} {}`},
		{name: "oversized", body: strings.Repeat(" ", (8<<20)+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			documents := []any{"one"}
			if strings.Contains(test.name, "duplicate") || strings.Contains(test.name, "unordered") {
				documents = []any{"one", "two"}
			}
			if _, err := NewNVIDIANIM(server.URL, "nim-key", false).Rerank(t.Context(), openai.RerankRequest{Model: "model", Query: "query", Documents: documents}); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

type nvidiaNIMRerankLifecycleRecorder struct {
	settledTokens int
}

func (*nvidiaNIMRerankLifecycleRecorder) Name() string   { return "nvidia-nim-rerank-recorder" }
func (*nvidiaNIMRerankLifecycleRecorder) Required() bool { return true }
func (*nvidiaNIMRerankLifecycleRecorder) Handle(context.Context, *modules.RequestContext) error {
	return nil
}
func (*nvidiaNIMRerankLifecycleRecorder) PostResponseEnabled() bool { return true }
func (m *nvidiaNIMRerankLifecycleRecorder) HandlePostResponse(_ context.Context, request *modules.RequestContext) error {
	if request.RerankResponse != nil && request.RerankResponse.Meta != nil && request.RerankResponse.Meta.Tokens != nil {
		m.settledTokens = request.RerankResponse.Meta.Tokens.InputTokens
	}
	return nil
}

func TestRouterNVIDIANIMRerankPreservesDocumentsAndExactUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"rankings":[{"index":1,"logit":2},{"index":0,"logit":1}],"usage":{"prompt_tokens":9,"total_tokens":9}}`)
	}))
	defer server.Close()
	recorder := &nvidiaNIMRerankLifecycleRecorder{}
	router := New(Config{Modules: modules.NewPipeline([]modules.Module{recorder}), Endpoints: []config.ProviderEndpointConfig{{Name: "nim", Type: "nvidia-nim", BaseURL: server.URL, Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"rerank"}}}}).(*Router)
	returnDocuments := true
	request := openai.RerankRequest{Model: "public", Query: "query", Documents: []any{"first", "second"}, ReturnDocuments: &returnDocuments}
	response, err := router.Rerank(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, RerankRequest: &request})
	if err != nil || len(response.Results) != 2 || response.Results[0].Document != "second" || recorder.settledTokens != 9 {
		t.Fatalf("response=%+v settled=%d err=%v", response, recorder.settledTokens, err)
	}
}
