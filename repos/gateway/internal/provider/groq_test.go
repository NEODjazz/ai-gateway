package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestGroqChatMapsSupportedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer groq-key" {
			t.Fatalf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "model" || body["max_completion_tokens"] != float64(32) || body["service_tier"] != "flex" || len(body["tools"].([]any)) != 1 || body["response_format"] == nil {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer server.Close()
	maxTokens := 32
	client := NewGroq(server.URL+"/openai/v1", "groq-key", true)
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, MaxCompletionTokens: &maxTokens, ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "flex"},
		Tools:          []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}},
		ResponseFormat: &openai.ResponseFormat{Type: "json_object"},
	})
	if err != nil || response.Usage.TotalTokens != 3 || openai.ContentText(response.Choices[0].Message.Content) != "ok" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGroqRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewGroq(server.URL, "key", true)
	logprobs := true
	_, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &logprobs}})
	if err == nil || !strings.Contains(err.Error(), "logprobs") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestGroqStreamsWithUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\ndata: {\"id\":\"chat\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewGroq(server.URL, "key", true)
	var payloads []string
	response, err := client.StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Stream: true}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil || response.Usage.TotalTokens != 3 || len(payloads) != 2 {
		t.Fatalf("response=%+v payloads=%v err=%v", response, payloads, err)
	}
}
