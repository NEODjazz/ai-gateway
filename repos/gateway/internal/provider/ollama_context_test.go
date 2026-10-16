package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/openai"
)

func TestOllamaDeploymentContextLifecycle(t *testing.T) {
	store := &memoryControlPlaneStore{}
	runtime, err := NewWithError(Config{ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	r := runtime.(*Router)
	if _, err := r.CreateProvider(ManagedProvider{ID: "local", Type: "ollama", BaseURL: "http://localhost:11434", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	deployment := ModelDeployment{ID: "qwen", ProviderID: "local", Models: []string{"qwen"}, Capabilities: []string{"chat", "responses", "stream"}, NumCtx: 65536, Enabled: true}
	if _, err := r.CreateModelDeployment(deployment); err != nil {
		t.Fatal(err)
	}
	restored, err := NewWithError(Config{ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.(*Router).ListModelDeployments(t.Context())[0].NumCtx; got != 65536 {
		t.Fatalf("restored context=%d", got)
	}
	deployment.NumCtx = 0
	saved, err := r.UpdateModelDeployment("qwen", deployment)
	if err != nil || saved.NumCtx != 65536 {
		t.Fatalf("omitted context reset: %+v %v", saved, err)
	}
	deployment.NumCtxSet = true
	saved, err = r.UpdateModelDeployment("qwen", deployment)
	if err != nil || saved.NumCtx != 0 {
		t.Fatalf("explicit reset: %+v %v", saved, err)
	}
	for _, n := range []int{-1, 1, 4095, 1048577, int(^uint(0) >> 1)} {
		deployment.NumCtx = n
		if _, err := r.UpdateModelDeployment("qwen", deployment); !errors.Is(err, ErrInvalidDeployment) {
			t.Fatalf("accepted num_ctx=%d: %v", n, err)
		}
	}
	if sameDeploymentRuntime(ModelDeployment{NumCtx: 65536}, ModelDeployment{NumCtx: 32768}) {
		t.Fatal("context change reused runtime")
	}
	if _, err := NewWithError(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "remote", Type: "openai-compatible", NumCtx: 65536}}}); err == nil {
		t.Fatal("non-Ollama config accepted")
	}
	if _, err := r.endpointForManagedDeploymentWithSecret(ModelDeployment{NumCtx: 65536}, ManagedProvider{Type: "openai-compatible"}, ""); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatal("non-Ollama deployment accepted")
	}
}

func TestOllamaContextBudget(t *testing.T) {
	cap := 256
	maxInt := int(^uint(0) >> 1)
	base := openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "hello"}}, MaxCompletionTokens: &cap}
	for _, test := range []struct {
		name   string
		edit   func(*openai.ChatCompletionRequest)
		reject bool
	}{
		{"small text", func(*openai.ChatCompletionRequest) {}, false},
		{"large ASCII", func(r *openai.ChatCompletionRequest) { r.Messages[0].Content = strings.Repeat("a", 8192) }, true},
		{"Russian byte budget", func(r *openai.ChatCompletionRequest) { r.Messages[0].Content = strings.Repeat("я", 4000) }, true},
		{"history", func(r *openai.ChatCompletionRequest) {
			r.Messages = append(r.Messages, openai.Message{Role: "assistant", Content: strings.Repeat("x", 7000)})
		}, true},
		{"tool schemas", func(r *openai.ChatCompletionRequest) {
			r.Tools = []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Description: strings.Repeat("x", 7000), Parameters: map[string]any{"type": "object"}}}}
		}, true},
		{"tool arguments", func(r *openai.ChatCompletionRequest) {
			r.Messages = append(r.Messages, openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "c", Type: "function", Function: openai.FunctionCall{Name: "lookup", Arguments: `{"value":"` + strings.Repeat("x", 7000) + `"}`}}}})
		}, true},
		{"max_completion overflow", func(r *openai.ChatCompletionRequest) { r.MaxCompletionTokens = &maxInt }, true},
		{"max_tokens overflow", func(r *openai.ChatCompletionRequest) { r.MaxCompletionTokens = nil; r.MaxTokens = &maxInt }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := base
			r.Messages = append([]openai.Message(nil), base.Messages...)
			test.edit(&r)
			err := (Ollama{numCtx: 8192}).ValidateChatParameters(r)
			if test.reject {
				var pe *Error
				if !errors.As(err, &pe) || pe.Class != FailureContextLength {
					t.Fatalf("want context error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	messages, _ := ollamaMessages(base.Messages)
	with, err := (Ollama{numCtx: 65536}).contextChatRequest(base, messages, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if with.Options.NumCtx != 65536 || with.Truncate == nil || *with.Truncate || with.Shift == nil || *with.Shift {
		t.Fatalf("unsafe native options %+v", with)
	}
	original, err := (Ollama{}).contextChatRequest(base, messages, nil, true)
	if err != nil || original.Truncate != nil || original.Shift != nil || original.Options.NumCtx != 0 {
		t.Fatalf("default changed: %+v %v", original, err)
	}
	base.MaxCompletionTokens = nil
	defaults, err := (Ollama{numCtx: 65536}).contextChatRequest(base, messages, nil, false)
	if err != nil || defaults.Options.NumPredict == nil || *defaults.Options.NumPredict != 1024 {
		t.Fatalf("unbounded output %+v %v", defaults, err)
	}
}

func TestConfiguredOllamaChatAndResponsesNativeContract(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, responses := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/responses=%t", stream, responses), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.URL.Path != "/api/chat" || r.Header.Get("Authorization") != "Bearer fixture-token" {
						t.Errorf("path=%s auth supplied=%t", r.URL.Path, r.Header.Get("Authorization") != "")
					}
					var native ollamaChatRequest
					if err := json.NewDecoder(r.Body).Decode(&native); err != nil {
						t.Error(err)
					}
					if native.Options.NumCtx != 65536 || native.Options.NumPredict == nil || *native.Options.NumPredict != 256 || native.Truncate == nil || *native.Truncate || native.Shift == nil || *native.Shift {
						t.Errorf("invalid settings %+v", native)
					}
					if stream {
						fmt.Fprintln(w, `{"model":"m","message":{"role":"assistant","thinking":"plan"},"done":false}`)
						fmt.Fprintln(w, `{"model":"m","message":{"role":"assistant","content":"ok"},"done":false}`)
					}
					text := `"content":"ok","thinking":"plan"`
					if stream {
						text = `"content":""`
					}
					fmt.Fprintf(w, `{"model":"m","message":{"role":"assistant",%s},"done":true,"done_reason":"stop","prompt_eval_count":10,"prompt_eval_cached_count":2,"eval_count":3}`, text)
				}))
				t.Cleanup(server.Close)
				client := newOllamaWithToken(server.URL, "fixture-token", true)
				client.numCtx = 65536
				limit := 256
				if responses {
					input := openai.ResponseRequest{Model: "m", Input: "hello", MaxOutputTokens: &limit}
					var got openai.ResponseResponse
					var err error
					var sse strings.Builder
					if stream {
						got, err = client.StreamResponses(t.Context(), input, func(event, payload string) error {
							fmt.Fprintf(&sse, "event: %s\ndata: %s\n\n", event, payload)
							return nil
						})
					} else {
						got, err = client.Responses(t.Context(), input)
					}
					if err != nil || got.OutputText != "ok" || got.Status != "completed" || got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 3 || got.Usage.InputTokensDetails.CachedTokens != 2 {
						t.Fatalf("result=%+v err=%v", got, err)
					}
					if len(got.Output) != 2 || got.Output[0].Type != "reasoning" {
						t.Fatalf("lost reasoning %+v", got.Output)
					}
					if stream {
						parsed, err := streamResponseDataValidated(strings.NewReader(sse.String()), "m", nil, nil)
						if err != nil || parsed.OutputText != "ok" {
							t.Fatalf("invalid generated SSE: %v", err)
						}
					}
				} else {
					request := openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "hello"}}, MaxCompletionTokens: &limit}
					var err error
					if stream {
						_, err = client.StreamChatCompletions(t.Context(), request, func(string) error { return nil })
					} else {
						_, err = client.ChatCompletions(t.Context(), request)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if calls != 1 {
					t.Fatalf("calls=%d", calls)
				}
			})
		}
	}
}

func TestOllamaContextResponseHistoryAndFormat(t *testing.T) {
	request := openai.ResponseRequest{Model: "m", Instructions: "system", Input: []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hello"}}}, map[string]any{"type": "function_call", "call_id": "c", "name": "lookup", "arguments": "{}"}, map[string]any{"type": "function_call_output", "call_id": "c", "output": "result"}}, Tools: []openai.ResponseTool{{Type: "function", Name: "lookup", Parameters: map[string]any{"type": "object"}}}, Text: map[string]any{"format": map[string]any{"type": "json_schema", "name": "answer", "schema": map[string]any{"type": "object"}}}}
	before, _ := json.Marshal(request)
	chat, err := (Ollama{numCtx: 65536}).contextResponseChat(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 4 || chat.Messages[0].Role != "system" || chat.Messages[2].ToolCalls[0].ID != "c" || chat.Messages[3].ToolCallID != "c" || len(chat.Tools) != 1 || chat.ResponseFormat.JSONSchema.Name != "answer" {
		t.Fatalf("lost fields %+v", chat)
	}
	after, _ := json.Marshal(request)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("mutated request")
	}
	request.Input = strings.Repeat("x", 65536)
	var pe *Error
	if err := (Ollama{numCtx: 65536}).ValidateResponseParameters(request); !errors.As(err, &pe) || pe.Class != FailureContextLength {
		t.Fatalf("oversized Responses: %v", err)
	}
}

func TestOllamaContextResponseToolsAndIncomplete(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tool := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/tool=%t", stream, tool), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if tool {
						fmt.Fprintln(w, `{"model":"m","message":{"role":"assistant","tool_calls":[{"function":{"name":"lookup","arguments":{"value":"x"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":3}`)
					} else {
						fmt.Fprintln(w, `{"model":"m","message":{"role":"assistant","content":"partial"},"done":true,"done_reason":"length","prompt_eval_count":10,"eval_count":3}`)
					}
				}))
				t.Cleanup(server.Close)
				p := NewOllama(server.URL, true)
				p.numCtx = 65536
				limit := 256
				request := openai.ResponseRequest{Model: "m", Input: "hello", MaxOutputTokens: &limit, Tools: []openai.ResponseTool{{Type: "function", Name: "lookup", Parameters: map[string]any{"type": "object"}}}}
				var got openai.ResponseResponse
				var err error
				var sse strings.Builder
				if stream {
					got, err = p.StreamResponses(t.Context(), request, func(event, payload string) error {
						fmt.Fprintf(&sse, "event: %s\ndata: %s\n\n", event, payload)
						return nil
					})
				} else {
					got, err = p.Responses(t.Context(), request)
				}
				if err != nil {
					t.Fatal(err)
				}
				if tool {
					if len(got.Output) != 1 || got.Output[0].Type != "function_call" || got.Output[0].Name != "lookup" || got.Output[0].CallID == "" || got.Output[0].Arguments != `{"value":"x"}` {
						t.Fatalf("lost tool call %+v", got.Output)
					}
				} else if got.Status != "incomplete" || got.IncompleteDetails == nil || got.IncompleteDetails.Reason != "max_output_tokens" {
					t.Fatalf("lost incomplete state %+v", got)
				}
				if stream {
					if _, err := streamResponseDataValidated(strings.NewReader(sse.String()), "m", nil, nil); err != nil {
						t.Fatalf("invalid SSE: %v", err)
					}
				}
			})
		}
	}
}

func TestOllamaContextStreamCancellationAndFailures(t *testing.T) {
	sentinel := errors.New("client stopped")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"model":"m","message":{"role":"assistant","content":"partial"},"done":false}`)
	}))
	t.Cleanup(server.Close)
	p := NewOllama(server.URL, true)
	p.numCtx = 65536
	_, err := p.StreamResponses(t.Context(), openai.ResponseRequest{Model: "m", Input: "hello"}, func(string, string) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("writer failure lost: %v", err)
	}
	terminal := false
	_, err = p.StreamResponses(t.Context(), openai.ResponseRequest{Model: "m", Input: "hello"}, func(kind, payload string) error {
		if kind == "response.completed" {
			terminal = true
		}
		return nil
	})
	if err == nil || terminal {
		t.Fatal("missing native terminal became successful")
	}
	for _, input := range []any{[]any{map[string]any{"type": "reasoning", "encrypted_content": "opaque"}}, []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}}}}} {
		if err := p.ValidateResponseParameters(openai.ResponseRequest{Model: "m", Input: input}); err == nil {
			t.Fatal("unsupported context silently dropped")
		}
	}
}
