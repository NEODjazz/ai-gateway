package provider

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestAnthropicStreamsSettleCumulativeUsage(t *testing.T) {
	stream := "event: message_start\ndata: {\"message\":{\"id\":\"message\",\"model\":\"model\",\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":2,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\ndata: {\"usage\":{\"input_tokens\":7,\"cache_read_input_tokens\":4,\"output_tokens\":8}}\n\n" +
		"event: message_stop\ndata: {}\n\n"
	chat, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, "", nil, func(string) error { return nil })
	if err != nil || chat.Usage.PromptTokens != 11 || chat.Usage.CompletionTokens != 8 || chat.Usage.TotalTokens != 19 || chat.Usage.PromptTokensDetails == nil || chat.Usage.PromptTokensDetails.CachedTokens != 4 {
		t.Fatalf("Chat cumulative usage=%+v err=%v", chat.Usage, err)
	}
	response, err := streamAnthropicResponses(strings.NewReader(stream), "model", false, func(_, _ string) error { return nil })
	if err != nil || response.Usage.InputTokens != 11 || response.Usage.OutputTokens != 8 || response.Usage.TotalTokens != 19 || response.Usage.InputTokensDetails == nil || response.Usage.InputTokensDetails.CachedTokens != 4 {
		t.Fatalf("Responses cumulative usage=%+v err=%v", response.Usage, err)
	}
}

func TestAnthropicStreamsPreserveStartOutputUsage(t *testing.T) {
	stream := "event: message_start\ndata: {\"message\":{\"id\":\"message\",\"model\":\"model\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n" +
		"event: message_stop\ndata: {}\n\n"
	chat, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, "", nil, func(string) error { return nil })
	if err != nil || chat.Usage.CompletionTokens != 1 || chat.Usage.TotalTokens != 6 {
		t.Fatalf("Chat start usage=%+v err=%v", chat.Usage, err)
	}
	response, err := streamAnthropicResponses(strings.NewReader(stream), "model", false, func(_, _ string) error { return nil })
	if err != nil || response.Usage.OutputTokens != 1 || response.Usage.TotalTokens != 6 {
		t.Fatalf("Responses start usage=%+v err=%v", response.Usage, err)
	}
}

func TestAnthropicStreamsRejectDecreasingCumulativeUsage(t *testing.T) {
	for _, delta := range []string{`{"input_tokens":4}`, `{"output_tokens":2}`} {
		t.Run(delta, func(t *testing.T) {
			stream := "event: message_start\ndata: {\"message\":{\"id\":\"message\",\"model\":\"model\",\"usage\":{\"input_tokens\":5,\"output_tokens\":3}}}\n\n" +
				"event: message_delta\ndata: {\"usage\":" + delta + "}\n\n" +
				"event: message_stop\ndata: {}\n\n"
			if _, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, "", nil, func(string) error { return nil }); err == nil {
				t.Fatal("Chat accepted decreasing cumulative usage")
			}
			completed := false
			if _, err := streamAnthropicResponses(strings.NewReader(stream), "model", false, func(event, _ string) error {
				completed = completed || event == "response.completed"
				return nil
			}); err == nil || completed {
				t.Fatalf("Responses accepted decreasing cumulative usage or completed: err=%v completed=%t", err, completed)
			}
		})
	}
}

func TestAnthropicStreamsRejectOverflowingCumulativeInput(t *testing.T) {
	stream := "event: message_start\ndata: {\"message\":{\"id\":\"message\",\"model\":\"model\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n" +
		fmt.Sprintf("event: message_delta\ndata: {\"usage\":{\"input_tokens\":%d,\"output_tokens\":1}}\n\n", math.MaxInt) +
		"event: message_stop\ndata: {}\n\n"
	if _, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, "", nil, func(string) error { return nil }); err == nil {
		t.Fatal("Chat accepted overflowing cumulative input")
	}
	if _, err := streamAnthropicResponses(strings.NewReader(stream), "model", false, func(_, _ string) error { return nil }); err == nil {
		t.Fatal("Responses accepted overflowing cumulative input")
	}
}
