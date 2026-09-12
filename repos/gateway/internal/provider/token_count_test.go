package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func countTestRequest() TokenCountRequest {
	return TokenCountRequest{Model: "claude-test", Messages: []openai.Message{{Role: "system", Content: "Be concise"}, {Role: "user", Content: []any{map[string]any{"type": "text", "text": "Describe"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}}}}}, Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}}}}, ToolChoice: "required"}
}

func anthropicCachedCountTestRequest() TokenCountRequest {
	request := countTestRequest()
	request.Messages[1].Content.([]any)[0].(map[string]any)["prompt_cache_breakpoint"] = map[string]any{"mode": "explicit", "ttl": "1h"}
	request.Tools[0].Function.PromptCacheBreakpoint = &openai.PromptCacheBreakpoint{Mode: "explicit", TTL: "5m"}
	return request
}

func TestAnthropicTokenCountPreservesOutputConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OutputConfig *anthropicOutputConfig `json:"output_config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.OutputConfig == nil || body.OutputConfig.Effort != "high" || body.OutputConfig.Format == nil || body.OutputConfig.Format.Type != "json_schema" {
			t.Fatalf("output config lost: %+v", body.OutputConfig)
		}
		_, _ = w.Write([]byte(`{"input_tokens":7}`))
	}))
	defer server.Close()
	result, err := NewAnthropic(server.URL, "", false).CountTokens(context.Background(), TokenCountRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hi"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "high"},
		ResponseFormat:        &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Schema: map[string]any{"type": "object"}}},
	})
	if err != nil || result.InputTokens != 7 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAnthropicTokenCountPreservesContextManagement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-beta") != "context-management-2025-06-27" {
			t.Errorf("beta=%q", r.Header.Get("anthropic-beta"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["context_management"] == nil {
			t.Fatalf("body=%v err=%v", body, err)
		}
		_, _ = w.Write([]byte(`{"input_tokens":25,"context_management":{"original_input_tokens":70}}`))
	}))
	defer server.Close()
	result, err := NewAnthropic(server.URL, "", false).CountTokens(context.Background(), TokenCountRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hi"}},
		AnthropicContextManagement: json.RawMessage(`{"edits":[{"type":"clear_tool_uses_20250919"}]}`),
	})
	if err != nil || result.InputTokens != 25 || result.OriginalInputTokens == nil || *result.OriginalInputTokens != 70 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestAnthropicCountTokensIncludesNativeContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/count_tokens" || r.Method != "POST" || r.Header.Get("x-api-key") != "test-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
			t.Error("wrong native count request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["model"] != "claude-test" || body["system"] != "Be concise" || body["max_tokens"] != nil || body["stream"] != nil {
			t.Errorf("wrong counter body: %v", body)
		}
		tools := body["tools"].([]any)
		if len(tools) != 1 || tools[0].(map[string]any)["input_schema"] == nil || tools[0].(map[string]any)["cache_control"].(map[string]any)["ttl"] != "5m" {
			t.Error("tool schema lost")
		}
		content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
		if len(content) != 2 || content[0].(map[string]any)["cache_control"].(map[string]any)["ttl"] != "1h" || content[1].(map[string]any)["source"].(map[string]any)["data"] != "iVBORw0KGgo=" {
			t.Error("image context lost")
		}
		if body["tool_choice"].(map[string]any)["type"] != "any" {
			t.Error("tool choice lost")
		}
		_, _ = w.Write([]byte(`{"input_tokens":321}`))
	}))
	defer server.Close()
	var counter TokenCountClient = NewAnthropic(server.URL, "test-key", false)
	result, err := counter.CountTokens(context.Background(), anthropicCachedCountTestRequest())
	if err != nil || result.InputTokens != 321 || result.Source != "anthropic" || result.Model != "claude-test" {
		t.Fatalf("count: %+v %v", result, err)
	}
}

func TestAnthropicCountTokensIncludesSkillExecutionContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-beta") != "skills-2025-10-02" {
			t.Errorf("missing skills beta header: %v", r.Header)
		}
		var body struct {
			Container *anthropicContainer `json:"container"`
			Tools     []anthropicTool     `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Container == nil || body.Container.ID != "container_1" || len(body.Container.Skills) != 1 || body.Container.Skills[0].SkillID != "skill_1" || len(body.Tools) != 1 || body.Tools[0].Type != "code_execution_20250825" {
			t.Errorf("skill execution context lost: %+v", body)
		}
		_, _ = w.Write([]byte(`{"input_tokens":37}`))
	}))
	defer server.Close()
	result, err := NewAnthropic(server.URL, "", false).CountTokens(t.Context(), TokenCountRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "count"}},
		AnthropicSkills:      []openai.AnthropicSkillReference{{Type: "custom", SkillID: "skill_1", Version: "v1"}},
		AnthropicContainerID: "container_1", AnthropicCodeExecution: true,
	})
	if err != nil || result.InputTokens != 37 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAnthropicCountTokensIncludesInferenceGeo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.InferenceGeo != "us" || body.CacheControl == nil || body.CacheControl.TTL != "1h" {
			t.Errorf("inference_geo=%q cache_control=%+v", body.InferenceGeo, body.CacheControl)
		}
		_, _ = w.Write([]byte(`{"input_tokens":17}`))
	}))
	defer server.Close()
	result, err := NewAnthropic(server.URL, "", false).CountTokens(t.Context(), TokenCountRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "count"}}, AnthropicInferenceGeo: "us", AnthropicCacheControl: &openai.PromptCacheBreakpoint{Mode: "explicit", TTL: "1h"},
	})
	if err != nil || result.InputTokens != 17 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAnthropicCountTokensIncludesNativeClientTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []anthropicTool `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Tools) != 1 || body.Tools[0].Type != "memory_20250818" || body.Tools[0].Name != "memory" {
			t.Errorf("client tool context lost: %+v", body.Tools)
		}
		_, _ = w.Write([]byte(`{"input_tokens":29}`))
	}))
	defer server.Close()
	result, err := NewAnthropic(server.URL, "", false).CountTokens(t.Context(), TokenCountRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "count"}}, AnthropicClientTools: []openai.AnthropicClientTool{{Type: "memory_20250818", Name: "memory"}},
	})
	if err != nil || result.InputTokens != 29 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAnthropicCountTokensIncludesNativeClientToolsets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []anthropicTool `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Tools) != 1 || body.Tools[0].Type != "computer_toolset_20260801" || body.Tools[0].Name != "" {
			t.Errorf("client toolset context lost: %+v", body.Tools)
		}
		_, _ = w.Write([]byte(`{"input_tokens":4590}`))
	}))
	defer server.Close()
	result, err := NewAnthropic(server.URL, "", false).CountTokens(t.Context(), TokenCountRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "count"}}, AnthropicClientToolsets: []openai.AnthropicClientToolset{{Type: "computer_toolset_20260801", Name: "computer"}},
	})
	if err != nil || result.InputTokens != 4590 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAnthropicCountTokensIncludesBrowserToolset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []anthropicTool `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Tools) != 1 || body.Tools[0].Type != "browser_toolset_20260801" || body.Tools[0].Name != "" {
			t.Errorf("browser toolset context lost: %+v", body.Tools)
		}
		_, _ = w.Write([]byte(`{"input_tokens":6670}`))
	}))
	defer server.Close()
	result, err := NewAnthropic(server.URL, "", false).CountTokens(t.Context(), TokenCountRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "count"}}, AnthropicClientToolsets: []openai.AnthropicClientToolset{{Type: "browser_toolset_20260801", Name: "browser"}},
	})
	if err != nil || result.InputTokens != 6670 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAnthropicCountTokensIncludesThinking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Thinking *anthropicThinking `json:"thinking"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Thinking == nil || body.Thinking.Type != "adaptive" || body.Thinking.Display != "omitted" {
			t.Errorf("thinking context lost: %+v", body.Thinking)
		}
		_, _ = w.Write([]byte(`{"input_tokens":31}`))
	}))
	defer server.Close()
	result, err := NewAnthropic(server.URL, "", false).CountTokens(t.Context(), TokenCountRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "count"}}, AnthropicThinking: &openai.AnthropicThinkingConfig{Type: "adaptive", Display: "omitted"}})
	if err != nil || result.InputTokens != 31 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestAnthropicCounterRejectsMalformedCounts(t *testing.T) {
	for _, payload := range []string{`{}`, `{"input_tokens":null}`, `{"input_tokens":-1}`, `{"input_tokens":1.5}`, `{"input_tokens":9223372036854775808}`, `{"input_tokens":1} {}`, strings.Repeat(" ", (64<<10)+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(payload)) }))
		_, err := NewAnthropic(server.URL, "", false).CountTokens(context.Background(), countTestRequest())
		server.Close()
		if err == nil {
			t.Fatalf("malformed count accepted: %.40s", payload)
		}
	}
}
func TestAnthropicCounterRejectsUnsupportedContextBeforeHTTP(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, change := range []func(*TokenCountRequest){
		func(r *TokenCountRequest) { r.ToolChoice = "unrecognized" },
		func(r *TokenCountRequest) { r.Messages[1].Role = "unknown" },
		func(r *TokenCountRequest) {
			r.Messages[1].Content = []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "audio"}}}
		},
		func(r *TokenCountRequest) { r.Tools[0].Type = "server_tool" },
	} {
		request := countTestRequest()
		change(&request)
		if _, err := NewAnthropic(server.URL, "", false).CountTokens(context.Background(), request); err == nil {
			t.Fatal("unsupported context accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported context reached upstream")
	}
}
func TestAnthropicCounterRejectsRedirectAndClassifiesErrors(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	if _, err := NewAnthropic(server.URL, "test-key", false).CountTokens(context.Background(), countTestRequest()); err == nil || reached.Load() {
		t.Fatal("redirect followed")
	}
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"sensitive"}}`))
	}))
	defer limited.Close()
	_, err := NewAnthropic(limited.URL, "", false).CountTokens(context.Background(), countTestRequest())
	var failure *Error
	if !errors.As(err, &failure) || failure.StatusCode != 429 || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unclassified/unredacted error: %v", err)
	}
}
func TestAnthropicCounterHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	if _, err := NewAnthropic(server.URL, "", false).CountTokens(ctx, countTestRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
