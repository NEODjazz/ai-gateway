package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

type Demo struct{}

func (Demo) ChatCompletions(_ context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	content := "Gateway accepted request for model " + request.Model
	if len(request.Messages) > 0 {
		content += ". Last message: " + openai.ContentText(request.Messages[len(request.Messages)-1].Content)
	}

	completionTokens := len(strings.Fields(content))
	return openai.ChatCompletionResponse{
		ID:     "chatcmpl-demo",
		Object: "chat.completion",
		Model:  request.Model,
		Choices: []openai.Choice{
			{
				Index: 0,
				Message: openai.Message{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
		Usage: openai.Usage{
			CompletionTokens: completionTokens,
			TotalTokens:      completionTokens,
		},
	}, nil
}

func (Demo) Responses(_ context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	content := "Gateway accepted response request for model " + request.Model + ". Input: " + responseInputText(request.Input)
	outputTokens := len(strings.Fields(content))
	return openai.ResponseResponse{
		ID:         "resp-demo",
		Object:     "response",
		CreatedAt:  time.Now().Unix(),
		Status:     "completed",
		Model:      request.Model,
		OutputText: content,
		Output: []openai.ResponseOutputItem{
			{
				ID:     "msg-demo",
				Type:   "message",
				Status: "completed",
				Role:   "assistant",
				Content: []openai.ResponseOutputContent{
					{Type: "output_text", Text: content},
				},
			},
		},
		Usage: openai.ResponseUsage{
			OutputTokens: outputTokens,
			TotalTokens:  outputTokens,
		},
	}, nil
}

func responseInputText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, responseInputText(item))
		}
		return strings.Join(parts, " ")
	case map[string]any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, responseInputText(item))
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprint(value)
	}
}
