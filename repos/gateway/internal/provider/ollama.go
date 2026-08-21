package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

type Ollama struct {
	baseURL        string
	upstreamStream bool
	client         *http.Client
}

type ollamaChatRequest struct {
	Model    string           `json:"model"`
	Messages []openai.Message `json:"messages"`
	Tools    []openai.Tool    `json:"tools,omitempty"`
	Format   any              `json:"format,omitempty"`
	Stream   bool             `json:"stream"`
}

type ollamaChatResponse struct {
	Model              string         `json:"model"`
	Message            openai.Message `json:"message"`
	Done               bool           `json:"done"`
	PromptEvalCount    int            `json:"prompt_eval_count"`
	EvalCount          int            `json:"eval_count"`
	DoneReason         string         `json:"done_reason"`
	TotalDuration      int64          `json:"total_duration"`
	LoadDuration       int64          `json:"load_duration"`
	PromptEvalDuration int64          `json:"prompt_eval_duration"`
	EvalDuration       int64          `json:"eval_duration"`
}

type ollamaEmbeddingRequest struct {
	Model      string `json:"model"`
	Input      any    `json:"input"`
	Dimensions *int   `json:"dimensions,omitempty"`
}

type ollamaEmbeddingResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float64 `json:"embeddings"`
	PromptEvalCount int         `json:"prompt_eval_count"`
}

func NewOllama(baseURL string, upstreamStream bool) Ollama {
	return Ollama{
		baseURL:        strings.TrimRight(baseURL, "/"),
		upstreamStream: upstreamStream,
		client:         newProviderHTTPClient(180 * time.Second),
	}
}

func (p Ollama) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	body, err := json.Marshal(ollamaChatRequest{
		Model:    request.Model,
		Messages: request.Messages,
		Tools:    request.Tools,
		Format:   ollamaResponseFormat(request.ResponseFormat),
		Stream:   request.Stream && p.upstreamStream,
	})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ChatCompletionResponse{}, statusError("ollama", resp.StatusCode)
	}

	var ollamaResp ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return openai.ChatCompletionResponse{}, err
	}

	finishReason := ollamaResp.DoneReason
	if finishReason == "" {
		finishReason = "stop"
	}

	return openai.ChatCompletionResponse{
		ID:     "chatcmpl-ollama",
		Object: "chat.completion",
		Model:  ollamaResp.Model,
		Choices: []openai.Choice{
			{
				Index:        0,
				Message:      ollamaResp.Message,
				FinishReason: finishReason,
			},
		},
		Usage: openai.Usage{
			PromptTokens:     ollamaResp.PromptEvalCount,
			CompletionTokens: ollamaResp.EvalCount,
			TotalTokens:      ollamaResp.PromptEvalCount + ollamaResp.EvalCount,
		},
	}, nil
}

func (p Ollama) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	body, err := json.Marshal(ollamaEmbeddingRequest{Model: request.Model, Input: request.Input, Dimensions: request.Dimensions})
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.EmbeddingResponse{}, statusError("ollama", resp.StatusCode)
	}
	var upstream ollamaEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	data := make([]openai.Embedding, len(upstream.Embeddings))
	for index, vector := range upstream.Embeddings {
		data[index] = openai.Embedding{Object: "embedding", Embedding: vector, Index: index}
	}
	return openai.EmbeddingResponse{
		Object: "list", Data: data, Model: upstream.Model,
		Usage: openai.Usage{PromptTokens: upstream.PromptEvalCount, TotalTokens: upstream.PromptEvalCount},
	}, nil
}

func (p Ollama) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if !p.upstreamStream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}

	body, err := json.Marshal(ollamaChatRequest{
		Model:    request.Model,
		Messages: request.Messages,
		Tools:    request.Tools,
		Format:   ollamaResponseFormat(request.ResponseFormat),
		Stream:   true,
	})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ChatCompletionResponse{}, statusError("ollama", resp.StatusCode)
	}

	response := openai.ChatCompletionResponse{
		ID:     "chatcmpl-ollama",
		Object: "chat.completion",
		Model:  request.Model,
		Choices: []openai.Choice{
			{
				Index:   0,
				Message: openai.Message{Role: "assistant"},
			},
		},
	}

	decoder := json.NewDecoder(resp.Body)
	sentRole := false
	for {
		var chunk ollamaChatResponse
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return openai.ChatCompletionResponse{}, err
		}
		if chunk.Model != "" {
			response.Model = chunk.Model
		}
		if chunk.Message.Role != "" {
			response.Choices[0].Message.Role = chunk.Message.Role
		}
		content := openai.ContentText(chunk.Message.Content)
		if content != "" {
			response.Choices[0].Message.Content = openai.ContentText(response.Choices[0].Message.Content) + content
			role := ""
			if !sentRole {
				role = response.Choices[0].Message.Role
				if role == "" {
					role = "assistant"
				}
				sentRole = true
			}
			if err := write(openAIChatCompletionChunkPayload(response.ID, response.Model, 0, role, content, nil)); err != nil {
				return openai.ChatCompletionResponse{}, err
			}
		}
		if chunk.Done {
			finishReason := chunk.DoneReason
			if finishReason == "" {
				finishReason = "stop"
			}
			response.Choices[0].FinishReason = finishReason
			response.Usage = openai.Usage{
				PromptTokens:     chunk.PromptEvalCount,
				CompletionTokens: chunk.EvalCount,
				TotalTokens:      chunk.PromptEvalCount + chunk.EvalCount,
			}
			if err := write(openAIChatCompletionChunkPayload(response.ID, response.Model, 0, "", "", &finishReason)); err != nil {
				return openai.ChatCompletionResponse{}, err
			}
		}
	}
	if response.Choices[0].FinishReason == "" {
		response.Choices[0].FinishReason = "stop"
	}
	return response, nil
}

func ollamaResponseFormat(format *openai.ResponseFormat) any {
	if format == nil {
		return nil
	}
	if format.Type == "json_object" {
		return "json"
	}
	if format.Type == "json_schema" && format.JSONSchema != nil {
		return format.JSONSchema.Schema
	}
	return nil
}

func (p Ollama) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	body, err := json.Marshal(openAICompatibleResponseRequest{
		Model:           request.Model,
		Input:           request.Input,
		Instructions:    request.Instructions,
		Stream:          false,
		MaxOutputTokens: request.MaxOutputTokens,
	})
	if err != nil {
		return openai.ResponseResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ResponseResponse{}, statusError("ollama", resp.StatusCode)
	}

	var response openai.ResponseResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return openai.ResponseResponse{}, err
	}
	response.OutputText = responseText(response)
	return response, nil
}

func (p Ollama) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if !p.upstreamStream {
		return openai.ResponseResponse{}, ErrStreamingUnsupported
	}

	body, err := json.Marshal(openAICompatibleResponseRequest{
		Model:           request.Model,
		Input:           request.Input,
		Instructions:    request.Instructions,
		Stream:          true,
		MaxOutputTokens: request.MaxOutputTokens,
	})
	if err != nil {
		return openai.ResponseResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ResponseResponse{}, statusError("ollama", resp.StatusCode)
	}

	return streamResponseData(resp.Body, request.Model, write)
}

func responseText(response openai.ResponseResponse) string {
	if response.OutputText != "" {
		return response.OutputText
	}
	for _, item := range response.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" && content.Text != "" {
				return content.Text
			}
		}
	}
	return ""
}
