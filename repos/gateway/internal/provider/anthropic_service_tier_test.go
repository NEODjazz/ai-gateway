package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAnthropicServiceTierRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ServiceTier string `json:"service_tier"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.ServiceTier != "standard_only" {
			t.Errorf("service tier lost: %q", body.ServiceTier)
		}
		_, _ = w.Write([]byte(`{"id":"msg-tier","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1,"service_tier":"priority"}}`))
	}))
	defer server.Close()

	response, err := NewAnthropic(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "claude-test", Messages: []openai.Message{{Role: "user", Content: "hello"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "standard_only"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ServiceTier != "priority" {
		t.Fatalf("assigned service tier lost: %+v", response)
	}
}

func TestAnthropicServiceTierStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"message\":{\"id\":\"msg-tier\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":2,\"service_tier\":\"standard\"}}}\n\n" +
			"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
			"event: message_stop\ndata: {}\n\n"))
	}))
	defer server.Close()

	var chunks []string
	response, err := NewAnthropic(server.URL, "key", true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "claude-test"}, func(payload string) error {
		chunks = append(chunks, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ServiceTier != "standard" || !strings.Contains(strings.Join(chunks, "\n"), `"service_tier":"standard"`) {
		t.Fatalf("stream service tier lost: response=%+v chunks=%s", response, strings.Join(chunks, "\n"))
	}
}

func TestAnthropicRejectsInvalidReportedServiceTier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"msg-tier","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1,"service_tier":"unknown"}}`))
	}))
	defer server.Close()
	if _, err := NewAnthropic(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "claude-test"}); err == nil {
		t.Fatal("invalid provider service tier was accepted")
	}
}

func TestAnthropicRejectsChangingStreamServiceTier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"message\":{\"id\":\"msg-tier\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":2,\"service_tier\":\"standard\"}}}\n\n" +
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1,\"service_tier\":\"priority\"}}\n\n"))
	}))
	defer server.Close()
	if _, err := NewAnthropic(server.URL, "key", true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "claude-test"}, func(string) error { return nil }); err == nil {
		t.Fatal("changing provider service tier was accepted")
	}
}
