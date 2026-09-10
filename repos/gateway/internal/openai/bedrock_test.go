package openai

import (
	"encoding/base64"
	"testing"
)

func stringPointer(value string) *string { return &value }

func TestBedrockConverseMapsToolHistoryAndConfiguration(t *testing.T) {
	maxTokens := 32
	request := BedrockConverseRequest{
		System: []BedrockContentBlock{{Text: stringPointer("be concise")}},
		Messages: []BedrockMessage{
			{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("weather")}}},
			{Role: "assistant", Content: []BedrockContentBlock{{ToolUse: &BedrockToolUse{ID: "call_1", Name: "weather", Input: map[string]any{"city": "Paris"}}}}},
			{Role: "user", Content: []BedrockContentBlock{{ToolResult: &BedrockToolResult{ID: "call_1", Content: []BedrockContentBlock{{Text: stringPointer("sunny")}}}}}},
		},
		InferenceConfig: BedrockInferenceConfig{MaxTokens: &maxTokens},
		ToolConfig:      &BedrockToolConfig{Tools: []BedrockTool{{Spec: BedrockToolSpec{Name: "weather", InputSchema: BedrockToolInputSchema{JSON: map[string]any{"type": "object"}}}}}},
	}
	chat, err := request.ChatRequest("public", "deployment")
	if err != nil || chat.Model != "public" || chat.Provider != "deployment" || chat.MaxCompletionTokens == nil || *chat.MaxCompletionTokens != 32 || len(chat.Messages) != 4 || chat.Messages[2].ToolCalls[0].Function.Arguments != `{"city":"Paris"}` || chat.Messages[3].Role != "tool" || len(chat.Tools) != 1 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
}

func TestBedrockConverseRejectsAmbiguousContent(t *testing.T) {
	request := BedrockConverseRequest{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("hello"), ToolUse: &BedrockToolUse{ID: "call", Name: "tool", Input: map[string]any{}}}}}}}
	if _, err := request.ChatRequest("model", ""); err == nil {
		t.Fatal("ambiguous content accepted")
	}
}

func TestBedrockConverseMapsBoundedUserImages(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	request := BedrockConverseRequest{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{
		{Text: stringPointer("describe")},
		{Image: &BedrockImage{Format: "png", Source: BedrockImageSource{Bytes: data}}},
	}}}}
	chat, err := request.ChatRequest("model", "bedrock")
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := ChatImageAttachments(chat.Messages)
	if err != nil || len(attachments) != 1 || attachments[0].MediaType != "image/png" || len(chat.Messages[0].Content.([]any)) != 2 {
		t.Fatalf("chat=%+v attachments=%+v err=%v", chat, attachments, err)
	}
	request.Messages[0].Role = "assistant"
	if _, err := request.ChatRequest("model", "bedrock"); err == nil {
		t.Fatal("assistant image accepted")
	}
	request.Messages[0].Role = "user"
	request.Messages[0].Content[1].Image.Source.Bytes = base64.StdEncoding.EncodeToString([]byte("not a png"))
	if _, err := request.ChatRequest("model", "bedrock"); err == nil {
		t.Fatal("invalid image signature accepted")
	}
}

func TestBedrockConverseResponsePreservesToolsAndUsage(t *testing.T) {
	response, err := BedrockFromChat(ChatCompletionResponse{
		Choices: []Choice{{FinishReason: "tool_calls", Message: Message{Role: "assistant", Content: "checking", ToolCalls: []ToolCall{{ID: "call", Type: "function", Function: FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}}}}}},
		Usage:   Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10},
	})
	if err != nil || response.StopReason != "tool_use" || response.Usage.TotalTokens != 10 || len(response.Output.Message.Content) != 2 || response.Output.Message.Content[1].ToolUse.Input.(map[string]any)["city"] != "Paris" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseResponseRejectsUnknownFinishReason(t *testing.T) {
	_, err := BedrockFromChat(ChatCompletionResponse{Choices: []Choice{{FinishReason: "unknown", Message: Message{Role: "assistant", Content: "hello"}}}})
	if err == nil {
		t.Fatal("unknown finish reason accepted")
	}
}
