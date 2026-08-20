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

const defaultAnthropicMaxTokens = 1024

type Anthropic struct {
	baseURL        string
	apiKey         string
	upstreamStream bool
	client         *http.Client
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Stream      bool               `json:"stream,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Model      string             `json:"model"`
	Content    []anthropicContent `json:"content"`
	StopReason string             `json:"stop_reason"`
	Usage      anthropicUsage     `json:"usage"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func NewAnthropic(baseURL string, apiKey string, upstreamStream bool) Anthropic {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.anthropic.com"
	}
	return Anthropic{
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         apiKey,
		upstreamStream: upstreamStream,
		client: &http.Client{
			Timeout: 180 * time.Second,
		},
	}
}

func (p Anthropic) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	upstreamRequest := anthropicChatRequest(request, false)
	var response anthropicResponse
	if err := p.doMessages(ctx, upstreamRequest, &response); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return anthropicToChatCompletion(response, request.Model), nil
}

func (p Anthropic) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if !p.upstreamStream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}

	resp, err := p.doMessagesStream(ctx, anthropicChatRequest(request, true))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	return streamAnthropicChat(resp.Body, request.Model, write)
}

func (p Anthropic) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	upstreamRequest := anthropicResponsesRequest(request, false)
	var response anthropicResponse
	if err := p.doMessages(ctx, upstreamRequest, &response); err != nil {
		return openai.ResponseResponse{}, err
	}
	return anthropicToResponse(response, request.Model), nil
}

func (p Anthropic) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if !p.upstreamStream {
		return openai.ResponseResponse{}, ErrStreamingUnsupported
	}

	resp, err := p.doMessagesStream(ctx, anthropicResponsesRequest(request, true))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	defer resp.Body.Close()

	return streamAnthropicResponses(resp.Body, request.Model, write)
}

func (p Anthropic) doMessages(ctx context.Context, request anthropicRequest, target any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("anthropic", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (p Anthropic) doMessagesStream(ctx context.Context, request anthropicRequest) (*http.Response, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, statusError("anthropic", resp.StatusCode)
	}
	return resp, nil
}

func (p Anthropic) setHeaders(request *http.Request) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Anthropic-Version", "2023-06-01")
	if p.apiKey != "" {
		request.Header.Set("X-API-Key", p.apiKey)
	}
}

func anthropicChatRequest(request openai.ChatCompletionRequest, stream bool) anthropicRequest {
	system, messages := anthropicMessages(request.Messages)
	return anthropicRequest{
		Model:       request.Model,
		System:      system,
		Messages:    messages,
		MaxTokens:   requestMaxTokens(request.MaxTokens, nil),
		Stream:      stream,
		Temperature: request.Temperature,
		TopP:        request.TopP,
	}
}

func anthropicResponsesRequest(request openai.ResponseRequest, stream bool) anthropicRequest {
	maxTokens := requestMaxTokens(request.MaxTokens, request.MaxOutputTokens)
	messages := []anthropicMessage{{Role: "user", Content: responseInputText(request.Input)}}
	return anthropicRequest{
		Model:       request.Model,
		System:      request.Instructions,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Stream:      stream,
		Temperature: request.Temperature,
		TopP:        request.TopP,
	}
}

func anthropicMessages(messages []openai.Message) (string, []anthropicMessage) {
	var system []string
	converted := make([]anthropicMessage, 0, len(messages))
	for _, message := range messages {
		content := openai.ContentText(message.Content)
		switch message.Role {
		case "system", "developer":
			if content != "" {
				system = append(system, content)
			}
		case "assistant":
			converted = append(converted, anthropicMessage{Role: "assistant", Content: content})
		default:
			converted = append(converted, anthropicMessage{Role: "user", Content: content})
		}
	}
	if len(converted) == 0 {
		converted = append(converted, anthropicMessage{Role: "user", Content: ""})
	}
	return strings.Join(system, "\n\n"), converted
}

func requestMaxTokens(maxTokens *int, maxOutputTokens *int) int {
	if maxOutputTokens != nil && *maxOutputTokens > 0 {
		return *maxOutputTokens
	}
	if maxTokens != nil && *maxTokens > 0 {
		return *maxTokens
	}
	return defaultAnthropicMaxTokens
}

func anthropicToChatCompletion(response anthropicResponse, fallbackModel string) openai.ChatCompletionResponse {
	model := response.Model
	if model == "" {
		model = fallbackModel
	}
	content := anthropicText(response)
	return openai.ChatCompletionResponse{
		ID:     response.ID,
		Object: "chat.completion",
		Model:  model,
		Choices: []openai.Choice{
			{
				Index:        0,
				Message:      openai.Message{Role: "assistant", Content: content},
				FinishReason: anthropicFinishReason(response.StopReason),
			},
		},
		Usage: openai.Usage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
	}
}

func anthropicToResponse(response anthropicResponse, fallbackModel string) openai.ResponseResponse {
	model := response.Model
	if model == "" {
		model = fallbackModel
	}
	content := anthropicText(response)
	return openai.ResponseResponse{
		ID:         response.ID,
		Object:     "response",
		CreatedAt:  time.Now().UTC().Unix(),
		Status:     "completed",
		Model:      model,
		OutputText: content,
		Output: []openai.ResponseOutputItem{
			{
				ID:     "msg-" + response.ID,
				Type:   "message",
				Status: "completed",
				Role:   "assistant",
				Content: []openai.ResponseOutputContent{
					{Type: "output_text", Text: content},
				},
			},
		},
		Usage: openai.ResponseUsage{
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  response.Usage.InputTokens + response.Usage.OutputTokens,
		},
	}
}

func anthropicText(response anthropicResponse) string {
	var parts []string
	for _, content := range response.Content {
		if content.Type == "text" && content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "")
}

func anthropicFinishReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}

func streamAnthropicChat(body io.Reader, fallbackModel string, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	response := openai.ChatCompletionResponse{
		Object: "chat.completion",
		Model:  fallbackModel,
		Choices: []openai.Choice{
			{Index: 0, Message: openai.Message{Role: "assistant"}, FinishReason: "stop"},
		},
	}
	err := scanSSEEvents(body, func(event string, payload string) error {
		if event == "message_stop" {
			return io.EOF
		}
		streamEvent, err := decodeAnthropicStreamEvent(payload)
		if err != nil {
			return err
		}
		switch event {
		case "message_start":
			response.ID = streamEvent.Message.ID
			if streamEvent.Message.Model != "" {
				response.Model = streamEvent.Message.Model
			}
			response.Usage.PromptTokens = streamEvent.Message.Usage.InputTokens
		case "content_block_delta":
			if streamEvent.Delta.Type == "text_delta" && streamEvent.Delta.Text != "" {
				response.Choices[0].Message.Content = openai.ContentText(response.Choices[0].Message.Content) + streamEvent.Delta.Text
				if err := write(openAIChatCompletionChunkPayload(response.ID, response.Model, 0, "assistant", streamEvent.Delta.Text, nil)); err != nil {
					return err
				}
			}
		case "message_delta":
			if streamEvent.Usage.OutputTokens != 0 {
				response.Usage.CompletionTokens = streamEvent.Usage.OutputTokens
				response.Usage.TotalTokens = response.Usage.PromptTokens + response.Usage.CompletionTokens
			}
			if streamEvent.Delta.StopReason != "" {
				finishReason := anthropicFinishReason(streamEvent.Delta.StopReason)
				response.Choices[0].FinishReason = finishReason
				return write(openAIChatCompletionChunkPayload(response.ID, response.Model, 0, "", "", &finishReason))
			}
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return openai.ChatCompletionResponse{}, err
	}
	return response, nil
}

func streamAnthropicResponses(body io.Reader, fallbackModel string, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	response := openai.ResponseResponse{
		Object: "response",
		Model:  fallbackModel,
		Status: "completed",
	}
	err := scanSSEEvents(body, func(event string, payload string) error {
		if event == "message_stop" {
			return io.EOF
		}
		streamEvent, err := decodeAnthropicStreamEvent(payload)
		if err != nil {
			return err
		}
		switch event {
		case "message_start":
			response.ID = streamEvent.Message.ID
			response.Model = streamEvent.Message.Model
			response.Usage.InputTokens = streamEvent.Message.Usage.InputTokens
			response.CreatedAt = time.Now().UTC().Unix()
			response.Status = "in_progress"
			return write("response.created", responseEventPayload("response.created", response, "", ""))
		case "content_block_delta":
			if streamEvent.Delta.Type == "text_delta" && streamEvent.Delta.Text != "" {
				response.OutputText += streamEvent.Delta.Text
				textSlot := ensureResponseOutputTextSlot(&response)
				textSlot.Text += streamEvent.Delta.Text
				return write("response.output_text.delta", responseEventPayload("response.output_text.delta", response, streamEvent.Delta.Text, ""))
			}
		case "message_delta":
			if streamEvent.Usage.OutputTokens != 0 {
				response.Usage.OutputTokens = streamEvent.Usage.OutputTokens
				response.Usage.TotalTokens = response.Usage.InputTokens + response.Usage.OutputTokens
			}
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return openai.ResponseResponse{}, err
	}
	response.Status = "completed"
	if response.OutputText == "" {
		response.OutputText = responseText(response)
	}
	if err := write("response.completed", responseEventPayload("response.completed", response, "", "completed")); err != nil {
		return openai.ResponseResponse{}, err
	}
	return response, nil
}

type anthropicStreamEvent struct {
	Type    string            `json:"type"`
	Message anthropicResponse `json:"message"`
	Delta   struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage anthropicUsage `json:"usage"`
}

func decodeAnthropicStreamEvent(payload string) (anthropicStreamEvent, error) {
	var event anthropicStreamEvent
	if strings.TrimSpace(payload) == "" {
		return event, nil
	}
	err := json.Unmarshal([]byte(payload), &event)
	return event, err
}

func responseEventPayload(eventType string, response openai.ResponseResponse, delta string, status string) string {
	payload := map[string]any{
		"type":        eventType,
		"response_id": response.ID,
	}
	if delta != "" {
		payload["delta"] = delta
	}
	if status != "" || eventType == "response.created" {
		payload["response"] = response
	}
	marshaled, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(marshaled)
}
