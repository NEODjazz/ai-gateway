package provider

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAnthropicUsageRejectsTokenOverflow(t *testing.T) {
	for _, test := range []struct {
		name  string
		usage anthropicUsage
	}{
		{"cache read", anthropicUsage{InputTokens: math.MaxInt, CacheReadInputTokens: 1}},
		{"cache creation", anthropicUsage{InputTokens: math.MaxInt, CacheCreationInputTokens: 1}},
		{"both caches", anthropicUsage{CacheReadInputTokens: math.MaxInt, CacheCreationInputTokens: 1}},
		{"output", anthropicUsage{InputTokens: math.MaxInt, OutputTokens: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateAnthropicUsage(test.usage); err == nil {
				t.Fatal("overflowing Anthropic usage accepted")
			}
		})
	}
	if err := validateAnthropicUsage(anthropicUsage{InputTokens: math.MaxInt - 3, CacheReadInputTokens: 1, CacheCreationInputTokens: 1, OutputTokens: 1}); err != nil {
		t.Fatalf("valid boundary usage rejected: %v", err)
	}
}

func TestAnthropicChatRejectsOverflowingProviderUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"id":"message","stop_reason":"end_turn","content":[{"type":"text","text":"answer"}],"usage":{"input_tokens":%d,"output_tokens":1}}`, math.MaxInt)
	}))
	t.Cleanup(server.Close)
	if _, err := NewAnthropic(server.URL, "", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model"}); err == nil {
		t.Fatal("overflowing provider usage returned as a successful Chat response")
	}

	stream := fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"message\",\"usage\":{\"input_tokens\":%d,\"output_tokens\":1}}}\n\n", math.MaxInt)
	forwarded := false
	_, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, "", nil, func(string) error {
		forwarded = true
		return nil
	})
	if err == nil || forwarded {
		t.Fatalf("overflowing stream usage accepted or forwarded: err=%v forwarded=%t", err, forwarded)
	}
}

func TestAnthropicStreamsRejectOverflowingCombinedUsage(t *testing.T) {
	stream := fmt.Sprintf("event: message_start\ndata: {\"message\":{\"id\":\"message\",\"model\":\"model\",\"usage\":{\"input_tokens\":%d}}}\n\nevent: message_delta\ndata: {\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n", math.MaxInt)
	if _, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, "", nil, func(string) error { return nil }); err == nil {
		t.Fatal("Chat stream accepted combined token overflow")
	}
	completed := false
	if _, err := streamAnthropicResponses(strings.NewReader(stream), "model", false, func(event, _ string) error {
		completed = completed || event == "response.completed"
		return nil
	}); err == nil || completed {
		t.Fatalf("Responses stream accepted combined token overflow or completed: err=%v completed=%t", err, completed)
	}
}

func TestAnthropicStreamsAcceptMaximumCombinedUsage(t *testing.T) {
	stream := fmt.Sprintf("event: message_start\ndata: {\"message\":{\"id\":\"message\",\"model\":\"model\",\"usage\":{\"input_tokens\":%d}}}\n\nevent: message_delta\ndata: {\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n", math.MaxInt-1)
	chat, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, "", nil, func(string) error { return nil })
	if err != nil || chat.Usage.TotalTokens != math.MaxInt {
		t.Fatalf("valid Chat boundary rejected: usage=%+v err=%v", chat.Usage, err)
	}
	response, err := streamAnthropicResponses(strings.NewReader(stream), "model", false, func(_, _ string) error { return nil })
	if err != nil || response.Usage.TotalTokens != math.MaxInt {
		t.Fatalf("valid Responses boundary rejected: usage=%+v err=%v", response.Usage, err)
	}
}

func TestAnthropicResponsesStreamRejectsInvalidStartUsage(t *testing.T) {
	stream := "event: message_start\ndata: {\"message\":{\"id\":\"message\",\"model\":\"model\",\"usage\":{\"input_tokens\":-1}}}\n\nevent: message_stop\ndata: {}\n\n"
	forwarded := false
	if _, err := streamAnthropicResponses(strings.NewReader(stream), "model", false, func(_, _ string) error {
		forwarded = true
		return nil
	}); err == nil || forwarded {
		t.Fatalf("Responses stream accepted invalid start usage or forwarded it: err=%v forwarded=%t", err, forwarded)
	}
}
