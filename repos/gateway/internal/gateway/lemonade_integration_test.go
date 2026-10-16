package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

// Explicit opt-in: regular unit tests never call an installed model or server.
func TestLemonadeLiveGateway(t *testing.T) {
	baseURL := os.Getenv("LEMONADE_INTEGRATION_BASE_URL")
	model := os.Getenv("LEMONADE_INTEGRATION_MODEL")
	if baseURL == "" || model == "" {
		t.Skip("set LEMONADE_INTEGRATION_BASE_URL and LEMONADE_INTEGRATION_MODEL for live validation")
	}
	limit := 256
	if os.Getenv("LEMONADE_INTEGRATION_TOOLS") == "1" {
		limit = 512
	}
	fn := map[string]any{"name": "lookup_city", "description": "Get a city temperature", "parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string", "enum": []string{"Paris"}}}, "required": []string{"city"}, "additionalProperties": false}}
	type liveCase struct {
		name, path string
		body       map[string]any
		tool       bool
	}
	cases := []liveCase{
		{"chat JSON", "/v1/chat/completions", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Say hello."}}, "max_tokens": limit, "temperature": 0}, false},
		{"chat SSE", "/v1/chat/completions", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Say hello."}}, "max_completion_tokens": limit, "stream": true, "stream_options": map[string]any{"include_usage": true}}, false},
		{"chat sampling", "/v1/chat/completions", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Say hello."}}, "max_tokens": 32, "top_k": 10, "top_p": 0.9, "repetition_penalty": 1.1, "seed": 7, "store": false}, false},
		{"chat structured output", "/v1/chat/completions", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Return a JSON object with greeting hello."}}, "max_tokens": limit, "temperature": 0, "response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "greeting", "strict": true, "schema": map[string]any{"type": "object", "properties": map[string]any{"greeting": map[string]any{"type": "string"}}, "required": []string{"greeting"}, "additionalProperties": false}}}}, false},
		{"completions JSON", "/v1/completions", map[string]any{"prompt": "The sky is", "max_tokens": 8}, false},
		{"completions SSE", "/v1/completions", map[string]any{"prompt": "The sky is", "max_tokens": 32, "stream": true}, false},
		{"responses JSON", "/v1/responses", map[string]any{"input": "Say hello.", "max_output_tokens": limit, "store": false}, false},
		{"responses SSE", "/v1/responses", map[string]any{"input": "Say hello.", "max_output_tokens": limit, "store": false, "stream": true}, false},
		{"responses message input", "/v1/responses", map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Say hello."}}}}, "max_output_tokens": limit, "store": false, "tool_choice": "auto"}, false},
		{"messages JSON bridge", "/v1/messages", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Say hello."}}, "max_tokens": limit}, false},
		{"messages SSE bridge", "/v1/messages", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Say hello."}}, "max_tokens": limit, "stream": true}, false},
	}
	if os.Getenv("LEMONADE_INTEGRATION_TOOLS") == "1" {
		for _, stream := range []bool{false, true} {
			cases = append(cases, liveCase{fmt.Sprintf("chat tools stream=%t", stream), "/v1/chat/completions", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Use lookup_city to get the temperature in Paris."}}, "max_tokens": limit, "temperature": 0, "stream": stream, "tools": []any{map[string]any{"type": "function", "function": fn}}, "tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "lookup_city"}}}, true})
			responseTool := map[string]any{"type": "function"}
			for k, v := range fn {
				responseTool[k] = v
			}
			cases = append(cases, liveCase{fmt.Sprintf("responses tools stream=%t", stream), "/v1/responses", map[string]any{"input": "Use lookup_city to get the temperature in Paris.", "max_output_tokens": limit, "store": false, "temperature": 0, "stream": stream, "tools": []any{responseTool}, "tool_choice": map[string]any{"type": "function", "name": "lookup_city"}}, true})
		}
		cases = append(cases,
			liveCase{"chat tool result", "/v1/chat/completions", map[string]any{"messages": []any{
				map[string]any{"role": "user", "content": "What temperature did lookup_city report?"},
				map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-test-1", "type": "function", "function": map[string]any{"name": "lookup_city", "arguments": `{"city":"Paris"}`}}}},
				map[string]any{"role": "tool", "tool_call_id": "call-test-1", "content": `{"temperature":21}`},
			}, "max_tokens": limit, "temperature": 0}, false},
			liveCase{"responses tool result", "/v1/responses", map[string]any{"input": []any{
				map[string]any{"role": "user", "content": "What temperature did lookup_city report?"},
				map[string]any{"type": "function_call", "call_id": "call-test-1", "name": "lookup_city", "arguments": `{"city":"Paris"}`},
				map[string]any{"type": "function_call_output", "call_id": "call-test-1", "output": `{"temperature":21}`},
			}, "max_output_tokens": limit, "store": false, "temperature": 0}, false},
		)
	}
	if os.Getenv("LEMONADE_INTEGRATION_VISION") == "1" {
		picture := image.NewRGBA(image.Rect(0, 0, 64, 64))
		for y := 0; y < 64; y++ {
			for x := 0; x < 64; x++ {
				picture.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
			}
		}
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, picture); err != nil {
			t.Fatal(err)
		}
		url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
		cases = append(cases, liveCase{"chat vision", "/v1/chat/completions", map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Name the dominant color in this image."}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}}}}, "max_tokens": limit}, false})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			recorder := &lemonadeLiveUsageRecorder{}
			endpoint := config.ProviderEndpointConfig{Name: "live-lemonade", Type: "lemonade", BaseURL: baseURL, APIKey: os.Getenv("LEMONADE_INTEGRATION_API_KEY"), Models: []string{"gateway-live-model"}, ModelAliases: map[string]string{"gateway-live-model": model}, Stream: true, Capabilities: []string{"chat", "responses", "tools", "structured_output", "stream"}}
			if os.Getenv("LEMONADE_INTEGRATION_VISION") == "1" {
				endpoint.Capabilities = append(endpoint.Capabilities, "vision")
				endpoint.AVEnabled = true
			}
			router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{endpoint}, Modules: modules.NewPipeline([]modules.Module{recorder})})
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"gateway-live-model"}, tools: []string{"lookup_city"}}}}), router))
			test.body["model"] = "gateway-live-model"
			body, err := json.Marshal(test.body)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(string(body)))
			request.Header.Set("Authorization", "Bearer gateway-test-key")
			request.Header.Set("X-Request-ID", "same-external-test-id")
			if test.path == "/v1/messages" {
				request.Header.Set("anthropic-version", "2023-06-01")
			}
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, request)
			if out.Code != http.StatusOK || strings.Contains(out.Body.String(), `"type":"error"`) || strings.Contains(out.Body.String(), `"type":"response.failed"`) {
				t.Fatalf("Gateway failed: HTTP %d %s", out.Code, out.Body.String())
			}
			if recorder.calls != 1 || recorder.total <= 0 || recorder.input <= 0 || recorder.output < 0 || recorder.total != recorder.input+recorder.output || !recorder.reported {
				t.Fatalf("usage: %+v", recorder)
			}
			if test.tool && (recorder.tools == 0 || !recorder.validArguments) {
				t.Fatal("expected tool call with valid city arguments absent")
			}
			if test.name == "chat structured output" {
				var structured map[string]any
				if err := json.Unmarshal([]byte(recorder.content), &structured); err != nil {
					t.Fatalf("invalid structured output: %v", err)
				}
				if greeting, ok := structured["greeting"].(string); !ok || greeting == "" || len(structured) != 1 {
					t.Fatal("structured output does not match requested schema")
				}
			}
			if stream, _ := test.body["stream"].(bool); stream {
				if !strings.HasPrefix(out.Header().Get("Content-Type"), "text/event-stream") {
					t.Fatal("stream content type missing")
				}
				if test.path == "/v1/responses" && !strings.Contains(out.Body.String(), "response.completed") {
					t.Fatal("Responses terminal event missing")
				}
			} else if !json.Valid(out.Body.Bytes()) {
				t.Fatal("invalid public JSON")
			}
			t.Logf("public=%s native=%s endpoint=%s usage=%d/%d/%d reported=%t", "gateway-live-model", model, test.path, recorder.input, recorder.output, recorder.total, recorder.reported)
		})
	}
}

type lemonadeLiveUsageRecorder struct {
	calls, input, output, total, tools int
	reported                           bool
	validArguments                     bool
	content                            string
}

func (*lemonadeLiveUsageRecorder) Name() string                                          { return "billing" }
func (*lemonadeLiveUsageRecorder) Required() bool                                        { return true }
func (*lemonadeLiveUsageRecorder) Handle(context.Context, *modules.RequestContext) error { return nil }
func (*lemonadeLiveUsageRecorder) PostResponseEnabled() bool                             { return true }
func (m *lemonadeLiveUsageRecorder) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.calls++
	if r := req.Response; r != nil {
		m.input, m.output, m.total, m.reported = r.Usage.PromptTokens, r.Usage.CompletionTokens, r.Usage.TotalTokens, r.UsageReported
		for _, choice := range r.Choices {
			if content, ok := choice.Message.Content.(string); ok {
				m.content += content
			}
			for _, call := range choice.Message.ToolCalls {
				if call.Function.Name == "lookup_city" {
					m.tools++
					m.validArguments = lemonadeLiveArgumentsValid(call.Function.Arguments)
				}
			}
		}
	}
	if r := req.CompletionResponse; r != nil {
		m.input, m.output, m.total, m.reported = r.Usage.PromptTokens, r.Usage.CompletionTokens, r.Usage.TotalTokens, r.UsageReported
	}
	if r := req.ResponsesResponse; r != nil {
		m.input, m.output, m.total, m.reported = r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.TotalTokens, r.InputTokensReported && r.OutputTokensReported
		for _, item := range r.Output {
			if item.Type == "function_call" && item.Name == "lookup_city" {
				m.tools++
				m.validArguments = lemonadeLiveArgumentsValid(item.Arguments)
			}
		}
	}
	return nil
}

func lemonadeLiveArgumentsValid(arguments string) bool {
	var value map[string]any
	return json.Unmarshal([]byte(arguments), &value) == nil && len(value) == 1 && value["city"] == "Paris"
}
