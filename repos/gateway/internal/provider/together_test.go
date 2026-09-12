package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestTogetherInferenceContracts(t *testing.T) {
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path]++
		if r.Header.Get("Authorization") != "Bearer together-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		if r.Method == http.MethodPost {
			var body map[string]any
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
		case "/v1/embeddings":
			_, _ = fmt.Fprint(w, `{"object":"list","model":"model","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
		case "/v1/rerank":
			_, _ = fmt.Fprint(w, `{"id":"rerank","results":[{"index":0,"relevance_score":0.9}],"usage":{"prompt_tokens":7,"completion_tokens":0,"total_tokens":7}}`)
		case "/v1/models":
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"model-b"},{"id":"model-a"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewTogether(server.URL, "together-key", true)
	chat, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || chat.Usage.TotalTokens != 3 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	completion, err := client.Completions(t.Context(), openai.CompletionRequest{Model: "model", Prompt: "hello"})
	if err != nil || completion.Usage.TotalTokens != 3 {
		t.Fatalf("completion=%+v err=%v", completion, err)
	}
	embedding, err := client.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "model", Input: "hello"})
	if err != nil || embedding.Usage.TotalTokens != 2 || len(embedding.Data) != 1 {
		t.Fatalf("embedding=%+v err=%v", embedding, err)
	}
	topN := 1
	reranked, err := client.Rerank(t.Context(), openai.RerankRequest{Model: "model", Query: "query", Documents: []any{"document"}, TopN: &topN})
	if err != nil || len(reranked.Results) != 1 || reranked.Meta == nil || reranked.Meta.Tokens == nil || reranked.Meta.Tokens.InputTokens != 7 {
		t.Fatalf("rerank=%+v err=%v", reranked, err)
	}
	router := New(Config{CredentialEncryptionKey: []byte("together-provider-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "together", Type: "together", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "together-key", ProviderID: "together", Secret: "together-key"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "together", "together-key")
	if err != nil || len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-b" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/rerank", "/v1/models"} {
		if seen[path] != 1 {
			t.Fatalf("path %s called %d times", path, seen[path])
		}
	}
}

func TestTogetherCapabilityProfileIsBounded(t *testing.T) {
	for _, profile := range ManagedProviderCapabilityProfiles() {
		if profile.Type != "together" {
			continue
		}
		if !slices.Equal(profile.Operations, []string{"chat", "completions", "embeddings", "rerank", "image_generation", "audio_transcription", "audio_translation", "audio_speech", "stream"}) || !slices.Equal(profile.Capabilities, []string{"chat", "completions", "embeddings", "rerank", "image_generation", "audio_transcription", "audio_translation", "audio_speech", "stream", "tools", "structured_output", "vision"}) || len(profile.AuthTypes) != 0 {
			t.Fatalf("profile=%+v", profile)
		}
		if slices.Contains(profile.ChatParameters.SupportedOptions, "store") || slices.Contains(profile.ChatParameters.SupportedOptions, "metadata") || slices.Contains(profile.ChatParameters.SupportedOptions, "service_tier") || slices.Contains(profile.ChatParameters.SupportedOptions, "prediction") || !slices.Contains(profile.ChatParameters.SupportedOptions, "logprobs") || slices.Contains(profile.ChatParameters.SupportedOptions, "top_logprobs") || !slices.Contains(profile.ChatParameters.SupportedOptions, "min_p") || !slices.Contains(profile.ChatParameters.SupportedOptions, "top_k") || !slices.Contains(profile.ChatParameters.SupportedOptions, "repetition_penalty") || !slices.Contains(profile.ChatParameters.SupportedOptions, "logit_bias") || !slices.Equal(profile.ChatParameters.Logprobs, []string{"false", "true"}) {
			t.Fatalf("ignored options advertised: %+v", profile.ChatParameters)
		}
		wantModels := []ProviderChatModelParameterPolicy{
			{Model: "openai/gpt-oss-20b", SupportedOptions: []string{"reasoning_effort"}, ReasoningEffort: []string{"low", "medium", "high"}},
			{Model: "openai/gpt-oss-120b", SupportedOptions: []string{"reasoning_effort"}, ReasoningEffort: []string{"low", "medium", "high"}},
			{Model: "deepseek-ai/DeepSeek-V4-Pro-0813", SupportedOptions: []string{"reasoning_effort"}, ReasoningEffort: []string{"high", "max"}},
		}
		if !slices.EqualFunc(profile.ChatModelParameters, wantModels, func(left, right ProviderChatModelParameterPolicy) bool {
			return left.Model == right.Model && slices.Equal(left.SupportedOptions, right.SupportedOptions) && slices.Equal(left.ReasoningEffort, right.ReasoningEffort)
		}) {
			t.Fatalf("model parameters=%+v want=%+v", profile.ChatModelParameters, wantModels)
		}
		return
	}
	t.Fatal("Together capability profile is missing")
}

func TestTogetherSamplingControlsRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MinP              *float64       `json:"min_p"`
			TopK              *int           `json:"top_k"`
			RepetitionPenalty *float64       `json:"repetition_penalty"`
			LogitBias         map[string]int `json:"logit_bias"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.MinP == nil || *body.MinP != 0.05 || body.TopK == nil || *body.TopK != 40 || body.RepetitionPenalty == nil || *body.RepetitionPenalty != 1.1 || !maps.Equal(body.LogitBias, map[string]int{"42": -10, "128": 20}) {
			t.Fatalf("body=%+v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","created":1,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer server.Close()
	minP, topK, repetitionPenalty := 0.05, 40, 1.1
	_, err := NewTogether(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{MinP: &minP, TopK: &topK, RepetitionPenalty: &repetitionPenalty, LogitBias: map[string]int{"42": -10, "128": 20}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTogetherMapsMaxCompletionTokensToNativeField(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				decodeErr := json.NewDecoder(r.Body).Decode(&body)
				gotStream, _ := body["stream"].(bool)
				if decodeErr != nil || body["max_tokens"] != float64(64) || body["max_completion_tokens"] != nil || gotStream != stream {
					t.Fatalf("body=%#v", body)
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
					return
				}
				_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","created":1,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
			}))
			defer server.Close()
			limit := 64
			client := NewTogether(server.URL, "key", true)
			request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}}, MaxCompletionTokens: &limit, Stream: stream}
			var err error
			if stream {
				_, err = client.StreamChatCompletions(t.Context(), request, func(string) error { return nil })
			} else {
				_, err = client.ChatCompletions(t.Context(), request)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTogetherNormalizesAndValidatesFinishReasons(t *testing.T) {
	for _, field := range []string{"message", "delta"} {
		t.Run(field, func(t *testing.T) {
			payload := fmt.Sprintf(`{"choices":[{"%s":{"role":"assistant","content":"answer"},"finish_reason":"eos"}]}`, field)
			normalized, err := normalizeTogetherChatPayload([]byte(payload), field)
			if err != nil || !strings.Contains(string(normalized), `"finish_reason":"stop"`) || strings.Contains(string(normalized), `"finish_reason":"eos"`) {
				t.Fatalf("normalized=%s err=%v", normalized, err)
			}
			for _, reason := range []string{"stop", "length", "tool_calls", "function_call"} {
				payload = fmt.Sprintf(`{"choices":[{"%s":{"role":"assistant","content":"answer"},"finish_reason":%q}]}`, field, reason)
				if _, err := normalizeTogetherChatPayload([]byte(payload), field); err != nil {
					t.Fatalf("reason=%s err=%v", reason, err)
				}
			}
			payload = fmt.Sprintf(`{"choices":[{"%s":{"role":"assistant","content":"answer"},"finish_reason":"unknown"}]}`, field)
			if _, err := normalizeTogetherChatPayload([]byte(payload), field); err == nil {
				t.Fatal("unknown finish reason accepted")
			}
		})
	}
	if _, err := normalizeTogetherChatPayload([]byte(`{"choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":null}]}`), "message"); err == nil {
		t.Fatal("missing JSON finish reason accepted")
	}
	if _, err := normalizeTogetherChatPayload([]byte(`{"choices":[{"delta":{"role":"assistant","content":"answer"},"finish_reason":null}]}`), "delta"); err != nil {
		t.Fatalf("pending stream finish reason rejected: %v", err)
	}
}

func TestTogetherStreamNormalizesEOSBeforeWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":\"eos\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	var chunks []string
	response, err := NewTogether(server.URL, "key", true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}}, Stream: true}, func(payload string) error {
		chunks = append(chunks, payload)
		return nil
	})
	if err != nil || response.Choices[0].FinishReason != "stop" || len(chunks) != 1 || !strings.Contains(chunks[0], `"finish_reason":"stop"`) {
		t.Fatalf("response=%+v chunks=%v err=%v", response, chunks, err)
	}
}

func TestTogetherRejectsInvalidSamplingControlsBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	invalidMinP, invalidTopK, invalidRepetition := 1.1, -1, 0.0
	tests := []openai.ChatGenerationOptions{
		{MinP: &invalidMinP},
		{TopK: &invalidTopK},
		{RepetitionPenalty: &invalidRepetition},
		{LogitBias: map[string]int{"invalid": 1}},
		{LogitBias: map[string]int{"42": 101}},
	}
	for _, options := range tests {
		_, err := NewTogether(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}}, ChatGenerationOptions: options})
		if err == nil {
			t.Fatalf("invalid options accepted: %+v", options)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached upstream %d times", calls)
	}
}

func TestTogetherLogprobsJSONContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["logprobs"] != float64(0) || body["top_logprobs"] != nil {
			t.Fatalf("body=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","created":1,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop","logprobs":{"token_ids":[42],"tokens":["answer"],"token_logprobs":[-0.25],"top_logprobs":{}}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer server.Close()
	enabled := true
	response, err := NewTogether(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &enabled}})
	if err != nil || response.Choices[0].Logprobs == nil || len(response.Choices[0].Logprobs.Content) != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	item := response.Choices[0].Logprobs.Content[0]
	if item.Token != "answer" || item.Logprob != -0.25 || !slices.Equal(item.Bytes, []int{97, 110, 115, 119, 101, 114}) || len(item.TopLogprobs) != 0 {
		t.Fatalf("logprob=%+v", item)
	}
}

func TestTogetherLogprobsSSEContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["logprobs"] != float64(0) || body["top_logprobs"] != nil {
			t.Fatalf("body=%#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"an\",\"token_id\":10},\"logprobs\":-0.1,\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"swer\",\"token_id\":11},\"logprobs\":-0.2,\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":2,\"total_tokens\":4}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	enabled := true
	chunks := []string{}
	response, err := NewTogether(server.URL, "key", true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}}, Stream: true, ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &enabled}}, func(payload string) error {
		chunks = append(chunks, payload)
		return nil
	})
	if err != nil || openai.ContentText(response.Choices[0].Message.Content) != "answer" || response.Choices[0].Logprobs == nil || len(response.Choices[0].Logprobs.Content) != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	for _, payload := range chunks {
		if strings.Contains(payload, `"logprobs":-`) || !strings.Contains(payload, `"logprobs":{"content"`) {
			t.Fatalf("native logprobs were not normalized: %s", payload)
		}
	}
}

func TestTogetherLogprobsFalseIsOmitted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["logprobs"] != nil || body["top_logprobs"] != nil {
			t.Fatalf("body=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","created":1,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer server.Close()
	disabled := false
	if _, err := NewTogether(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &disabled}}); err != nil {
		t.Fatal(err)
	}
}

func TestTogetherLogprobsRejectUnsupportedOrMalformedContracts(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	enabled := true
	top := 1
	if _, err := NewTogether(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &enabled, TopLogprobs: &top}}); err == nil || calls != 0 {
		t.Fatalf("top_logprobs accepted or reached upstream: err=%v calls=%d", err, calls)
	}
	for name, payload := range map[string]string{
		"different lengths": `{"choices":[{"message":{"content":"a"},"logprobs":{"tokens":["a"],"token_logprobs":[]}}]}`,
		"positive":          `{"choices":[{"message":{"content":"a"},"logprobs":{"tokens":["a"],"token_logprobs":[0.1]}}]}`,
		"invalid id":        `{"choices":[{"message":{"content":"a"},"logprobs":{"token_ids":[-1],"tokens":["a"],"token_logprobs":[-0.1]}}]}`,
		"ambiguous top":     `{"choices":[{"message":{"content":"a"},"logprobs":{"tokens":["a"],"token_logprobs":[-0.1],"top_logprobs":{"b":-1}}}]}`,
		"stream no token":   `{"choices":[{"delta":{"role":"assistant"},"logprobs":-0.1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			field := "message"
			if name == "stream no token" {
				field = "delta"
			}
			if _, err := normalizeTogetherChatPayload([]byte(payload), field); err == nil {
				t.Fatal("malformed logprobs accepted")
			}
		})
	}
}

func TestTogetherRequestedLogprobsCannotDisappear(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","created":1,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer server.Close()
	enabled := true
	if _, err := NewTogether(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &enabled}}); err == nil {
		t.Fatal("missing requested logprobs accepted")
	}
}

func TestTogetherRequestedStreamLogprobsFailBeforeMissingChunkIsWritten(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\",\"token_id\":1},\"logprobs\":-0.1,\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" missing\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":2,\"total_tokens\":4}}\n\n")
	}))
	defer server.Close()
	enabled := true
	writes := 0
	_, err := NewTogether(server.URL, "key", true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}}, Stream: true, ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &enabled}}, func(string) error {
		writes++
		return nil
	})
	if err == nil || writes != 1 {
		t.Fatalf("err=%v writes=%d", err, writes)
	}
}

func TestTogetherReasoningEffortAndReasoningAliasJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		messages, _ := body["messages"].([]any)
		prior, _ := messages[1].(map[string]any)
		if body["reasoning_effort"] != "medium" || prior["reasoning"] != "prior plan" || prior["reasoning_content"] != nil {
			t.Fatalf("body=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","created":1,"model":"openai/gpt-oss-20b","choices":[{"index":0,"message":{"role":"assistant","reasoning":"new plan","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8,"completion_tokens_details":{"reasoning_tokens":2}}}`)
	}))
	defer server.Close()

	response, err := NewTogether(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model:                 "openai/gpt-oss-20b",
		Messages:              []openai.Message{{Role: "user", Content: "question"}, {Role: "assistant", Content: "prior answer", ReasoningContent: "prior plan"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "medium"},
	})
	if err != nil || response.Choices[0].Message.ReasoningContent != "new plan" || response.Usage.CompletionTokensDetails == nil || response.Usage.CompletionTokensDetails.ReasoningTokens != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestTogetherReasoningAliasSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"openai/gpt-oss-120b\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning\":\"plan \"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"openai/gpt-oss-120b\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"done\",\"content\":\"answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	chunks := []string{}
	request := openai.ChatCompletionRequest{Model: "openai/gpt-oss-120b", Messages: []openai.Message{{Role: "user", Content: "question"}}, Stream: true, ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "high"}}
	response, err := NewTogether(server.URL, "key", true).StreamChatCompletions(t.Context(), request, func(payload string) error {
		chunks = append(chunks, payload)
		return nil
	})
	if err != nil || response.Choices[0].Message.ReasoningContent != "plan done" || openai.ContentText(response.Choices[0].Message.Content) != "answer" || response.Usage.TotalTokens != 8 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	for _, payload := range chunks {
		if strings.Contains(payload, `"reasoning":`) || !strings.Contains(payload, `"reasoning_content":`) {
			t.Fatalf("reasoning alias was not normalized: %s", payload)
		}
	}
}

func TestTogetherReasoningEffortRejectsUnsupportedModelOrValueBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	client := NewTogether(server.URL, "key", false)
	for _, request := range []openai.ChatCompletionRequest{
		{Model: "other", Messages: []openai.Message{{Role: "user", Content: "question"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "medium"}},
		{Model: "openai/gpt-oss-20b", Messages: []openai.Message{{Role: "user", Content: "question"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "xhigh"}},
		{Model: "deepseek-ai/DeepSeek-V4-Pro-0813", Messages: []openai.Message{{Role: "user", Content: "question"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "medium"}},
	} {
		if _, err := client.ChatCompletions(t.Context(), request); err == nil {
			t.Fatalf("accepted request=%+v", request)
		}
	}
	if err := client.ValidateChatParameters(openai.ChatCompletionRequest{Model: "deepseek-ai/DeepSeek-V4-Pro-0813", Messages: []openai.Message{{Role: "user", Content: "question"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "max"}}); err != nil {
		t.Fatalf("documented DeepSeek value rejected: %v", err)
	}
	if calls != 0 {
		t.Fatalf("upstream calls=%d", calls)
	}
}

func TestTogetherReasoningAliasRejectsInvalidResponses(t *testing.T) {
	for name, payload := range map[string]string{
		"wrong type":   `{"choices":[{"message":{"reasoning":{"text":"plan"}}}]}`,
		"conflict":     `{"choices":[{"message":{"reasoning":"plan","reasoning_content":"other"}}]}`,
		"over maximum": `{"choices":[{"message":{"reasoning":"` + strings.Repeat("x", openai.MaxChatReasoningContentBytes+1) + `"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var response openai.ChatCompletionResponse
			if err := decodeTogetherChatCompletionResponse(strings.NewReader(payload), &response); err == nil {
				t.Fatal("invalid reasoning accepted")
			}
		})
	}
}

func TestTogetherImageGenerationContract(t *testing.T) {
	n := 2
	seed := int64(42)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != "Bearer together-key" {
			t.Fatalf("method=%s path=%s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "image-model" || body["prompt"] != "draw" || body["n"] != float64(2) || body["width"] != float64(1536) || body["height"] != float64(1024) || body["response_format"] != "base64" || body["output_format"] != "png" || body["seed"] != float64(42) {
			t.Fatalf("body=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"image-id","model":"image-model","object":"list","data":[{"index":0,"b64_json":"b25l"},{"index":1,"b64_json":"dHdv"}]}`)
	}))
	defer server.Close()

	request := openai.ImageGenerationRequest{Model: "image-model", Prompt: "draw", N: &n, Size: "1536x1024", ResponseFormat: "b64_json", OutputFormat: "png", Seed: &seed}
	response, err := NewTogether(server.URL, "together-key", false).GenerateImage(t.Context(), request)
	if err != nil || len(response.Data) != 2 || response.Data[0].B64JSON != "b25l" || response.Usage != nil {
		t.Fatalf("response=%+v err=%v", response, err)
	}

	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "together-images", Type: "together", BaseURL: server.URL, APIKey: "together-key", Models: []string{"image-model"}, Capabilities: []string{"image_generation"}}}}).(*Router)
	routed, err := router.GenerateImage(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, ImageGenerationRequest: &request})
	if err != nil || len(routed.Data) != 2 || routed.Usage != nil {
		t.Fatalf("routed=%+v err=%v", routed, err)
	}
}

func TestTogetherImageGenerationRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	five := 5
	compression := 50
	partial := 1
	for _, request := range []openai.ImageGenerationRequest{
		{Model: "image", Prompt: "draw", N: &five},
		{Model: "image", Prompt: "draw", Quality: "high"},
		{Model: "image", Prompt: "draw", Style: "vivid"},
		{Model: "image", Prompt: "draw", User: "user"},
		{Model: "image", Prompt: "draw", Background: "opaque"},
		{Model: "image", Prompt: "draw", OutputFormat: "webp"},
		{Model: "image", Prompt: "draw", OutputCompression: &compression},
		{Model: "image", Prompt: "draw", Resolution: "2K"},
		{Model: "image", Prompt: "draw", AspectRatio: "1:1"},
		{Model: "image", Prompt: "draw", Stream: true},
		{Model: "image", Prompt: "draw", Stream: true, PartialImages: &partial},
	} {
		if _, err := NewTogether(server.URL, "key", false).GenerateImage(t.Context(), request); err == nil {
			t.Fatalf("accepted request=%+v", request)
		}
	}
	if calls != 0 {
		t.Fatalf("upstream calls=%d", calls)
	}
}

func TestTogetherImageGenerationRejectsMalformedResponses(t *testing.T) {
	n := 2
	request := openai.ImageGenerationRequest{Model: "image", Prompt: "draw", N: &n}
	for name, body := range map[string]string{
		"missing id":       `{"model":"image","object":"list","data":[{"index":0,"url":"https://example.test/1"},{"index":1,"url":"https://example.test/2"}]}`,
		"wrong model":      `{"id":"id","model":"other","object":"list","data":[{"index":0,"url":"https://example.test/1"},{"index":1,"url":"https://example.test/2"}]}`,
		"wrong object":     `{"id":"id","model":"image","object":"image","data":[{"index":0,"url":"https://example.test/1"},{"index":1,"url":"https://example.test/2"}]}`,
		"wrong index":      `{"id":"id","model":"image","object":"list","data":[{"index":1,"url":"https://example.test/1"},{"index":0,"url":"https://example.test/2"}]}`,
		"wrong count":      `{"id":"id","model":"image","object":"list","data":[{"index":0,"url":"https://example.test/1"}]}`,
		"ambiguous data":   `{"id":"id","model":"image","object":"list","data":[{"index":0,"url":"https://example.test/1","b64_json":"b25l"},{"index":1,"url":"https://example.test/2"}]}`,
		"invalid base64":   `{"id":"id","model":"image","object":"list","data":[{"index":0,"b64_json":"%%%"},{"index":1,"b64_json":"dHdv"}]}`,
		"trailing payload": `{"id":"id","model":"image","object":"list","data":[{"index":0,"url":"https://example.test/1"},{"index":1,"url":"https://example.test/2"}]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeTogetherImageResponse(strings.NewReader(body), request); err == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
}

func TestTogetherAudioTranscriptionContract(t *testing.T) {
	attachment := mistralWAVAttachment(12500)
	temperature := 0.25
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer together-key" {
			t.Fatalf("method=%s path=%s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := r.ParseMultipartForm(openai.MaxAudioBytes + 1); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if header.Filename != attachment.Filename || len(data) == 0 || r.FormValue("model") != "speech" || r.FormValue("language") != "en" || r.FormValue("response_format") != "verbose_json" || r.FormValue("temperature") != "0.25" || !slices.Equal(r.MultipartForm.Value["timestamp_granularities[]"], []string{"word", "segment"}) {
			t.Fatalf("form=%#v file=%q bytes=%d", r.MultipartForm.Value, header.Filename, len(data))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"text":"hello world","words":[{"word":"hello","start":0,"end":0.5}],"segments":[{"id":0,"text":"hello world","start":0,"end":1.25}]}`)
	}))
	defer server.Close()

	response, err := NewTogether(server.URL, "together-key", false).TranscribeAudio(t.Context(), openai.AudioTranscriptionRequest{
		Model: "speech", File: attachment, Language: "en", ResponseFormat: "verbose_json", Temperature: &temperature, TimestampGranularities: []string{"word", "segment"},
	})
	if err != nil || response.Text != "hello world" || response.Duration != 12.5 || response.Usage == nil || response.Usage.Type != "duration" || response.Usage.InputAudioMilliseconds != 12500 || len(response.Words) != 1 || len(response.Segments) != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestRouterTogetherTranscriptionReservesAndSettlesExactDuration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"text":"hello"}`)
	}))
	defer server.Close()
	recorder := &transcriptionLifecycleRecorder{}
	router := New(Config{Modules: modules.NewPipeline([]modules.Module{recorder}), Endpoints: []config.ProviderEndpointConfig{{
		Name: "speech", Type: "together", BaseURL: server.URL, Models: []string{"audio-model"}, Capabilities: []string{"audio_transcription"},
	}}}).(*Router)
	request := openai.AudioTranscriptionRequest{Model: "audio-model", File: mistralWAVAttachment(1250)}

	response, err := router.TranscribeAudio(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, AudioTranscriptionRequest: &request})
	if err != nil || response.Text != "hello" || recorder.reserved != 1250 || recorder.settled != 1250 || recorder.settledTokens != 0 {
		t.Fatalf("response=%+v recorder=%+v err=%v", response, recorder, err)
	}
}

func TestTogetherAudioTranslationContract(t *testing.T) {
	attachment := mistralWAVAttachment(2500)
	temperature := 0.5
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/translations" || r.Header.Get("Authorization") != "Bearer together-key" {
			t.Fatalf("method=%s path=%s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := r.ParseMultipartForm(openai.MaxAudioBytes + 1); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("model") != "speech" || r.FormValue("prompt") != "business terms" || r.FormValue("response_format") != "json" || r.FormValue("temperature") != "0.5" || r.FormValue("language") != "" || len(r.MultipartForm.Value["timestamp_granularities[]"]) != 0 {
			t.Fatalf("form=%#v", r.MultipartForm.Value)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
		_, _ = io.WriteString(w, `{"text":"translated"}`)
	}))
	defer server.Close()

	response, err := NewTogether(server.URL, "together-key", false).TranslateAudio(t.Context(), openai.AudioTranscriptionRequest{
		Model: "speech", File: attachment, Prompt: "business terms", ResponseFormat: "json", Temperature: &temperature,
	})
	if err != nil || response.Text != "translated" || response.Duration != 2.5 || response.Usage == nil || response.Usage.Type != "duration" || response.Usage.InputAudioMilliseconds != 2500 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestRouterTogetherTranslationReservesAndSettlesExactDuration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"text":"translated"}`)
	}))
	defer server.Close()
	recorder := &transcriptionLifecycleRecorder{}
	router := New(Config{Modules: modules.NewPipeline([]modules.Module{recorder}), Endpoints: []config.ProviderEndpointConfig{{
		Name: "translation", Type: "together", BaseURL: server.URL, Models: []string{"audio-model"}, Capabilities: []string{"audio_translation"},
	}}}).(*Router)
	request := openai.AudioTranscriptionRequest{Model: "audio-model", File: mistralWAVAttachment(1750)}

	response, err := router.TranslateAudio(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, AudioTranscriptionRequest: &request, Metadata: map[string]string{"gateway.api_type": "audio_translation"}})
	if err != nil || response.Text != "translated" || recorder.reserved != 1750 || recorder.settled != 1750 || recorder.settledTokens != 0 {
		t.Fatalf("response=%+v recorder=%+v err=%v", response, recorder, err)
	}
}

func TestTogetherAudioTranslationRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	attachment := mistralWAVAttachment(1000)
	tests := []struct {
		param string
		apply func(*openai.AudioTranscriptionRequest)
	}{
		{param: "language", apply: func(r *openai.AudioTranscriptionRequest) { r.Language = "en" }},
		{param: "timestamp_granularities", apply: func(r *openai.AudioTranscriptionRequest) {
			r.ResponseFormat = "verbose_json"
			r.TimestampGranularities = []string{"word"}
		}},
		{param: "response_format", apply: func(r *openai.AudioTranscriptionRequest) { r.ResponseFormat = "diarized_json" }},
		{param: "include", apply: func(r *openai.AudioTranscriptionRequest) { r.Include = []string{"logprobs"} }},
		{param: "languages", apply: func(r *openai.AudioTranscriptionRequest) { r.Languages = []string{"en"} }},
		{param: "keywords", apply: func(r *openai.AudioTranscriptionRequest) { r.Keywords = []string{"term"} }},
		{param: "mode", apply: func(r *openai.AudioTranscriptionRequest) { r.Mode = "SMART" }},
		{param: "chunking_strategy", apply: func(r *openai.AudioTranscriptionRequest) {
			r.ChunkingStrategy = &openai.AudioChunkingStrategy{Type: "auto"}
		}},
		{param: "stream", apply: func(r *openai.AudioTranscriptionRequest) { r.Stream = true }},
	}
	for _, test := range tests {
		t.Run(test.param, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer server.Close()
			request := openai.AudioTranscriptionRequest{Model: "speech", File: attachment}
			test.apply(&request)

			_, err := NewTogether(server.URL, "key", false).TranslateAudio(t.Context(), request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Param != test.param || failure.UpstreamCode != "unsupported_parameter" || called {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}

func TestTogetherAudioTranscriptionRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	attachment := mistralWAVAttachment(1000)
	tests := []struct {
		param string
		apply func(*openai.AudioTranscriptionRequest)
	}{
		{param: "language", apply: func(r *openai.AudioTranscriptionRequest) { r.Language = "en-US" }},
		{param: "prompt", apply: func(r *openai.AudioTranscriptionRequest) { r.Prompt = "terms" }},
		{param: "response_format", apply: func(r *openai.AudioTranscriptionRequest) { r.ResponseFormat = "diarized_json" }},
		{param: "include", apply: func(r *openai.AudioTranscriptionRequest) { r.Include = []string{"logprobs"} }},
		{param: "languages", apply: func(r *openai.AudioTranscriptionRequest) { r.Languages = []string{"en"} }},
		{param: "keywords", apply: func(r *openai.AudioTranscriptionRequest) { r.Keywords = []string{"term"} }},
		{param: "mode", apply: func(r *openai.AudioTranscriptionRequest) { r.Mode = "SMART" }},
		{param: "chunking_strategy", apply: func(r *openai.AudioTranscriptionRequest) {
			r.ChunkingStrategy = &openai.AudioChunkingStrategy{Type: "auto"}
		}},
		{param: "stream", apply: func(r *openai.AudioTranscriptionRequest) { r.Stream = true }},
	}
	for _, test := range tests {
		t.Run(test.param, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer server.Close()
			request := openai.AudioTranscriptionRequest{Model: "speech", File: attachment}
			test.apply(&request)

			_, err := NewTogether(server.URL, "key", false).TranscribeAudio(t.Context(), request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Param != test.param || failure.UpstreamCode != "unsupported_parameter" || called {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}

func TestTogetherAudioTranscriptionRequiresReliableDuration(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	request := openai.AudioTranscriptionRequest{Model: "speech", File: openai.AudioAttachment{Filename: "audio.aac", MediaType: "audio/aac", Data: "//FQgAAf/A=="}}

	_, err := NewTogether(server.URL, "key", false).TranscribeAudio(t.Context(), request)
	var failure *Error
	if !errors.As(err, &failure) || failure.Param != "file" || failure.UpstreamCode != "unsupported_audio" || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
	if _, err := togetherBoundedAudioDuration(togetherMaxAudioMilliseconds+1, nil); err == nil {
		t.Fatal("audio longer than four hours accepted")
	}
}

func TestTogetherAudioTranscriptionRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "null", body: `null`},
		{name: "missing text", body: `{}`},
		{name: "trailing", body: `{"text":"hello"} {}`},
		{name: "oversized", body: strings.Repeat(" ", maxAudioTranscriptionResponseBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeTogetherAudioResponse(strings.NewReader(test.body), 1000); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestTogetherAudioSpeechContract(t *testing.T) {
	tests := []struct {
		name          string
		request       openai.AudioSpeechRequest
		wantFormat    string
		wantMediaType string
	}{
		{name: "default mp3", request: openai.AudioSpeechRequest{Model: "speech", Input: "hello", Voice: "voice"}, wantFormat: "mp3", wantMediaType: "audio/mpeg"},
		{name: "raw pcm with language", request: openai.AudioSpeechRequest{Model: "speech", Input: "hello", Voice: "voice", Language: "en-us", ResponseFormat: "pcm", StreamFormat: "audio"}, wantFormat: "raw", wantMediaType: "application/octet-stream"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/speech" || r.Header.Get("Authorization") != "Bearer together-key" {
					t.Fatalf("method=%s path=%s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
				}
				var body struct {
					Model          string `json:"model"`
					Input          string `json:"input"`
					Voice          string `json:"voice"`
					Language       string `json:"language"`
					ResponseFormat string `json:"response_format"`
					Stream         bool   `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Model != test.request.Model || body.Input != test.request.Input || body.Voice != test.request.Voice || body.Language != test.request.Language || body.ResponseFormat != test.wantFormat || body.Stream {
					t.Fatalf("body=%+v", body)
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write([]byte("audio"))
			}))
			defer server.Close()

			response, err := NewTogether(server.URL, "together-key", false).GenerateSpeech(t.Context(), test.request)
			if err != nil || string(response.Data) != "audio" || response.Model != test.request.Model || response.ContentType != test.wantMediaType {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
}

func TestTogetherAudioSpeechRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	speed := 1.0
	tests := []struct {
		param string
		apply func(*openai.AudioSpeechRequest)
	}{
		{param: "language", apply: func(r *openai.AudioSpeechRequest) { r.Language = "auto" }},
		{param: "language", apply: func(r *openai.AudioSpeechRequest) { r.Language = "en-US" }},
		{param: "instructions", apply: func(r *openai.AudioSpeechRequest) { r.Instructions = "warmly" }},
		{param: "response_format", apply: func(r *openai.AudioSpeechRequest) { r.ResponseFormat = "opus" }},
		{param: "speed", apply: func(r *openai.AudioSpeechRequest) { r.Speed = &speed }},
		{param: "stream_format", apply: func(r *openai.AudioSpeechRequest) { r.StreamFormat = "sse" }},
	}
	for _, test := range tests {
		t.Run(test.param, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer server.Close()
			request := openai.AudioSpeechRequest{Model: "speech", Input: "hello", Voice: "voice"}
			test.apply(&request)

			_, err := NewTogether(server.URL, "key", false).GenerateSpeech(t.Context(), request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Param != test.param || failure.UpstreamCode != "unsupported_parameter" || called {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}

func TestTogetherRejectsIgnoredParameterBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	store := true
	_, err := NewTogether(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Store: &store}})
	if err == nil || !strings.Contains(err.Error(), "store") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestTogetherRejectsUnsupportedRerankParameterBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()

	_, err := NewTogether(server.URL, "key", false).Rerank(t.Context(), openai.RerankRequest{
		Model:      "model",
		Query:      "query",
		Documents:  []any{"document"},
		RankFields: []string{"title"},
	})
	if err == nil || !strings.Contains(err.Error(), "rank_fields") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestTogetherRerankRequiresExactUsage(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing", body: `{"id":"rerank","results":[]}`},
		{name: "negative", body: `{"id":"rerank","results":[],"usage":{"prompt_tokens":-1,"completion_tokens":0,"total_tokens":-1}}`},
		{name: "inconsistent total", body: `{"id":"rerank","results":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":2}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()

			_, err := NewTogether(server.URL, "key", false).Rerank(t.Context(), openai.RerankRequest{Model: "model", Query: "query", Documents: []any{"document"}})
			if err == nil || !strings.Contains(err.Error(), "usage") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestTogetherRerankAcceptsObjectDocuments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Documents []map[string]any `json:"documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Documents) != 1 || body.Documents[0]["title"] != "document" {
			t.Fatalf("documents=%#v", body.Documents)
		}
		_, _ = fmt.Fprint(w, `{"id":"rerank","results":[{"index":0,"relevance_score":0.9,"document":{"title":"document"}}],"usage":{"prompt_tokens":4,"completion_tokens":0,"total_tokens":4}}`)
	}))
	defer server.Close()

	response, err := NewTogether(server.URL, "key", false).Rerank(t.Context(), openai.RerankRequest{
		Model:     "model",
		Query:     "query",
		Documents: []any{map[string]any{"title": "document"}},
	})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document == nil {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestTogetherRerankRejectsTrailingAndOversizedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "trailing", body: `{"id":"rerank","results":[],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}} {}`},
		{name: "oversized", body: strings.Repeat(" ", (8<<20)+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()

			_, err := NewTogether(server.URL, "key", false).Rerank(t.Context(), openai.RerankRequest{Model: "model", Query: "query", Documents: []any{"document"}})
			if err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}
