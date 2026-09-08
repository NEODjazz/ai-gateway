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
		if len(tools) != 1 || tools[0].(map[string]any)["input_schema"] == nil {
			t.Error("tool schema lost")
		}
		content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
		if len(content) != 2 || content[1].(map[string]any)["source"].(map[string]any)["data"] != "iVBORw0KGgo=" {
			t.Error("image context lost")
		}
		if body["tool_choice"].(map[string]any)["type"] != "any" {
			t.Error("tool choice lost")
		}
		_, _ = w.Write([]byte(`{"input_tokens":321}`))
	}))
	defer server.Close()
	var counter TokenCountClient = NewAnthropic(server.URL, "test-key", false)
	result, err := counter.CountTokens(context.Background(), countTestRequest())
	if err != nil || result.InputTokens != 321 || result.Source != "anthropic" || result.Model != "claude-test" {
		t.Fatalf("count: %+v %v", result, err)
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
