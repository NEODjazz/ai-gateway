package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

const defaultAnthropicMaxTokens = 1024

func (Anthropic) ReportsMatchedStop() bool { return true }

type Anthropic struct {
	baseURL        string
	apiKey         string
	upstreamStream bool
	client         *http.Client
}

type anthropicRequest struct {
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Model         string                 `json:"model"`
	System        any                    `json:"system,omitempty"`
	Messages      []anthropicMessage     `json:"messages"`
	Tools         []anthropicTool        `json:"tools,omitempty"`
	ToolChoice    map[string]any         `json:"tool_choice,omitempty"`
	MaxTokens     int                    `json:"max_tokens"`
	Stream        bool                   `json:"stream,omitempty"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
	ServiceTier   string                 `json:"service_tier,omitempty"`
	Metadata      *anthropicMetadata     `json:"metadata,omitempty"`
	OutputConfig  *anthropicOutputConfig `json:"output_config,omitempty"`
}

type anthropicMetadata struct {
	UserID string `json:"user_id"`
}

type anthropicOutputConfig struct {
	Effort string                     `json:"effort,omitempty"`
	Format *anthropicJSONOutputFormat `json:"format,omitempty"`
}

type anthropicJSONOutputFormat struct {
	Type   string `json:"type"`
	Schema any    `json:"schema"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicTool struct {
	Type             string                 `json:"type,omitempty"`
	Name             string                 `json:"name"`
	Description      string                 `json:"description,omitempty"`
	InputSchema      any                    `json:"input_schema,omitempty"`
	MaxUses          int                    `json:"max_uses,omitempty"`
	UserLocation     *anthropicUserLocation `json:"user_location,omitempty"`
	AllowedDomains   []string               `json:"allowed_domains,omitempty"`
	Citations        *anthropicCitations    `json:"citations,omitempty"`
	MaxContentTokens int                    `json:"max_content_tokens,omitempty"`
	CacheControl     *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicCitations struct {
	Enabled bool `json:"enabled"`
}

type anthropicUserLocation struct {
	Type     string `json:"type"`
	City     string `json:"city,omitempty"`
	Country  string `json:"country,omitempty"`
	Region   string `json:"region,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type anthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Model        string             `json:"model"`
	Content      []anthropicContent `json:"content"`
	StopReason   string             `json:"stop_reason"`
	StopSequence *string            `json:"stop_sequence"`
	Usage        anthropicUsage     `json:"usage"`
}

type anthropicContent struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	Source       any                    `json:"source,omitempty"`
	ID           string                 `json:"id,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Input        any                    `json:"input,omitempty"`
	ToolUseID    string                 `json:"tool_use_id,omitempty"`
	Content      any                    `json:"content,omitempty"`
	Citations    []anthropicCitation    `json:"citations,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
	Thinking     string                 `json:"thinking,omitempty"`
	Signature    string                 `json:"signature,omitempty"`
	Data         string                 `json:"data,omitempty"`
	Raw          json.RawMessage        `json:"-"`
}

func (c *anthropicContent) UnmarshalJSON(data []byte) error {
	type content anthropicContent
	var decoded content
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = anthropicContent(decoded)
	c.Raw = append(c.Raw[:0], data...)
	return nil
}

type anthropicCitation struct {
	Type          string `json:"type"`
	URL           string `json:"url"`
	Title         string `json:"title"`
	DocumentTitle string `json:"document_title"`
	DocumentIndex int    `json:"document_index"`
	CitedText     string `json:"cited_text"`
}

type anthropicUsage struct {
	InputTokens              int                          `json:"input_tokens"`
	OutputTokens             int                          `json:"output_tokens"`
	CacheReadInputTokens     int                          `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int                          `json:"cache_creation_input_tokens,omitempty"`
	OutputTokensDetails      *anthropicOutputTokenDetails `json:"output_tokens_details,omitempty"`
	ServerToolUse            *anthropicServerToolUsage    `json:"server_tool_use,omitempty"`
	ServiceTier              string                       `json:"service_tier,omitempty"`
}

type anthropicServerToolUsage struct {
	WebSearchRequests int `json:"web_search_requests"`
	WebFetchRequests  int `json:"web_fetch_requests"`
}

type anthropicOutputTokenDetails struct {
	ThinkingTokens int `json:"thinking_tokens"`
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

func (Anthropic) SupportsVision() bool          { return true }
func (Anthropic) SupportsReasoningBlocks() bool { return true }
func (Anthropic) SupportsWebFetch() bool        { return true }

func (p Anthropic) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	upstreamRequest := anthropicChatRequest(request, false)
	var response anthropicResponse
	if err := p.doMessages(ctx, upstreamRequest, &response); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if err := validateAnthropicUsage(response.Usage); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if err := validateAnthropicRequestedToolUsage(response.Usage, request.WebSearchOptions, request.WebFetchOptions); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if err := validateAnthropicFetchContent(response.Content, request.WebFetchOptions); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if _, err := anthropicAnnotations(response); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if err := validateAnthropicNativeMessageContent(response.Content); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	converted := anthropicToChatCompletion(response, request.Model)
	if err := openai.ValidateReasoningBlocks(converted.Choices[0].Message.Reasoning); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if response.StopReason == "stop_sequence" && response.StopSequence == nil {
		return openai.ChatCompletionResponse{}, errors.New("Anthropic omitted matched stop sequence")
	}
	if anthropicUsesStructuredTool(request.ResponseFormat) {
		converted = anthropicStructuredChat(converted)
	}
	return converted, nil
}

func (p Anthropic) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if !p.upstreamStream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}

	resp, err := p.doMessagesStream(ctx, anthropicChatRequest(request, true))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	return streamAnthropicChat(resp.Body, request.Model, anthropicUsesStructuredTool(request.ResponseFormat), request.WebSearchOptions, request.WebFetchOptions, write)
}

func (p Anthropic) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if err := p.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	upstreamRequest, err := anthropicResponsesRequest(request, false)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
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
	if err := p.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	if !p.upstreamStream {
		return openai.ResponseResponse{}, ErrStreamingUnsupported
	}

	upstreamRequest, err := anthropicResponsesRequest(request, true)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	resp, err := p.doMessagesStream(ctx, upstreamRequest)
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
	if request.WebSearchOptions != nil {
		tools = append(tools, anthropicWebSearchTool(request.WebSearchOptions))
	}
	if request.WebFetchOptions != nil {
		tools = append(tools, anthropicWebFetchTool(request.WebFetchOptions))
	}
	var outputConfig *anthropicOutputConfig
	if request.ResponseFormat != nil && request.ResponseFormat.Type == "json_schema" {
		outputConfig = &anthropicOutputConfig{}
		if request.ResponseFormat.JSONSchema != nil {
			outputConfig.Format = &anthropicJSONOutputFormat{Type: "json_schema", Schema: request.ResponseFormat.JSONSchema.Schema}
		}
	} else if request.ResponseFormat != nil {
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
	if request.ReasoningEffort != "" {
		if outputConfig == nil {
			outputConfig = &anthropicOutputConfig{}
		}
		outputConfig.Effort = request.ReasoningEffort
	}
	var metadata *anthropicMetadata
	if userID := request.Metadata["user_id"]; userID != "" {
		metadata = &anthropicMetadata{UserID: userID}
	}
	stop, _ := openai.StopSequences(request.Stop)
	return anthropicRequest{
		StopSequences: stop,
		Model:         request.Model,
		System:        system,
		Messages:      messages,
		Tools:         tools,
		ToolChoice:    anthropicParallelChoice(toolChoice, request.ParallelToolCalls),
		MaxTokens:     requestMaxTokens(request.MaxTokens, request.MaxCompletionTokens),
		Stream:        stream,
		Temperature:   request.Temperature,
		TopP:          request.TopP,
		ServiceTier:   request.ServiceTier,
		Metadata:      metadata,
		OutputConfig:  outputConfig,
	}
}

func anthropicWebFetchTool(options *openai.ChatWebFetchOptions) anthropicTool {
	maxUses := openai.WebFetchMaxUses
	if options.MaxUses != nil {
		maxUses = *options.MaxUses
	}
	return anthropicTool{Type: "web_fetch_20250910", Name: "web_fetch", MaxUses: maxUses, AllowedDomains: append([]string(nil), options.AllowedDomains...), Citations: &anthropicCitations{Enabled: true}, MaxContentTokens: options.MaxContentTokens}
}

func anthropicWebSearchTool(options *openai.ChatWebSearchOptions) anthropicTool {
	tool := anthropicTool{Type: "web_search_20250305", Name: "web_search", MaxUses: openai.WebSearchMaxUses}
	if options != nil && options.MaxUses != nil {
		tool.MaxUses = *options.MaxUses
	}
	if options == nil || options.UserLocation == nil || options.UserLocation.Approximate == nil {
		return tool
	}
	location := options.UserLocation.Approximate
	tool.UserLocation = &anthropicUserLocation{
		Type: "approximate", City: location.City, Country: location.Country,
		Region: location.Region, Timezone: location.Timezone,
	}
	return tool
}

func anthropicUsesStructuredTool(format *openai.ResponseFormat) bool {
	return format != nil && format.Type != "json_schema"
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

func anthropicResponsesRequest(request openai.ResponseRequest, stream bool) (anthropicRequest, error) {
	maxTokens := requestMaxTokens(request.MaxTokens, request.MaxOutputTokens)
	messages, err := anthropicResponseMessages(request.Input)
	if err != nil {
		return anthropicRequest{}, err
	}
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
		ToolChoice:  anthropicParallelChoice(toolChoice, request.ParallelToolCalls),
		MaxTokens:   maxTokens,
		Stream:      stream,
		Temperature: request.Temperature,
		TopP:        request.TopP,
	}, nil
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

func anthropicMessages(messages []openai.Message) (any, []anthropicMessage) {
	var systemText []string
	var systemBlocks []anthropicContent
	structuredSystem := false
	converted := make([]anthropicMessage, 0, len(messages))
	for _, message := range messages {
		content := openai.ContentText(message.Content)
		switch message.Role {
		case "system", "developer":
			if content != "" {
				systemText = append(systemText, content)
			}
			blocks := anthropicContentBlocks(message.Content)
			systemBlocks = append(systemBlocks, blocks...)
			structuredSystem = structuredSystem || anthropicBlocksUseCache(blocks)
		case "assistant":
			blocks := anthropicReasoningBlocks(message.Reasoning)
			blocks = append(blocks, anthropicContentBlocks(message.Content)...)
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
	if structuredSystem {
		return systemBlocks, converted
	}
	return strings.Join(systemText, "\n\n"), converted
}

func anthropicReasoningBlocks(blocks []openai.ReasoningBlock) []anthropicContent {
	result := make([]anthropicContent, 0, len(blocks))
	for _, block := range blocks {
		result = append(result, anthropicContent{Type: block.Type, Thinking: block.Thinking, Signature: block.Signature, Data: block.Data})
	}
	return result
}

func anthropicContentBlocks(value any) []anthropicContent {
	converted := anthropicMessageContent(value)
	if blocks, ok := converted.([]anthropicContent); ok {
		return blocks
	}
	if text := openai.ContentText(converted); text != "" {
		return []anthropicContent{{Type: "text", Text: text}}
	}
	return nil
}

func anthropicBlocksUseCache(blocks []anthropicContent) bool {
	for _, block := range blocks {
		if block.CacheControl != nil {
			return true
		}
	}
	return false
}

func anthropicResponseMessages(input any) ([]anthropicMessage, error) {
	if items, ok := input.([]any); ok {
		messages := make([]anthropicMessage, 0, len(items))
		for _, item := range items {
			object, ok := item.(map[string]any)
			if !ok {
				messages = nil
				break
			}
			if kind, _ := object["type"].(string); kind == "function_call" || kind == "function_call_output" {
				message, err := anthropicResponseToolMessage(object)
				if err != nil {
					return nil, err
				}
				messages = appendAnthropicResponseMessage(messages, message)
				continue
			}
			role, _ := object["role"].(string)
			if role != "user" && role != "assistant" {
				messages = nil
				break
			}
			messages = appendAnthropicResponseMessage(messages, anthropicMessage{Role: role, Content: anthropicMessageContent(object["content"])})
		}
		if len(messages) > 0 {
			return orderAnthropicToolResults(messages), nil
		}
	}
	return []anthropicMessage{{Role: "user", Content: anthropicMessageContent(input)}}, nil
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
				blocks = append(blocks, anthropicContent{Type: "text", Text: text, CacheControl: anthropicContentCacheControl(object)})
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

func anthropicContentCacheControl(object map[string]any) *anthropicCacheControl {
	value, _ := object["prompt_cache_breakpoint"].(map[string]any)
	if value == nil || value["mode"] != "explicit" {
		return nil
	}
	ttl, _ := value["ttl"].(string)
	return &anthropicCacheControl{Type: "ephemeral", TTL: ttl}
}

func anthropicToolCacheControl(value *openai.PromptCacheBreakpoint) *anthropicCacheControl {
	if !openai.ValidPromptCacheBreakpoint(value) {
		return nil
	}
	return &anthropicCacheControl{Type: "ephemeral", TTL: value.TTL}
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
		converted = append(converted, anthropicTool{Name: tool.Function.Name, Description: tool.Function.Description, InputSchema: schema, CacheControl: anthropicToolCacheControl(tool.Function.PromptCacheBreakpoint)})
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
	annotations, _ := anthropicAnnotations(response)
	toolCalls := anthropicToolCalls(response)
	inputTokens := anthropicInputTokens(response.Usage)
	return openai.ChatCompletionResponse{
		ID:          response.ID,
		Object:      "chat.completion",
		Model:       model,
		ServiceTier: response.Usage.ServiceTier,
		Choices: []openai.Choice{
			{
				Index:        0,
				Message:      openai.Message{Role: "assistant", Content: content, ToolCalls: toolCalls, Annotations: annotations, Reasoning: anthropicReasoning(response), NativeContent: anthropicNativeMessageContent(response.Content)},
				FinishReason: anthropicFinishReason(response.StopReason),
				StopSequence: anthropicMatchedStop(response.StopReason, response.StopSequence),
			},
		},
		Usage: openai.Usage{
			SearchRequests:   anthropicSearchRequests(response.Usage),
			PromptTokens:     inputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      inputTokens + response.Usage.OutputTokens,
			PromptTokensDetails: &openai.PromptTokenDetails{
				CachedTokens:     response.Usage.CacheReadInputTokens,
				CacheWriteTokens: response.Usage.CacheCreationInputTokens,
			},
			CompletionTokensDetails: anthropicCompletionTokenDetails(response.Usage),
		},
	}
}

func anthropicNativeMessageContent(content []anthropicContent) []json.RawMessage {
	native := false
	for _, block := range content {
		if block.Type == "server_tool_use" || block.Type == "web_search_tool_result" || block.Type == "web_fetch_tool_result" {
			native = true
			break
		}
	}
	if !native || len(content) > 128 {
		return nil
	}
	result := make([]json.RawMessage, 0, len(content))
	total := 0
	for _, block := range content {
		encoded, err := anthropicContentJSON(block)
		if err != nil || len(encoded) > 4<<20 || total > (32<<20)-len(encoded) {
			return nil
		}
		total += len(encoded)
		result = append(result, append(json.RawMessage(nil), encoded...))
	}
	return result
}

func validateAnthropicNativeMessageContent(content []anthropicContent) error {
	native := false
	for _, block := range content {
		if block.Type == "server_tool_use" || block.Type == "web_search_tool_result" || block.Type == "web_fetch_tool_result" {
			native = true
		}
	}
	if !native {
		return nil
	}
	if len(content) > 128 {
		return errors.New("Anthropic returned too many content blocks")
	}
	total := 0
	for _, block := range content {
		switch block.Type {
		case "text", "thinking", "redacted_thinking", "tool_use", "server_tool_use", "web_search_tool_result", "web_fetch_tool_result":
		default:
			return errors.New("Anthropic returned unsupported native content")
		}
		encoded, err := anthropicContentJSON(block)
		if err != nil || len(encoded) > 4<<20 || total > (32<<20)-len(encoded) {
			return errors.New("Anthropic native content exceeds limit")
		}
		total += len(encoded)
	}
	return nil
}

func anthropicContentJSON(block anthropicContent) ([]byte, error) {
	if len(block.Raw) > 0 {
		if !json.Valid(block.Raw) {
			return nil, errors.New("invalid Anthropic content block")
		}
		return block.Raw, nil
	}
	return json.Marshal(block)
}

func anthropicReasoning(response anthropicResponse) []openai.ReasoningBlock {
	var result []openai.ReasoningBlock
	for _, block := range response.Content {
		if block.Type == "thinking" || block.Type == "redacted_thinking" {
			result = append(result, openai.ReasoningBlock{Type: block.Type, Thinking: block.Thinking, Signature: block.Signature, Data: block.Data})
		}
	}
	return result
}

func anthropicCompletionTokenDetails(usage anthropicUsage) *openai.CompletionTokenDetails {
	if usage.OutputTokensDetails == nil {
		return nil
	}
	return &openai.CompletionTokenDetails{ReasoningTokens: usage.OutputTokensDetails.ThinkingTokens}
}

func validateAnthropicUsage(usage anthropicUsage) error {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheReadInputTokens < 0 || usage.CacheCreationInputTokens < 0 {
		return errors.New("invalid Anthropic usage")
	}
	if details := usage.OutputTokensDetails; details != nil && (details.ThinkingTokens < 0 || details.ThinkingTokens > usage.OutputTokens) {
		return errors.New("invalid Anthropic output token details")
	}
	if searches := anthropicSearchRequests(usage); searches < 0 || searches > openai.WebSearchMaxUses {
		return errors.New("invalid Anthropic server tool usage")
	}
	if usage.ServerToolUse != nil && (usage.ServerToolUse.WebFetchRequests < 0 || usage.ServerToolUse.WebFetchRequests > openai.WebFetchMaxUses) {
		return errors.New("invalid Anthropic server tool usage")
	}
	if usage.ServiceTier != "" && usage.ServiceTier != "standard" && usage.ServiceTier != "priority" && usage.ServiceTier != "batch" {
		return errors.New("invalid Anthropic service tier")
	}
	return nil
}

func validateAnthropicRequestedToolUsage(usage anthropicUsage, search *openai.ChatWebSearchOptions, fetch *openai.ChatWebFetchOptions) error {
	searchLimit := openai.WebSearchMaxUses
	if search != nil && search.MaxUses != nil {
		searchLimit = *search.MaxUses
	}
	fetchLimit := openai.WebFetchMaxUses
	if fetch != nil && fetch.MaxUses != nil {
		fetchLimit = *fetch.MaxUses
	}
	if anthropicSearchRequests(usage) > searchLimit || (usage.ServerToolUse != nil && usage.ServerToolUse.WebFetchRequests > fetchLimit) {
		return errors.New("Anthropic exceeded requested server tool usage")
	}
	return nil
}

func anthropicSearchRequests(usage anthropicUsage) int {
	if usage.ServerToolUse == nil {
		return 0
	}
	return usage.ServerToolUse.WebSearchRequests
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

func anthropicAnnotations(response anthropicResponse) ([]openai.ChatAnnotation, error) {
	annotations := make([]openai.ChatAnnotation, 0)
	fetchURLs := anthropicFetchURLs(response.Content)
	offset := 0
	for _, content := range response.Content {
		if content.Type != "text" {
			continue
		}
		for _, citation := range content.Citations {
			annotation, err := anthropicCitationAnnotation(citation, content.Text, offset, fetchURLs)
			if err != nil {
				return nil, err
			}
			annotations = append(annotations, annotation)
		}
		offset += utf8.RuneCountInString(content.Text)
	}
	if err := openai.ValidateChatAnnotations(annotations); err != nil {
		return nil, err
	}
	return annotations, nil
}

func anthropicCitationAnnotation(citation anthropicCitation, text string, offset int, fetchURLs []string) (openai.ChatAnnotation, error) {
	if citation.Type == "char_location" {
		if citation.DocumentIndex < 0 || citation.DocumentIndex >= len(fetchURLs) {
			return openai.ChatAnnotation{}, errors.New("Anthropic fetch citation has no source URL")
		}
		citation.URL = fetchURLs[citation.DocumentIndex]
		citation.Title = citation.DocumentTitle
	} else if citation.Type != "web_search_result_location" {
		return openai.ChatAnnotation{}, errors.New("unsupported Anthropic citation")
	}
	start, end := 0, utf8.RuneCountInString(text)
	if citation.CitedText != "" {
		index := strings.Index(text, citation.CitedText)
		if index < 0 {
			return openai.ChatAnnotation{}, errors.New("Anthropic citation text is absent from its content block")
		}
		start = utf8.RuneCountInString(text[:index])
		end = start + utf8.RuneCountInString(citation.CitedText)
	}
	return openai.ChatAnnotation{Type: "url_citation", URLCitation: &openai.ChatURLCitation{
		StartIndex: offset + start, EndIndex: offset + end, Title: citation.Title, URL: citation.URL,
	}}, nil
}

func anthropicFetchURLs(content []anthropicContent) []string {
	var urls []string
	for _, block := range content {
		if block.Type != "web_fetch_tool_result" {
			continue
		}
		result, ok := block.Content.(map[string]any)
		if !ok || result["type"] != "web_fetch_result" {
			continue
		}
		value, _ := result["url"].(string)
		if value != "" {
			urls = append(urls, value)
		}
	}
	return urls
}

func validateAnthropicFetchContent(content []anthropicContent, options *openai.ChatWebFetchOptions) error {
	if options == nil {
		return nil
	}
	for _, value := range anthropicFetchURLs(content) {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || len(value) > 8192 {
			return errors.New("Anthropic returned an invalid web fetch URL")
		}
		host := strings.ToLower(parsed.Hostname())
		allowed := false
		for _, domain := range options.AllowedDomains {
			domain = strings.ToLower(domain)
			if host == domain || strings.HasSuffix(host, "."+domain) {
				allowed = true
				break
			}
		}
		if !allowed {
			return errors.New("Anthropic returned a web fetch URL outside the allowed domains")
		}
	}
	return nil
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

func streamAnthropicChat(body io.Reader, fallbackModel string, structured bool, webSearch *openai.ChatWebSearchOptions, webFetch *openai.ChatWebFetchOptions, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	response := openai.ChatCompletionResponse{
		Object: "chat.completion",
		Model:  fallbackModel,
		Choices: []openai.Choice{
			{Index: 0, Message: openai.Message{Role: "assistant"}, FinishReason: "stop"},
		},
	}
	toolIndexes := map[int]int{}
	reasoningIndexes := map[int]int{}
	textBlockOffsets := map[int]int{}
	textBlockContents := map[int]string{}
	var fetchURLs []string
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
			if err := validateAnthropicUsage(streamEvent.Message.Usage); err != nil {
				return err
			}
			if err := validateAnthropicRequestedToolUsage(streamEvent.Message.Usage, webSearch, webFetch); err != nil {
				return err
			}
			response.ID = streamEvent.Message.ID
			if streamEvent.Message.Model != "" {
				response.Model = streamEvent.Message.Model
			}
			response.Usage.PromptTokens = anthropicInputTokens(streamEvent.Message.Usage)
			response.Usage.SearchRequests = anthropicSearchRequests(streamEvent.Message.Usage)
			response.Usage.PromptTokensDetails = &openai.PromptTokenDetails{CachedTokens: streamEvent.Message.Usage.CacheReadInputTokens, CacheWriteTokens: streamEvent.Message.Usage.CacheCreationInputTokens}
			response.ServiceTier = streamEvent.Message.Usage.ServiceTier
			if response.ServiceTier != "" {
				if err := write(openAIChatServiceTierChunkPayload(response.ID, response.Model, response.ServiceTier)); err != nil {
					return err
				}
			}
		case "content_block_start":
			if streamEvent.ContentBlock.Type == "web_fetch_tool_result" {
				if err := validateAnthropicFetchContent([]anthropicContent{streamEvent.ContentBlock}, webFetch); err != nil {
					return err
				}
				fetchURLs = append(fetchURLs, anthropicFetchURLs([]anthropicContent{streamEvent.ContentBlock})...)
			}
			if streamEvent.ContentBlock.Type == "thinking" || streamEvent.ContentBlock.Type == "redacted_thinking" {
				reasoningIndex := len(response.Choices[0].Message.Reasoning)
				reasoningIndexes[streamEvent.Index] = reasoningIndex
				index := reasoningIndex
				block := openai.ReasoningBlock{Index: &index, Type: streamEvent.ContentBlock.Type, Thinking: streamEvent.ContentBlock.Thinking, Signature: streamEvent.ContentBlock.Signature, Data: streamEvent.ContentBlock.Data}
				response.Choices[0].Message.Reasoning = append(response.Choices[0].Message.Reasoning, block)
				if err := write(openAIChatReasoningChunkPayload(response.ID, response.Model, block)); err != nil {
					return err
				}
			}
			if streamEvent.ContentBlock.Type == "text" {
				textBlockOffsets[streamEvent.Index] = utf8.RuneCountInString(openai.ContentText(response.Choices[0].Message.Content))
				textBlockContents[streamEvent.Index] = ""
			}
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
			if streamEvent.Delta.Type == "thinking_delta" || streamEvent.Delta.Type == "signature_delta" {
				reasoningIndex, found := reasoningIndexes[streamEvent.Index]
				if !found || reasoningIndex >= len(response.Choices[0].Message.Reasoning) {
					return errors.New("Anthropic reasoning delta has no content block")
				}
				stored := &response.Choices[0].Message.Reasoning[reasoningIndex]
				index := reasoningIndex
				delta := openai.ReasoningBlock{Index: &index, Type: stored.Type, Thinking: streamEvent.Delta.Thinking, Signature: streamEvent.Delta.Signature}
				stored.Thinking += streamEvent.Delta.Thinking
				stored.Signature += streamEvent.Delta.Signature
				return write(openAIChatReasoningChunkPayload(response.ID, response.Model, delta))
			}
			if streamEvent.Delta.Type == "text_delta" && streamEvent.Delta.Text != "" {
				response.Choices[0].Message.Content = openai.ContentText(response.Choices[0].Message.Content) + streamEvent.Delta.Text
				textBlockContents[streamEvent.Index] += streamEvent.Delta.Text
				if err := write(openAIChatCompletionChunkPayload(response.ID, response.Model, 0, "assistant", streamEvent.Delta.Text, nil)); err != nil {
					return err
				}
			}
			if streamEvent.Delta.Type == "citations_delta" {
				annotation, err := anthropicCitationAnnotation(streamEvent.Delta.Citation, textBlockContents[streamEvent.Index], textBlockOffsets[streamEvent.Index], fetchURLs)
				if err != nil {
					return err
				}
				response.Choices[0].Message.Annotations = append(response.Choices[0].Message.Annotations, annotation)
				if err := openai.ValidateChatAnnotations(response.Choices[0].Message.Annotations); err != nil {
					return err
				}
				return write(openAIChatAnnotationChunkPayload(response.ID, response.Model, annotation))
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
			if err := validateAnthropicUsage(streamEvent.Usage); err != nil {
				return err
			}
			if err := validateAnthropicRequestedToolUsage(streamEvent.Usage, webSearch, webFetch); err != nil {
				return err
			}
			if streamEvent.Usage.OutputTokens != 0 {
				response.Usage.CompletionTokens = streamEvent.Usage.OutputTokens
				response.Usage.TotalTokens = response.Usage.PromptTokens + response.Usage.CompletionTokens
			}
			if streamEvent.Usage.OutputTokensDetails != nil {
				response.Usage.CompletionTokensDetails = anthropicCompletionTokenDetails(streamEvent.Usage)
			}
			if streamEvent.Usage.ServerToolUse != nil {
				response.Usage.SearchRequests = anthropicSearchRequests(streamEvent.Usage)
			}
			if streamEvent.Usage.ServiceTier != "" {
				if response.ServiceTier != "" && response.ServiceTier != streamEvent.Usage.ServiceTier {
					return errors.New("Anthropic changed service tier during stream")
				}
				if response.ServiceTier == "" {
					response.ServiceTier = streamEvent.Usage.ServiceTier
					if err := write(openAIChatServiceTierChunkPayload(response.ID, response.Model, response.ServiceTier)); err != nil {
						return err
					}
				}
			}
			if streamEvent.Delta.StopReason != "" {
				if streamEvent.Delta.StopReason == "stop_sequence" && streamEvent.Delta.StopSequence == nil {
					return errors.New("Anthropic omitted matched stop sequence")
				}
				finishReason := anthropicFinishReason(streamEvent.Delta.StopReason)
				response.Choices[0].FinishReason = finishReason
				response.Choices[0].StopSequence = anthropicMatchedStop(streamEvent.Delta.StopReason, streamEvent.Delta.StopSequence)
				return write(openAIChatCompletionChunkPayload(response.ID, response.Model, 0, "", "", &finishReason, response.Choices[0].StopSequence))
			}
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return openai.ChatCompletionResponse{}, err
	}
	if err := openai.ValidateReasoningBlocks(response.Choices[0].Message.Reasoning); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if structured {
		response = anthropicStructuredChat(response)
	}
	return response, nil
}

func openAIChatReasoningChunkPayload(id, model string, block openai.ReasoningBlock) string {
	payload, err := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().UTC().Unix(), "model": model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"reasoning": []openai.ReasoningBlock{block}}, "finish_reason": nil}},
	})
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func openAIChatServiceTierChunkPayload(id, model, serviceTier string) string {
	payload, err := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().UTC().Unix(), "model": model, "service_tier": serviceTier,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}},
	})
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func openAIChatAnnotationChunkPayload(id, model string, annotation openai.ChatAnnotation) string {
	payload, err := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().UTC().Unix(), "model": model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"annotations": []openai.ChatAnnotation{annotation}}, "finish_reason": nil}},
	})
	if err != nil {
		return "{}"
	}
	return string(payload)
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
		Type         string            `json:"type"`
		Text         string            `json:"text"`
		Thinking     string            `json:"thinking"`
		Signature    string            `json:"signature"`
		PartialJSON  string            `json:"partial_json"`
		StopReason   string            `json:"stop_reason"`
		StopSequence *string           `json:"stop_sequence"`
		Citation     anthropicCitation `json:"citation"`
	} `json:"delta"`
	Usage anthropicUsage `json:"usage"`
}

func anthropicMatchedStop(reason string, sequence *string) *string {
	if reason == "stop_sequence" {
		return sequence
	}
	return nil
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

func anthropicParallelChoice(choice map[string]any, parallel *bool) map[string]any {
	if choice != nil && parallel != nil {
		choice["disable_parallel_tool_use"] = !*parallel
	}
	return choice
}
