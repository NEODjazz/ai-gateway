package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

const lemonadeChatResponse = `{"id":"chat-1","object":"chat.completion","model":"native","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
const lemonadeResponse = `{"id":"resp-1","object":"response","model":"native","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`

func TestLemonadeTextWireContract(t *testing.T) {
	for _, path := range []string{"", "/v1", "/proxy", "/proxy/v1"} {
		t.Run(path, func(t *testing.T) {
			prefix := strings.TrimSuffix(path, "/v1") + "/v1"
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("incorrect method or authentication")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "invalid JSON", http.StatusBadRequest)
					return
				}
				if body["provider"] != nil || body["model"] != "native" {
					t.Error("gateway provider or public model leaked into native request")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case prefix + "/chat/completions":
					if body["repeat_penalty"] != 1.2 || body["repetition_penalty"] != nil || body["max_completion_tokens"] != float64(64) {
						t.Errorf("incorrect parameter translation: %v", body)
					}
					fmt.Fprint(w, lemonadeChatResponse)
				case prefix + "/responses":
					if body["store"] != false || body["max_output_tokens"] != float64(64) {
						t.Error("stateless directive or output limit omitted")
					}
					fmt.Fprint(w, lemonadeResponse)
				case prefix + "/completions":
					fmt.Fprint(w, `{"id":"completion-1","object":"text_completion","model":"native","choices":[{"index":0,"text":"answer","finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
				case prefix + "/embeddings":
					fmt.Fprint(w, `{"object":"list","model":"native","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`)
				case prefix + "/rerank":
					fmt.Fprint(w, `{"results":[{"index":0,"relevance_score":0.9}],"usage":{"prompt_tokens":7,"total_tokens":7}}`)
				default:
					t.Errorf("unexpected native path %q", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			client := NewLemonade(server.URL+path+"/", "test-token", true)
			limit, penalty, store := 64, 1.2, false
			chat, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Provider: "gateway", Model: "native", Messages: []openai.Message{{Role: "user", Content: "hello"}}, MaxCompletionTokens: &limit, ChatGenerationOptions: openai.ChatGenerationOptions{RepetitionPenalty: &penalty}})
			if err != nil || chat.Choices[0].Message.Content != "answer" || !chat.UsageReported || chat.Usage.TotalTokens != 5 {
				t.Fatalf("chat=%+v err=%v", chat, err)
			}
			response, err := client.Responses(t.Context(), openai.ResponseRequest{Model: "native", Input: "hello", Store: &store, MaxTokens: &limit})
			if err != nil || responseText(response) != "answer" || !response.InputTokensReported || !response.OutputTokensReported || response.Usage.TotalTokens != 5 {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			completion, err := client.Completions(t.Context(), openai.CompletionRequest{Model: "native", Prompt: "hello"})
			if err != nil || completion.Choices[0].Text != "answer" || !completion.UsageReported {
				t.Fatalf("completion=%+v err=%v", completion, err)
			}
			embedding, err := client.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "native", Input: "hello"})
			if err != nil || !embedding.UsageReported || len(embedding.Data) != 1 || !reflect.DeepEqual(embedding.Data[0].Embedding, []float64{0.1, 0.2}) {
				t.Fatalf("embedding=%+v err=%v", embedding, err)
			}
			rerank, err := client.Rerank(t.Context(), openai.RerankRequest{Model: "native", Query: "hello", Documents: []any{"hello"}})
			if err != nil || len(rerank.Results) != 1 || rerank.Results[0].RelevanceScore != 0.9 || rerank.Meta == nil || rerank.Meta.Tokens.InputTokens != 7 {
				t.Fatalf("rerank=%+v err=%v", rerank, err)
			}
			if calls.Load() != 5 {
				t.Fatalf("native calls=%d", calls.Load())
			}
		})
	}
}

func TestLemonadeStreamingTextContracts(t *testing.T) {
	for _, operation := range []string{"chat/completions", "responses", "completions"} {
		t.Run(operation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["stream"] != true || r.URL.Path != "/v1/"+operation {
					t.Error("incorrect streaming request")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch operation {
				case "chat/completions":
					fmt.Fprint(w, "data: {\"id\":\"chat-1\",\"model\":\"native\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"}}]}\n\n")
					fmt.Fprint(w, "data: {\"id\":\"chat-1\",\"model\":\"native\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
				case "responses":
					fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"status\":\"in_progress\",\"output\":[]}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n")
					fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", lemonadeResponse)
				case "completions":
					fmt.Fprint(w, "data: {\"id\":\"completion-1\",\"object\":\"text_completion\",\"created\":1,\"model\":\"native\",\"choices\":[{\"index\":0,\"text\":\"answer\",\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
				}
			}))
			t.Cleanup(server.Close)
			client := NewLemonade(server.URL, "", true)
			events := 0
			switch operation {
			case "chat/completions":
				response, err := client.StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "native", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Stream: true}, func(string) error { events++; return nil })
				if err != nil || !response.UsageReported || response.Usage.TotalTokens != 5 || response.Choices[0].Message.Content != "answer" {
					t.Fatalf("streaming chat=%+v err=%v", response, err)
				}
			case "responses":
				response, err := client.StreamResponses(t.Context(), openai.ResponseRequest{Model: "native", Input: "hello", Stream: true}, func(string, string) error { events++; return nil })
				if err != nil || !response.InputTokensReported || !response.OutputTokensReported || responseText(response) != "answer" {
					t.Fatalf("streaming responses=%+v err=%v", response, err)
				}
			case "completions":
				response, err := client.StreamCompletions(t.Context(), openai.CompletionRequest{Model: "native", Prompt: "hello", Stream: true}, func(string) error { events++; return nil })
				if err != nil || !response.UsageReported || response.Choices[0].Text != "answer" {
					t.Fatalf("streaming completion=%+v err=%v", response, err)
				}
			}
			if events == 0 {
				t.Fatal("stream callback was never called")
			}
		})
	}
}

func TestLemonadeDiscoveryAndManagedDeployment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxy/v1/models" {
			if r.URL.RawQuery != "" {
				t.Error("discovery requested unavailable models")
			}
			fmt.Fprint(w, `{"data":[{"id":"native","recipe":"llamacpp","labels":["chat","tool-calling"],"downloaded":true}]}`)
			return
		}
		if r.URL.Path != "/proxy/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "native" {
			t.Error("upstream alias was not applied")
		}
		fmt.Fprint(w, lemonadeChatResponse)
	}))
	t.Cleanup(server.Close)
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "local-lemonade", Type: "lemonade", BaseURL: server.URL + "/proxy/v1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	probe, err := router.TestProvider(t.Context(), "local-lemonade", "")
	if err != nil || probe.Status != "available" || probe.ModelCount != 1 {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "local-lemonade", "")
	if err != nil || len(models) != 1 || !containsString(models[0].Capabilities, "tools") {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	deployment := ModelDeployment{ID: "deployment", ProviderID: "local-lemonade", Models: []string{"public"}, UpstreamModel: "native", Capabilities: models[0].Capabilities, DocumentProcessing: "native", Enabled: true}
	if err := router.validateDeployment(deployment); err != nil {
		t.Fatalf("deployment validation: %v, capabilities=%v", err, deployment.Capabilities)
	}
	if _, err := router.endpointForDeployment(deployment); err != nil {
		t.Fatalf("deployment adapter: %v", err)
	}
	if _, err := router.CreateModelDeployment(deployment); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	response, err := router.ChatCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Provider: "deployment", Model: "public", Messages: []openai.Message{{Role: "user", Content: "hello"}}}})
	if err != nil || response.Choices[0].Message.Content != "answer" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if _, ok := providerFor(config.ProviderEndpointConfig{Type: "lemonade"}).(Lemonade); !ok {
		t.Fatal("static provider factory omitted Lemonade")
	}
}

func TestLemonadeUpstreamErrorsAndCanceledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"busy","type":"rate_limit_error"}}`)
	}))
	t.Cleanup(server.Close)
	client := NewLemonade(server.URL, "", true)
	_, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "native", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	var providerError *Error
	if !errors.As(err, &providerError) || providerError.Provider != "lemonade" || providerError.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("upstream error attribution lost: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.ChatCompletions(ctx, openai.ChatCompletionRequest{Model: "native", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
