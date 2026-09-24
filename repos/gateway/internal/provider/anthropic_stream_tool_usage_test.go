package provider

import (
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAnthropicChatStreamPreservesPartialServerToolUsage(t *testing.T) {
	stream := "event: message_start\ndata: {\"message\":{\"id\":\"message\",\"model\":\"model\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"server_tool_use\":{\"web_search_requests\":1,\"code_execution_requests\":1}}}}\n\n" +
		"event: message_delta\ndata: {\"usage\":{\"output_tokens\":2,\"server_tool_use\":{\"web_search_requests\":2}}}\n\n" +
		"event: message_delta\ndata: {\"usage\":{\"output_tokens\":3,\"server_tool_use\":{\"code_execution_requests\":2}}}\n\n" +
		"event: message_stop\ndata: {}\n\n"
	chat, err := streamAnthropicChat(strings.NewReader(stream), "model", false, &openai.ChatWebSearchOptions{}, nil, true, "", nil, func(string) error { return nil })
	if err != nil || chat.Usage.SearchRequests != 2 || chat.Usage.ToolRequests != 2 {
		t.Fatalf("partial server tool usage=%+v err=%v", chat.Usage, err)
	}
}

func TestAnthropicChatStreamRejectsDecreasingServerToolUsage(t *testing.T) {
	stream := "event: message_start\ndata: {\"message\":{\"id\":\"message\",\"model\":\"model\",\"usage\":{\"input_tokens\":3,\"server_tool_use\":{\"web_search_requests\":2}}}}\n\n" +
		"event: message_delta\ndata: {\"usage\":{\"output_tokens\":1,\"server_tool_use\":{\"web_search_requests\":1}}}\n\n" +
		"event: message_stop\ndata: {}\n\n"
	if _, err := streamAnthropicChat(strings.NewReader(stream), "model", false, &openai.ChatWebSearchOptions{}, nil, false, "", nil, func(string) error { return nil }); err == nil {
		t.Fatal("decreasing web search usage was accepted")
	}
}
