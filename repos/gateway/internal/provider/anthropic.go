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
	Tools       []anthropicTool    `json:"tools,omitempty"`
	ToolChoice  map[string]any     `json:"tool_choice,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Stream      bool               `json:"stream,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
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
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Source    any    `json:"source,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   any    `json:"content,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

func NewAnthropic(baseURL string, apiKey string, upstreamStream bool) Anthropic {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.anthropic.com"
	}
	return Anthropic{
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         apiKey,
		upstreamStream: upstreamStream,
		client:         newProviderHTTPClient(180 * time.Second),
	}
}

func (Anthropic) SupportsVision() bool { return true }

func (p Anthropic) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	upstreamRequest := anthropicChatRequest(request, false)
	var response anthropicResponse
	if err := p.doMessages(ctx, upstreamRequest, &response); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	converted := anthropicToChatCompletion(response, request.Model)
	if request.ResponseFormat != nil {
		converted = anthropicStructuredChat(converted)
	}
	return converted, nil
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

	return streamAnthropicChat(resp.Body, request.Model, request.ResponseFormat != nil, write)
}

func (p Anthropic) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	upstreamRequest := anthropicResponsesRequest(request, false)
	var response anthropicResponse
	if err := p.doMessages(ctx, upstreamRequest, &response); err != nil {
		return openai.ResponseResponse{}, err
	}
	converted := anthropicToResponse(response, request.Model)
	if _, structured := anthropicStructuredResponseTool(request.Text); structured {
		converted = anthropicStructuredResponse(converted)
	}
	return converted, nil
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

	_, structured := anthropicStructuredResponseTool(request.Text)
	return streamAnthropicResponses(resp.Body, request.Model, structured, write)
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
		return responseStatusError("anthropic", resp)
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
		return nil, responseStatusError("anthropic", resp)
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
	tools, toolChoice := anthropicChatTools(request.Tools, request.ToolChoice)
	if request.ResponseFormat != nil {
		name := "structured_output"
		schema := any(map[string]any{"type": "object"})
		if request.ResponseFormat.JSONSchema != nil {
			if request.ResponseFormat.JSONSchema.Name != "" {
				name = request.ResponseFormat.JSONSchema.Name
			}
			schema = request.ResponseFormat.JSONSchema.Schema
		}
		tools = append(tools, anthropicTool{Name: name, Description: "Return the response using the required JSON schema.", InputSchema: schema})
		toolChoice = map[string]any{"type": "tool", "name": name}
	}
	return anthropicRequest{
		Model:       request.Model,
		System:      system,
		Messages:    messages,
		Tools:       tools,
		ToolChoice:  toolChoice,
		MaxTokens:   requestMaxTokens(request.MaxTokens, request.MaxCompletionTokens),
		Stream:      stream,
		Temperature: request.Temperature,
		TopP:        request.TopP,
	}
}

func anthropicStructuredChat(response openai.ChatCompletionResponse) openai.ChatCompletionResponse {
	for index := range response.Choices {
		if len(response.Choices[index].Message.ToolCalls) == 0 {
			continue
		}
		response.Choices[index].Message.Content = response.Choices[index].Message.ToolCalls[0].Function.Arguments
		response.Choices[index].Message.ToolCalls = nil
		response.Choices[index].FinishReason = "stop"
	}
	return response
}

func anthropicResponsesRequest(request openai.ResponseRequest, stream bool) anthropicRequest {
	maxTokens := requestMaxTokens(request.MaxTokens, request.MaxOutputTokens)
	messages := anthropicResponseMessages(request.Input)
	tools, toolChoice := anthropicResponseTools(request.Tools, request.ToolChoice)
	if structuredTool, ok := anthropicStructuredResponseTool(request.Text); ok {
		tools = append(tools, structuredTool)
		toolChoice = map[string]any{"type": "tool", "name": structuredTool.Name}
	}
	return anthropicRequest{
		Model:       request.Model,
		System:      request.Instructions,
		Messages:    messages,
		Tools:       tools,
		ToolChoice:  toolChoice,
		MaxTokens:   maxTokens,
		Stream:      stream,
		Temperature: request.Temperature,
		TopP:        request.TopP,
	}
}

func anthropicStructuredResponseTool(text any) (anthropicTool, bool) {
	encoded, err := json.Marshal(text)
	if err != nil || string(encoded) == "null" {
		return anthropicTool{}, false
	}
	var config map[string]any
	if json.Unmarshal(encoded, &config) != nil {
		return anthropicTool{}, false
	}
	format, _ := config["format"].(map[string]any)
	if format == nil {
		return anthropicTool{}, false
	}
	typeName, _ := format["type"].(string)
	if typeName != "json_schema" && typeName != "json_object" {
		return anthropicTool{}, false
	}
	name, _ := format["name"].(string)
	if name == "" {
		name = "structured_output"
	}
	schema := format["schema"]
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	return anthropicTool{Name: name, Description: "Return the response using the required JSON schema.", InputSchema: schema}, true
}

func anthropicStructuredResponse(response openai.ResponseResponse) openai.ResponseResponse {
	for _, item := range response.Output {
		if item.Type != "function_call" {
			continue
		}
		response.OutputText = item.Arguments
		response.Output = []openai.ResponseOutputItem{{
			ID: item.ID, Type: "message", Status: "completed", Role: "assistant",
			Content: []openai.ResponseOutputContent{{Type: "output_text", Text: item.Arguments}},
		}}
		break
	}
	return response
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
			blocks := make([]anthropicContent, 0, len(message.ToolCalls)+1)
			if content != "" {
				blocks = append(blocks, anthropicContent{Type: "text", Text: content})
			}
			for _, call := range message.ToolCalls {
				var input any = map[string]any{}
				if call.Function.Arguments != "" {
					if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
						input = map[string]any{"raw": call.Function.Arguments}
					}
				}
				blocks = append(blocks, anthropicContent{Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: input})
			}
			if len(blocks) == 0 {
				converted = append(converted, anthropicMessage{Role: "assistant", Content: ""})
			} else {
				converted = append(converted, anthropicMessage{Role: "assistant", Content: blocks})
			}
		case "tool":
			converted = append(converted, anthropicMessage{Role: "user", Content: []anthropicContent{{
				Type: "tool_result", ToolUseID: message.ToolCallID, Content: message.Content,
			}}})
		default:
			converted = append(converted, anthropicMessage{Role: "user", Content: anthropicMessageContent(message.Content)})
		}
	}
	if len(converted) == 0 {
		converted = append(converted, anthropicMessage{Role: "user", Content: ""})
	}
	return strings.Join(system, "\n\n"), converted
}

func anthropicResponseMessages(input any) []anthropicMessage {
	if items, ok := input.([]any); ok {
		messages := make([]anthropicMessage, 0, len(items))
		for _, item := range items {
			object, ok := item.(map[string]any)
			if !ok {
				messages = nil
				break
			}
			role, _ := object["role"].(string)
			if role != "user" && role != "assistant" {
				messages = nil
				break
			}
			messages = append(messages, anthropicMessage{Role: role, Content: anthropicMessageContent(object["content"])})
		}
		if len(messages) > 0 {
			return messages
		}
	}
	return []anthropicMessage{{Role: "user", Content: anthropicMessageContent(input)}}
}

func anthropicMessageContent(value any) any {
	items, ok := value.([]any)
	if !ok {
		return openai.ContentText(value)
	}
	blocks := make([]anthropicContent, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			if text := openai.ContentText(item); text != "" {
				blocks = append(blocks, anthropicContent{Type: "text", Text: text})
			}
			continue
		}
		typeName, _ := object["type"].(string)
		switch typeName {
		case "text", "input_text":
			if text, _ := object["text"].(string); text != "" {
				blocks = append(blocks, anthropicContent{Type: "text", Text: text})
			}
		case "image_url", "input_image":
			imageURL := ""
			if typeName == "image_url" {
				if image, ok := object["image_url"].(map[string]any); ok {
					imageURL, _ = image["url"].(string)
				}
			} else {
				imageURL, _ = object["image_url"].(string)
			}
			if attachment, err := openai.ParseDataImageURL(imageURL); err == nil {
				blocks = append(blocks, anthropicContent{Type: "image", Source: map[string]any{
					"type": "base64", "media_type": attachment.MediaType, "data": attachment.Data,
				}})
			}
		default:
			if text := openai.ContentText(object); text != "" {
				blocks = append(blocks, anthropicContent{Type: "text", Text: text})
			}
		}
	}
	return blocks
}

func anthropicChatTools(tools []openai.Tool, choice any) ([]anthropicTool, map[string]any) {
	converted := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" || tool.Function.Name == "" {
			continue
		}
		schema := tool.Function.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		converted = append(converted, anthropicTool{Name: tool.Function.Name, Description: tool.Function.Description, InputSchema: schema})
	}
	return applyAnthropicToolChoice(converted, choice)
}

func anthropicResponseTools(tools []openai.ResponseTool, choice any) ([]anthropicTool, map[string]any) {
	converted := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" || tool.Name == "" {
			continue
		}
		schema := tool.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		converted = append(converted, anthropicTool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
	}
	return applyAnthropicToolChoice(converted, choice)
}

func applyAnthropicToolChoice(tools []anthropicTool, choice any) ([]anthropicTool, map[string]any) {
	if len(tools) == 0 {
		return nil, nil
	}
	switch typed := choice.(type) {
	case string:
		switch typed {
		case "none":
			return nil, nil
		case "required":
			return tools, map[string]any{"type": "any"}
		case "auto", "":
			return tools, map[string]any{"type": "auto"}
		}
	}
	encoded, err := json.Marshal(choice)
	if err == nil {
		var object map[string]any
		if json.Unmarshal(encoded, &object) == nil {
			name, _ := object["name"].(string)
			if function, ok := object["function"].(map[string]any); ok {
				name, _ = function["name"].(string)
			}
			if name != "" {
				return tools, map[string]any{"type": "tool", "name": name}
			}
		}
	}
	return tools, map[string]any{"type": "auto"}
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
	toolCalls := anthropicToolCalls(response)
	inputTokens := anthropicInputTokens(response.Usage)
	return openai.ChatCompletionResponse{
		ID:     response.ID,
		Object: "chat.completion",
		Model:  model,
		Choices: []openai.Choice{
			{
				Index:        0,
				Message:      openai.Message{Role: "assistant", Content: content, ToolCalls: toolCalls},
				FinishReason: anthropicFinishReason(response.StopReason),
			},
		},
		Usage: openai.Usage{
			PromptTokens:     inputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      inputTokens + response.Usage.OutputTokens,
			PromptTokensDetails: &openai.PromptTokenDetails{
				CachedTokens:     response.Usage.CacheReadInputTokens,
				CacheWriteTokens: response.Usage.CacheCreationInputTokens,
			},
		},
	}
}

func anthropicToResponse(response anthropicResponse, fallbackModel string) openai.ResponseResponse {
	model := response.Model
	if model == "" {
		model = fallbackModel
	}
	content := anthropicText(response)
	output := make([]openai.ResponseOutputItem, 0, len(response.Content))
	if content != "" {
		output = append(output, openai.ResponseOutputItem{
			ID: "msg-" + response.ID, Type: "message", Status: "completed", Role: "assistant",
			Content: []openai.ResponseOutputContent{{Type: "output_text", Text: content}},
		})
	}
	for _, block := range response.Content {
		if block.Type != "tool_use" {
			continue
		}
		output = append(output, openai.ResponseOutputItem{
			ID: block.ID, Type: "function_call", Status: "completed", CallID: block.ID,
			Name: block.Name, Arguments: jsonArguments(block.Input),
		})
	}
	inputTokens := anthropicInputTokens(response.Usage)
	return openai.ResponseResponse{
		ID:         response.ID,
		Object:     "response",
		CreatedAt:  time.Now().UTC().Unix(),
		Status:     "completed",
		Model:      model,
		OutputText: content,
		Output:     output,
		Usage: openai.ResponseUsage{
			InputTokens:  inputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  inputTokens + response.Usage.OutputTokens,
			InputTokensDetails: &openai.InputTokenDetails{
				CachedTokens:     response.Usage.CacheReadInputTokens,
				CacheWriteTokens: response.Usage.CacheCreationInputTokens,
			},
		},
	}
}

func anthropicInputTokens(usage anthropicUsage) int {
	return usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
}

func anthropicToolCalls(response anthropicResponse) []openai.ToolCall {
	var calls []openai.ToolCall
	for _, block := range response.Content {
		if block.Type != "tool_use" {
			continue
		}
		calls = append(calls, openai.ToolCall{
			ID: block.ID, Type: "function",
			Function: openai.FunctionCall{Name: block.Name, Arguments: jsonArguments(block.Input)},
		})
	}
	return calls
}

func jsonArguments(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
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

func streamAnthropicChat(body io.Reader, fallbackModel string, structured bool, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	response := openai.ChatCompletionResponse{
		Object: "chat.completion",
		Model:  fallbackModel,
		Choices: []openai.Choice{
			{Index: 0, Message: openai.Message{Role: "assistant"}, FinishReason: "stop"},
		},
	}
	toolIndexes := map[int]int{}
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
			response.Usage.PromptTokens = anthropicInputTokens(streamEvent.Message.Usage)
			response.Usage.PromptTokensDetails = &openai.PromptTokenDetails{CachedTokens: streamEvent.Message.Usage.CacheReadInputTokens, CacheWriteTokens: streamEvent.Message.Usage.CacheCreationInputTokens}
		case "content_block_start":
			if streamEvent.ContentBlock.Type == "tool_use" {
				toolIndex := len(response.Choices[0].Message.ToolCalls)
				toolIndexes[streamEvent.Index] = toolIndex
				call := openai.ToolCall{ID: streamEvent.ContentBlock.ID, Type: "function", Function: openai.FunctionCall{Name: streamEvent.ContentBlock.Name}}
				response.Choices[0].Message.ToolCalls = append(response.Choices[0].Message.ToolCalls, call)
				if !structured {
					return write(openAIChatToolCallChunkPayload(response.ID, response.Model, toolIndex, call))
				}
			}
		case "content_block_delta":
			if streamEvent.Delta.Type == "text_delta" && streamEvent.Delta.Text != "" {
				response.Choices[0].Message.Content = openai.ContentText(response.Choices[0].Message.Content) + streamEvent.Delta.Text
				if err := write(openAIChatCompletionChunkPayload(response.ID, response.Model, 0, "assistant", streamEvent.Delta.Text, nil)); err != nil {
					return err
				}
			}
			if streamEvent.Delta.Type == "input_json_delta" && streamEvent.Delta.PartialJSON != "" {
				toolIndex, found := toolIndexes[streamEvent.Index]
				if !found || toolIndex >= len(response.Choices[0].Message.ToolCalls) {
					return nil
				}
				response.Choices[0].Message.ToolCalls[toolIndex].Function.Arguments += streamEvent.Delta.PartialJSON
				if structured {
					response.Choices[0].Message.Content = openai.ContentText(response.Choices[0].Message.Content) + streamEvent.Delta.PartialJSON
					return write(openAIChatCompletionChunkPayload(response.ID, response.Model, 0, "", streamEvent.Delta.PartialJSON, nil))
				}
				delta := openai.ToolCall{Type: "function", Function: openai.FunctionCall{Arguments: streamEvent.Delta.PartialJSON}}
				index := toolIndex
				delta.Index = &index
				return write(openAIChatToolCallChunkPayload(response.ID, response.Model, toolIndex, delta))
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
	if structured {
		response = anthropicStructuredChat(response)
	}
	return response, nil
}

func openAIChatToolCallChunkPayload(id, model string, toolIndex int, call openai.ToolCall) string {
	index := toolIndex
	call.Index = &index
	payload, err := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().UTC().Unix(), "model": model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"tool_calls": []openai.ToolCall{call}}, "finish_reason": nil}},
	})
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func streamAnthropicResponses(body io.Reader, fallbackModel string, structured bool, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	response := openai.ResponseResponse{
		Object: "response",
		Model:  fallbackModel,
		Status: "completed",
	}
	toolOutputs := map[int]int{}
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
			response.Usage.InputTokens = anthropicInputTokens(streamEvent.Message.Usage)
			response.Usage.InputTokensDetails = &openai.InputTokenDetails{CachedTokens: streamEvent.Message.Usage.CacheReadInputTokens, CacheWriteTokens: streamEvent.Message.Usage.CacheCreationInputTokens}
			response.CreatedAt = time.Now().UTC().Unix()
			response.Status = "in_progress"
			return write("response.created", responseEventPayload("response.created", response, "", ""))
		case "content_block_start":
			if streamEvent.ContentBlock.Type == "tool_use" {
				outputIndex := len(response.Output)
				toolOutputs[streamEvent.Index] = outputIndex
				item := openai.ResponseOutputItem{
					ID: streamEvent.ContentBlock.ID, Type: "function_call", Status: "in_progress",
					CallID: streamEvent.ContentBlock.ID, Name: streamEvent.ContentBlock.Name,
				}
				response.Output = append(response.Output, item)
				if !structured {
					return write("response.output_item.added", responseOutputItemEvent("response.output_item.added", outputIndex, item, ""))
				}
			}
		case "content_block_delta":
			if streamEvent.Delta.Type == "text_delta" && streamEvent.Delta.Text != "" {
				response.OutputText += streamEvent.Delta.Text
				textSlot := ensureResponseOutputTextSlot(&response)
				textSlot.Text += streamEvent.Delta.Text
				return write("response.output_text.delta", responseEventPayload("response.output_text.delta", response, streamEvent.Delta.Text, ""))
			}
			if streamEvent.Delta.Type == "input_json_delta" && streamEvent.Delta.PartialJSON != "" {
				outputIndex, found := toolOutputs[streamEvent.Index]
				if !found || outputIndex >= len(response.Output) {
					return nil
				}
				response.Output[outputIndex].Arguments += streamEvent.Delta.PartialJSON
				if structured {
					response.OutputText += streamEvent.Delta.PartialJSON
					return write("response.output_text.delta", responseEventPayload("response.output_text.delta", response, streamEvent.Delta.PartialJSON, ""))
				}
				return write("response.function_call_arguments.delta", responseOutputItemEvent("response.function_call_arguments.delta", outputIndex, response.Output[outputIndex], streamEvent.Delta.PartialJSON))
			}
		case "content_block_stop":
			if outputIndex, found := toolOutputs[streamEvent.Index]; found && outputIndex < len(response.Output) {
				response.Output[outputIndex].Status = "completed"
				if !structured {
					return write("response.output_item.done", responseOutputItemEvent("response.output_item.done", outputIndex, response.Output[outputIndex], ""))
				}
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
	if structured {
		response = anthropicStructuredResponse(response)
	}
	if response.OutputText == "" {
		response.OutputText = responseText(response)
	}
	if err := write("response.completed", responseEventPayload("response.completed", response, "", "completed")); err != nil {
		return openai.ResponseResponse{}, err
	}
	return response, nil
}

func responseOutputItemEvent(eventType string, outputIndex int, item openai.ResponseOutputItem, delta string) string {
	payload := map[string]any{"type": eventType, "output_index": outputIndex, "item": item}
	if delta != "" {
		payload["delta"] = delta
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

type anthropicStreamEvent struct {
	Type         string            `json:"type"`
	Index        int               `json:"index"`
	Message      anthropicResponse `json:"message"`
	ContentBlock anthropicContent  `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
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
