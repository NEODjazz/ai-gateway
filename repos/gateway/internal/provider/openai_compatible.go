package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

type openAICompatibleChatRequest struct {
	Model       string           `json:"model"`
	Messages    []openai.Message `json:"messages"`
	Stream      bool             `json:"stream,omitempty"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	TopP        *float64         `json:"top_p,omitempty"`
}

type openAICompatibleResponseRequest struct {
	Model           string   `json:"model"`
	Input           any      `json:"input"`
	Instructions    string   `json:"instructions,omitempty"`
	Stream          bool     `json:"stream,omitempty"`
	MaxOutputTokens *int     `json:"max_output_tokens,omitempty"`
	MaxTokens       *int     `json:"max_tokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
}

type OpenAICompatible struct {
	baseURL        string
	apiKey         string
	upstreamStream bool
	client         *http.Client
}

func NewOpenAICompatible(baseURL string, apiKey string, upstreamStream bool) OpenAICompatible {
	return OpenAICompatible{
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         apiKey,
		upstreamStream: upstreamStream,
		client: &http.Client{
			Timeout: 180 * time.Second,
		},
	}
}

func (p OpenAICompatible) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	body, err := json.Marshal(openAICompatibleChatRequest{
		Model:       request.Model,
		Messages:    request.Messages,
		Stream:      request.Stream && p.upstreamStream,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		TopP:        request.TopP,
	})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "chat/completions"), bytes.NewReader(body))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ChatCompletionResponse{}, fmt.Errorf("openai-compatible provider returned %s", resp.Status)
	}

	if request.Stream && p.upstreamStream {
		return decodeChatCompletionStream(resp.Body, request.Model)
	}

	var response openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return response, nil
}

func (p OpenAICompatible) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if !p.upstreamStream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}

	body, err := json.Marshal(openAICompatibleChatRequest{
		Model:       request.Model,
		Messages:    request.Messages,
		Stream:      true,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		TopP:        request.TopP,
	})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "chat/completions"), bytes.NewReader(body))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ChatCompletionResponse{}, fmt.Errorf("openai-compatible provider returned %s", resp.Status)
	}

	return streamChatCompletionData(resp.Body, request.Model, write)
}

func (p OpenAICompatible) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	body, err := json.Marshal(openAICompatibleResponseRequest{
		Model:           request.Model,
		Input:           request.Input,
		Instructions:    request.Instructions,
		Stream:          false,
		MaxOutputTokens: request.MaxOutputTokens,
		MaxTokens:       request.MaxTokens,
		Temperature:     request.Temperature,
		TopP:            request.TopP,
	})
	if err != nil {
		return openai.ResponseResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "responses"), bytes.NewReader(body))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ResponseResponse{}, fmt.Errorf("responses provider returned %s", resp.Status)
	}

	var response openai.ResponseResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return openai.ResponseResponse{}, err
	}
	response.OutputText = responseText(response)
	return response, nil
}

func (p OpenAICompatible) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if !p.upstreamStream {
		return openai.ResponseResponse{}, ErrStreamingUnsupported
	}

	body, err := json.Marshal(openAICompatibleResponseRequest{
		Model:           request.Model,
		Input:           request.Input,
		Instructions:    request.Instructions,
		Stream:          true,
		MaxOutputTokens: request.MaxOutputTokens,
		MaxTokens:       request.MaxTokens,
		Temperature:     request.Temperature,
		TopP:            request.TopP,
	})
	if err != nil {
		return openai.ResponseResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "responses"), bytes.NewReader(body))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ResponseResponse{}, fmt.Errorf("responses provider returned %s", resp.Status)
	}

	return streamResponseData(resp.Body, request.Model, write)
}

func providerURL(baseURL string, path string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/" + strings.TrimLeft(path, "/")
	}
	return baseURL + "/v1/" + strings.TrimLeft(path, "/")
}

func openAIChatCompletionChunkPayload(id string, model string, index int, role string, content string, finishReason *string) string {
	delta := map[string]any{}
	if role != "" {
		delta["role"] = role
	}
	if content != "" {
		delta["content"] = content
	}
	payload, err := json.Marshal(map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().UTC().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         index,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	})
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func decodeChatCompletionStream(body io.Reader, fallbackModel string) (openai.ChatCompletionResponse, error) {
	return streamChatCompletionData(body, fallbackModel, nil)
}

func streamChatCompletionData(body io.Reader, fallbackModel string, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	response := openai.ChatCompletionResponse{
		Object: "chat.completion",
		Model:  fallbackModel,
		Choices: []openai.Choice{
			{
				Index:        0,
				Message:      openai.Message{Role: "assistant"},
				FinishReason: "stop",
			},
		},
	}
	err := scanSSEData(body, func(payload string) error {
		if payload == "[DONE]" {
			return io.EOF
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return err
		}
		if chunk.ID != "" {
			response.ID = chunk.ID
		}
		if chunk.Model != "" {
			response.Model = chunk.Model
		}
		for _, choice := range chunk.Choices {
			for len(response.Choices) <= choice.Index {
				response.Choices = append(response.Choices, openai.Choice{Index: len(response.Choices), Message: openai.Message{Role: "assistant"}})
			}
			current := &response.Choices[choice.Index]
			current.Index = choice.Index
			if choice.Delta.Role != "" {
				current.Message.Role = choice.Delta.Role
			}
			if choice.Delta.Content != "" {
				current.Message.Content = openai.ContentText(current.Message.Content) + choice.Delta.Content
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				current.FinishReason = *choice.FinishReason
			}
		}
		if write != nil {
			return write(payload)
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return openai.ChatCompletionResponse{}, err
	}
	return response, nil
}

func decodeResponseStream(body io.Reader, fallbackModel string) (openai.ResponseResponse, error) {
	return streamResponseData(body, fallbackModel, nil)
}

func streamResponseData(body io.Reader, fallbackModel string, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	response := openai.ResponseResponse{
		Object: "response",
		Model:  fallbackModel,
		Status: "completed",
		Output: []openai.ResponseOutputItem{
			{
				Type:   "message",
				Status: "completed",
				Role:   "assistant",
				Content: []openai.ResponseOutputContent{
					{Type: "output_text"},
				},
			},
		},
	}
	err := scanSSEEvents(body, func(event string, payload string) error {
		if payload == "[DONE]" {
			return io.EOF
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			return err
		}
		if id, ok := decoded["response_id"].(string); ok && response.ID == "" {
			response.ID = id
		}
		if delta, ok := decoded["delta"].(string); ok {
			response.OutputText += delta
			textSlot := ensureResponseOutputTextSlot(&response)
			textSlot.Text += delta
		}
		if typed, ok := decoded["response"].(map[string]any); ok {
			marshaled, err := json.Marshal(typed)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(marshaled, &response); err != nil {
				return err
			}
			response.OutputText = responseText(response)
		}
		if write != nil {
			if event == "" {
				event = eventName(decoded)
			}
			return write(event, payload)
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return openai.ResponseResponse{}, err
	}
	if response.OutputText == "" {
		response.OutputText = responseText(response)
	}
	return response, nil
}

func sseData(body io.Reader) []string {
	var payloads []string
	_ = scanSSEData(body, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	return payloads
}

func scanSSEData(body io.Reader, handle func(payload string) error) error {
	return scanSSEEvents(body, func(_ string, payload string) error {
		return handle(payload)
	})
}

func scanSSEEvents(body io.Reader, handle func(event string, payload string) error) error {
	var builder strings.Builder
	var event string
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if builder.Len() > 0 {
				if err := handle(event, strings.TrimSpace(builder.String())); err != nil {
					return err
				}
				builder.Reset()
				event = ""
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if builder.Len() > 0 {
		if err := handle(event, strings.TrimSpace(builder.String())); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func eventName(event map[string]any) string {
	if typed, ok := event["type"].(string); ok {
		return typed
	}
	return ""
}

func ensureResponseOutputTextSlot(response *openai.ResponseResponse) *openai.ResponseOutputContent {
	for outputIndex := range response.Output {
		for contentIndex := range response.Output[outputIndex].Content {
			if response.Output[outputIndex].Content[contentIndex].Type == "output_text" {
				return &response.Output[outputIndex].Content[contentIndex]
			}
		}
	}
	response.Output = append(response.Output, openai.ResponseOutputItem{
		Type:   "message",
		Status: "completed",
		Role:   "assistant",
		Content: []openai.ResponseOutputContent{
			{Type: "output_text"},
		},
	})
	outputIndex := len(response.Output) - 1
	return &response.Output[outputIndex].Content[0]
}
