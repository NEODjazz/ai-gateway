package provider

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

type Demo struct{}

func (Demo) Embeddings(_ context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := (Demo{}).ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	inputs, ok := openai.EmbeddingInputStrings(request.Input)
	if !ok {
		return openai.EmbeddingResponse{}, fmt.Errorf("invalid embedding input")
	}
	dimensions := 8
	if request.Dimensions != nil {
		dimensions = *request.Dimensions
	}
	if dimensions < 1 || dimensions > 3072 {
		return openai.EmbeddingResponse{}, fmt.Errorf("dimensions must be between 1 and 3072")
	}
	data := make([]openai.Embedding, len(inputs))
	promptTokens := 0
	for index, input := range inputs {
		promptTokens += len(strings.Fields(input))
		vector := make([]float64, dimensions)
		for dimension := range vector {
			digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", input, dimension)))
			vector[dimension] = float64(int(digest[0])-128) / 128
		}
		data[index] = openai.Embedding{Object: "embedding", Embedding: vector, Index: index}
	}
	return openai.EmbeddingResponse{
		Object: "list", Data: data, Model: request.Model,
		Usage: openai.Usage{PromptTokens: promptTokens, TotalTokens: promptTokens},
	}, nil
}

func (d Demo) ChatCompletions(_ context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := d.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
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

func (p Demo) Responses(_ context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if err := p.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
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
