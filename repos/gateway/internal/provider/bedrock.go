package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

type Bedrock struct {
	baseURL  string
	apiKey   string
	authType string
	region   string
	client   *http.Client
	now      func() time.Time
	aws      *awsCredentialSource
}

type bedrockContentBlock struct {
	Text             string                   `json:"text,omitempty"`
	Image            *bedrockImage            `json:"image,omitempty"`
	Document         *bedrockDocument         `json:"document,omitempty"`
	ToolUse          *bedrockToolUse          `json:"toolUse,omitempty"`
	ToolResult       *bedrockToolResult       `json:"toolResult,omitempty"`
	CitationsContent *bedrockCitationsContent `json:"citationsContent,omitempty"`
	ReasoningContent *bedrockReasoningContent `json:"reasoningContent,omitempty"`
}

type bedrockReasoningContent struct {
	ReasoningText   *bedrockReasoningText `json:"reasoningText,omitempty"`
	RedactedContent string                `json:"redactedContent,omitempty"`
}

type bedrockReasoningText struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

type bedrockCitationsContent struct {
	Content   []bedrockCitationText `json:"content"`
	Citations []bedrockCitation     `json:"citations"`
}

type bedrockCitationText struct {
	Text *string `json:"text,omitempty"`
}

type bedrockCitation struct {
	Title         string                         `json:"title,omitempty"`
	Source        string                         `json:"source,omitempty"`
	SourceContent []bedrockCitationSourceContent `json:"sourceContent,omitempty"`
	Location      bedrockCitationLocation        `json:"location"`
}

type bedrockCitationSourceContent struct {
	Text *string `json:"text,omitempty"`
}

type bedrockCitationLocation struct {
	DocumentChar         *bedrockDocumentLocation     `json:"documentChar,omitempty"`
	DocumentChunk        *bedrockDocumentLocation     `json:"documentChunk,omitempty"`
	DocumentPage         *bedrockDocumentLocation     `json:"documentPage,omitempty"`
	SearchResultLocation *bedrockSearchResultLocation `json:"searchResultLocation,omitempty"`
	Web                  *bedrockWebLocation          `json:"web,omitempty"`
}

type bedrockDocumentLocation struct {
	DocumentIndex *int `json:"documentIndex,omitempty"`
	Start         *int `json:"start,omitempty"`
	End           *int `json:"end,omitempty"`
}

type bedrockSearchResultLocation struct {
	SearchResultIndex *int `json:"searchResultIndex,omitempty"`
	Start             *int `json:"start,omitempty"`
	End               *int `json:"end,omitempty"`
}

type bedrockWebLocation struct {
	Domain string `json:"domain,omitempty"`
	URL    string `json:"url,omitempty"`
}

type bedrockDocument struct {
	Format string                `json:"format,omitempty"`
	Name   string                `json:"name"`
	Source bedrockDocumentSource `json:"source"`
}

type bedrockDocumentSource struct {
	Bytes string `json:"bytes"`
}

type bedrockImage struct {
	Format string `json:"format"`
	Source struct {
		Bytes string `json:"bytes"`
	} `json:"source"`
}

type bedrockToolUse struct {
	ID    string `json:"toolUseId"`
	Name  string `json:"name"`
	Input any    `json:"input"`
}

type bedrockToolResult struct {
	ID      string                `json:"toolUseId"`
	Content []bedrockContentBlock `json:"content"`
}

type bedrockMessage struct {
	Role    string                `json:"role"`
	Content []bedrockContentBlock `json:"content"`
}

type bedrockToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema struct {
		JSON any `json:"json"`
	} `json:"inputSchema"`
}

type bedrockTool struct {
	Spec bedrockToolSpec `json:"toolSpec"`
}

type bedrockRequest struct {
	Messages                          []bedrockMessage               `json:"messages"`
	System                            []bedrockContentBlock          `json:"system,omitempty"`
	InferenceConfig                   bedrockInferenceConfig         `json:"inferenceConfig,omitempty"`
	ServiceTier                       *bedrockServiceTier            `json:"serviceTier,omitempty"`
	PerformanceConfig                 *bedrockPerformanceConfig      `json:"performanceConfig,omitempty"`
	OutputConfig                      *bedrockOutputConfig           `json:"outputConfig,omitempty"`
	GuardrailConfig                   *openai.BedrockGuardrailConfig `json:"guardrailConfig,omitempty"`
	AdditionalModelRequestFields      json.RawMessage                `json:"additionalModelRequestFields,omitempty"`
	AdditionalModelResponseFieldPaths []string                       `json:"additionalModelResponseFieldPaths,omitempty"`
	RequestMetadata                   map[string]string              `json:"requestMetadata,omitempty"`
	ToolConfig                        *bedrockToolConfig             `json:"toolConfig,omitempty"`
}

type bedrockToolConfig struct {
	Tools      []bedrockTool      `json:"tools"`
	ToolChoice *bedrockToolChoice `json:"toolChoice,omitempty"`
}

type bedrockToolChoice struct {
	Auto *struct{}                  `json:"auto,omitempty"`
	Any  *struct{}                  `json:"any,omitempty"`
	Tool *bedrockSpecificToolChoice `json:"tool,omitempty"`
}

type bedrockSpecificToolChoice struct {
	Name string `json:"name"`
}

type bedrockServiceTier struct {
	Type string `json:"type"`
}

type bedrockPerformanceConfig struct {
	Latency string `json:"latency"`
}

type bedrockOutputConfig struct {
	TextFormat bedrockOutputFormat `json:"textFormat"`
}

type bedrockOutputFormat struct {
	Type      string                       `json:"type"`
	Structure bedrockOutputFormatStructure `json:"structure"`
}

type bedrockOutputFormatStructure struct {
	JSONSchema bedrockJSONSchemaDefinition `json:"jsonSchema"`
}

type bedrockJSONSchemaDefinition struct {
	Schema      string `json:"schema"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type bedrockInferenceConfig struct {
	MaxTokens     *int     `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

type bedrockResponse struct {
	Output struct {
		Message bedrockMessage `json:"message"`
	} `json:"output"`
	StopReason                    string          `json:"stopReason"`
	AdditionalModelResponseFields json.RawMessage `json:"additionalModelResponseFields,omitempty"`
	Trace                         json.RawMessage `json:"trace,omitempty"`
	Usage                         *struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

func NewBedrock(baseURL, apiKey string) Bedrock {
	return NewBedrockWithAuth(baseURL, apiKey, "bearer", "")
}

func NewBedrockWithAuth(baseURL, credential, authType, region string) Bedrock {
	client := newProviderHTTPClient(180 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	authType = strings.ToLower(strings.TrimSpace(authType))
	if authType == "" || authType == "api_key" {
		authType = "bearer"
	}
	region = strings.ToLower(strings.TrimSpace(region))
	return Bedrock{baseURL: strings.TrimRight(baseURL, "/"), apiKey: credential, authType: authType, region: region, client: client, now: time.Now, aws: newAWSCredentialSource(credential, region)}
}

func (Bedrock) SupportsResponses() bool             { return false }
func (Bedrock) SupportsTools() bool                 { return true }
func (Bedrock) SupportsVision() bool                { return true }
func (Bedrock) SupportsBedrockNativeControls() bool { return true }
func (Bedrock) SupportsReasoningBlocks() bool       { return true }

func bedrockInvalid(param string) error {
	return &Error{Class: FailureClientRequest, Provider: "bedrock", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: param, Err: fmt.Errorf("unsupported or invalid %s for Bedrock adapter", param)}
}

func (b Bedrock) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := validateChatReasoningContent("bedrock", request.Messages, false); err != nil {
		return err
	}
	_, err := bedrockChatRequest(request)
	return err
}

func bedrockChatRequest(request openai.ChatCompletionRequest) (bedrockRequest, error) {
	var result bedrockRequest
	if err := openai.ValidateBedrockResponseFieldPaths(request.BedrockAdditionalModelResponseFieldPaths); err != nil {
		return result, bedrockInvalid("additional_model_response_field_paths")
	}
	if err := openai.ValidateBedrockAdditionalModelRequestFields(request.BedrockAdditionalModelRequestFields); err != nil {
		return result, bedrockInvalid("additional_model_request_fields")
	}
	if err := openai.ValidateBedrockGuardrailConfig(request.BedrockGuardrailConfig); err != nil {
		return result, bedrockInvalid("guardrail_config")
	}
	if err := openai.ValidateBedrockRequestMetadata(request.BedrockRequestMetadata); err != nil {
		return result, bedrockInvalid("request_metadata")
	}
	if strings.TrimSpace(request.Model) == "" {
		return result, bedrockInvalid("model")
	}
	if len(request.Messages) == 0 {
		return result, bedrockInvalid("messages")
	}
	if err := validateChatMessagePrefix("bedrock", request.Messages, false); err != nil {
		return result, err
	}
	if err := rejectLegacyFunctionCalling("bedrock", request); err != nil {
		return result, err
	}
	if err := validateChatPromptCacheBreakpoints("bedrock", request, false); err != nil {
		return result, err
	}
	if err := rejectChatMessageRefusals("bedrock", request.Messages); err != nil {
		return result, err
	}
	if err := rejectChatMessageAudio("bedrock", request.Messages); err != nil {
		return result, err
	}
	if _, err := openai.ChatImageAttachments(request.Messages); err != nil {
		return result, err
	}
	if _, err := openai.BedrockDocumentAttachments(request.Messages); err != nil {
		return result, err
	}
	if request.MaxTokens != nil && request.MaxCompletionTokens != nil {
		return result, bedrockInvalid("max_tokens")
	}
	maxTokens := request.MaxCompletionTokens
	if maxTokens == nil {
		maxTokens = request.MaxTokens
	}
	if maxTokens != nil && (*maxTokens <= 0 || *maxTokens > math.MaxInt32) {
		return result, bedrockInvalid("max_completion_tokens")
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 1) {
		return result, bedrockInvalid("temperature")
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return result, bedrockInvalid("top_p")
	}
	stop, valid := openai.StopSequences(request.Stop)
	if !valid {
		return result, bedrockInvalid("stop")
	}
	if err := rejectParameters("bedrock",
		parameterCheck{"stream_options", request.StreamOptions != nil && !request.Stream},
		parameterCheck{"parallel_tool_calls", request.ParallelToolCalls != nil},
		parameterCheck{"seed", request.Seed != nil},
		parameterCheck{"metadata", request.Metadata != nil}, parameterCheck{"store", request.Store != nil},
		parameterCheck{"modalities", request.Modalities != nil}, parameterCheck{"reasoning_effort", request.ReasoningEffort != ""},
		parameterCheck{"safe_prompt", request.SafePrompt != nil}, parameterCheck{"n", request.N != nil && *request.N != 1},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""}, parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""},
		parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil}, parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""},
		parameterCheck{"prompt_mode", request.PromptMode != ""}, parameterCheck{"prediction", request.Prediction != nil},
		parameterCheck{"user", request.User != ""},
		parameterCheck{"verbosity", request.Verbosity != ""}, parameterCheck{"web_search_options", request.WebSearchOptions != nil},
		parameterCheck{"web_fetch_options", request.WebFetchOptions != nil}, parameterCheck{"logprobs", request.Logprobs != nil},
		parameterCheck{"top_logprobs", request.TopLogprobs != nil}, parameterCheck{"frequency_penalty", request.FrequencyPenalty != nil},
		parameterCheck{"presence_penalty", request.PresencePenalty != nil}, parameterCheck{"logit_bias", request.LogitBias != nil},
		parameterCheck{"min_p", request.MinP != nil}, parameterCheck{"top_k", request.TopK != nil}, parameterCheck{"top_a", request.TopA != nil},
		parameterCheck{"repetition_penalty", request.RepetitionPenalty != nil},
	); err != nil {
		return result, err
	}
	result.InferenceConfig = bedrockInferenceConfig{MaxTokens: maxTokens, Temperature: request.Temperature, TopP: request.TopP, StopSequences: stop}
	if request.ServiceTier != "" {
		switch request.ServiceTier {
		case "default", "flex", "priority":
			result.ServiceTier = &bedrockServiceTier{Type: request.ServiceTier}
		default:
			return result, bedrockInvalid("service_tier")
		}
	}
	if request.BedrockServiceTier != "" {
		if request.BedrockServiceTier != "reserved" || result.ServiceTier != nil {
			return result, bedrockInvalid("service_tier")
		}
		result.ServiceTier = &bedrockServiceTier{Type: request.BedrockServiceTier}
	}
	if request.BedrockPerformanceLatency != "" {
		switch request.BedrockPerformanceLatency {
		case "standard", "optimized":
			result.PerformanceConfig = &bedrockPerformanceConfig{Latency: request.BedrockPerformanceLatency}
		default:
			return result, bedrockInvalid("performance_config")
		}
	}
	result.AdditionalModelResponseFieldPaths = append([]string(nil), request.BedrockAdditionalModelResponseFieldPaths...)
	result.AdditionalModelRequestFields = append(json.RawMessage(nil), request.BedrockAdditionalModelRequestFields...)
	if request.BedrockGuardrailConfig != nil {
		config := *request.BedrockGuardrailConfig
		result.GuardrailConfig = &config
	}
	if len(request.BedrockRequestMetadata) > 0 {
		result.RequestMetadata = make(map[string]string, len(request.BedrockRequestMetadata))
		for key, value := range request.BedrockRequestMetadata {
			result.RequestMetadata[key] = value
		}
	}
	if request.ResponseFormat != nil {
		format := request.ResponseFormat
		if format.Type != "json_schema" || format.JSONSchema == nil || format.JSONSchema.Schema == nil || format.JSONSchema.Strict != nil && !*format.JSONSchema.Strict || utf8.RuneCountInString(format.JSONSchema.Name) > 256 || utf8.RuneCountInString(format.JSONSchema.Description) > 8192 {
			return result, bedrockInvalid("response_format")
		}
		schema, err := json.Marshal(format.JSONSchema.Schema)
		if err != nil || len(schema) == 0 || len(schema) > 1<<20 || schema[0] != '{' {
			return result, bedrockInvalid("response_format")
		}
		result.OutputConfig = &bedrockOutputConfig{TextFormat: bedrockOutputFormat{Type: "json_schema", Structure: bedrockOutputFormatStructure{JSONSchema: bedrockJSONSchemaDefinition{Schema: string(schema), Name: format.JSONSchema.Name, Description: format.JSONSchema.Description}}}}
	}
	toolCalls := make(map[string]bool)
	for _, message := range request.Messages {
		if message.Name != "" || len(message.Annotations) > 0 {
			return result, bedrockInvalid("messages")
		}
		text := openai.ContentText(message.Content)
		switch message.Role {
		case "system", "developer":
			if text == "" || len(message.ToolCalls) > 0 || message.ToolCallID != "" {
				return result, bedrockInvalid("messages")
			}
			result.System = append(result.System, bedrockContentBlock{Text: text})
		case "user", "assistant":
			content := make([]bedrockContentBlock, 0, 1+len(message.ToolCalls))
			messageContent, err := bedrockInputContent(message.Content, message.NativeContent)
			if err != nil {
				return result, err
			}
			content = append(content, messageContent...)
			if message.Role == "user" && len(message.ToolCalls) > 0 || message.ToolCallID != "" {
				return result, bedrockInvalid("messages")
			}
			for _, call := range message.ToolCalls {
				if call.Type != "function" || call.ID == "" || call.Function.Name == "" || toolCalls[call.ID] {
					return result, bedrockInvalid("messages.tool_calls")
				}
				var input any
				if json.Unmarshal([]byte(call.Function.Arguments), &input) != nil || input == nil {
					return result, bedrockInvalid("messages.tool_calls.arguments")
				}
				toolCalls[call.ID] = true
				content = append(content, bedrockContentBlock{ToolUse: &bedrockToolUse{ID: call.ID, Name: call.Function.Name, Input: input}})
			}
			if len(message.Reasoning) > 0 {
				if message.Role != "assistant" {
					return result, bedrockInvalid("messages.reasoning")
				}
				if err := openai.ValidateReasoningBlocks(message.Reasoning); err != nil {
					return result, bedrockInvalid("messages.reasoning")
				}
				for _, reasoning := range message.Reasoning {
					block := bedrockContentBlock{ReasoningContent: &bedrockReasoningContent{}}
					switch reasoning.Type {
					case "thinking":
						block.ReasoningContent.ReasoningText = &bedrockReasoningText{Text: reasoning.Thinking, Signature: reasoning.Signature}
					case "redacted_thinking":
						if _, err := base64.StdEncoding.DecodeString(reasoning.Data); err != nil {
							return result, bedrockInvalid("messages.reasoning")
						}
						block.ReasoningContent.RedactedContent = reasoning.Data
					}
					position := len(content)
					if reasoning.Index != nil {
						position = *reasoning.Index
						if position > len(content) {
							position = len(content)
						}
					}
					content = append(content, bedrockContentBlock{})
					copy(content[position+1:], content[position:])
					content[position] = block
				}
			}
			if len(content) > 128 {
				return result, bedrockInvalid("messages.content")
			}
			if len(content) == 0 {
				return result, bedrockInvalid("messages.content")
			}
			result.Messages = append(result.Messages, bedrockMessage{Role: message.Role, Content: content})
		case "tool":
			if message.ToolCallID == "" || !toolCalls[message.ToolCallID] || text == "" || len(message.ToolCalls) > 0 {
				return result, bedrockInvalid("messages.tool_call_id")
			}
			result.Messages = append(result.Messages, bedrockMessage{Role: "user", Content: []bedrockContentBlock{{ToolResult: &bedrockToolResult{ID: message.ToolCallID, Content: []bedrockContentBlock{{Text: text}}}}}})
		default:
			return result, bedrockInvalid("messages.role")
		}
	}
	if len(result.Messages) == 0 {
		return result, bedrockInvalid("messages")
	}
	if len(request.Tools) > 0 {
		result.ToolConfig = &bedrockToolConfig{}
		seen := make(map[string]bool)
		for _, tool := range request.Tools {
			function := tool.Function
			if tool.Type != "function" || !openai.ValidBedrockToolName(function.Name) || seen[function.Name] || function.Strict != nil || function.PromptCacheBreakpoint != nil {
				return result, bedrockInvalid("tools")
			}
			seen[function.Name] = true
			spec := bedrockToolSpec{Name: function.Name, Description: function.Description}
			spec.InputSchema.JSON = function.Parameters
			if spec.InputSchema.JSON == nil {
				spec.InputSchema.JSON = map[string]any{"type": "object"}
			}
			result.ToolConfig.Tools = append(result.ToolConfig.Tools, bedrockTool{Spec: spec})
		}
		choice, err := bedrockChatToolChoice(request.ToolChoice, seen)
		if err != nil {
			return result, err
		}
		result.ToolConfig.ToolChoice = choice
	} else if request.ToolChoice != nil {
		return result, bedrockInvalid("tool_choice")
	}
	return result, nil
}

func bedrockChatToolChoice(value any, tools map[string]bool) (*bedrockToolChoice, error) {
	if value == nil {
		return nil, nil
	}
	switch choice := value.(type) {
	case string:
		switch choice {
		case "auto":
			return &bedrockToolChoice{Auto: &struct{}{}}, nil
		case "required":
			return &bedrockToolChoice{Any: &struct{}{}}, nil
		default:
			return nil, bedrockInvalid("tool_choice")
		}
	case map[string]any:
		function, ok := choice["function"].(map[string]any)
		name, _ := function["name"].(string)
		if !ok || len(choice) != 2 || choice["type"] != "function" || len(function) != 1 || !tools[name] {
			return nil, bedrockInvalid("tool_choice")
		}
		return &bedrockToolChoice{Tool: &bedrockSpecificToolChoice{Name: name}}, nil
	default:
		return nil, bedrockInvalid("tool_choice")
	}
}

func bedrockInputContent(value any, native []json.RawMessage) ([]bedrockContentBlock, error) {
	if value == nil {
		return nil, nil
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return nil, nil
		}
		return []bedrockContentBlock{{Text: text}}, nil
	}
	parts, ok := value.([]any)
	if !ok {
		return nil, bedrockInvalid("messages.content")
	}
	result := make([]bedrockContentBlock, 0, len(parts))
	for _, value := range parts {
		part, ok := value.(map[string]any)
		if !ok {
			return nil, bedrockInvalid("messages.content")
		}
		typeName, _ := part["type"].(string)
		switch typeName {
		case "text":
			text, ok := part["text"].(string)
			if !ok || text == "" || len(part) != 2 {
				return nil, bedrockInvalid("messages.content")
			}
			result = append(result, bedrockContentBlock{Text: text})
		case "image_url":
			imageValue, ok := part["image_url"].(map[string]any)
			if !ok || len(part) != 2 || len(imageValue) != 1 {
				return nil, bedrockInvalid("messages.content")
			}
			dataURL, ok := imageValue["url"].(string)
			if !ok {
				return nil, bedrockInvalid("messages.content")
			}
			attachment, err := openai.ParseDataImageURL(dataURL)
			if err != nil {
				return nil, err
			}
			image := bedrockImage{Format: strings.TrimPrefix(attachment.MediaType, "image/")}
			image.Source.Bytes = attachment.Data
			result = append(result, bedrockContentBlock{Image: &image})
		case "bedrock_document":
			index, ok := part["index"].(int)
			if !ok || len(part) != 2 || index < 0 || index >= len(native) {
				return nil, bedrockInvalid("messages.content")
			}
			var block struct {
				Document *bedrockDocument `json:"document"`
			}
			if json.Unmarshal(native[index], &block) != nil || block.Document == nil {
				return nil, bedrockInvalid("messages.content")
			}
			result = append(result, bedrockContentBlock{Document: block.Document})
		default:
			return nil, bedrockInvalid("messages.content")
		}
	}
	return result, nil
}

func (b Bedrock) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if request.Stream {
		return openai.ChatCompletionResponse{}, bedrockInvalid("stream")
	}
	body, err := bedrockChatRequest(request)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	endpoint := b.baseURL + "/model/" + url.PathEscape(request.Model) + "/converse"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if err := b.authorize(ctx, httpRequest, payload); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	response, err := b.client.Do(httpRequest)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := responseStatusError("bedrock", response)
		_ = response.Body.Close()
		return openai.ChatCompletionResponse{}, failure
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxChatCompletionResponseBytes+1))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if len(data) > maxChatCompletionResponseBytes {
		return openai.ChatCompletionResponse{}, errors.New("Bedrock response exceeds limit")
	}
	var decoded bedrockResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if len(decoded.AdditionalModelResponseFields) > 0 && string(decoded.AdditionalModelResponseFields) != "null" && len(request.BedrockAdditionalModelResponseFieldPaths) == 0 {
		return openai.ChatCompletionResponse{}, errors.New("Bedrock returned unrequested additional model response fields")
	}
	if len(decoded.Trace) > 0 && string(decoded.Trace) != "null" {
		traceEnabled := request.BedrockGuardrailConfig != nil && (request.BedrockGuardrailConfig.Trace == "enabled" || request.BedrockGuardrailConfig.Trace == "enabled_full")
		if !traceEnabled || len(decoded.Trace) > 1<<20 || !json.Valid(decoded.Trace) || bytes.Equal(bytes.TrimSpace(decoded.Trace), []byte("null")) {
			return openai.ChatCompletionResponse{}, errors.New("Bedrock returned invalid or unrequested guardrail trace")
		}
	}
	return bedrockToChat(decoded, request.Model)
}

func (b Bedrock) authorize(ctx context.Context, request *http.Request, payload []byte) error {
	request.Header.Del("Authorization")
	request.Header.Del("X-Amz-Security-Token")
	request.Header.Del("X-Amz-Date")
	request.Header.Del("X-Amz-Content-Sha256")
	if b.authType == "aws_sigv4" {
		credential, err := b.aws.Credential(ctx)
		if err != nil {
			return &Error{Class: FailureUnavailable, Provider: "bedrock", StatusCode: http.StatusServiceUnavailable, UpstreamCode: "credential_unavailable", Err: errors.New("AWS credential source is unavailable")}
		}
		if err := signAWSRequest(request, payload, credential, b.region, "bedrock", b.now()); err != nil {
			return bedrockInvalid("credential")
		}
		return nil
	}
	if b.authType != "bearer" {
		return bedrockInvalid("auth_type")
	}
	if b.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
	return nil
}

func bedrockToChat(response bedrockResponse, model string) (openai.ChatCompletionResponse, error) {
	result := openai.ChatCompletionResponse{ID: "chatcmpl-" + rand.Text(), Object: "chat.completion", Model: model}
	if response.Usage == nil {
		return result, errors.New("Bedrock response is missing usage")
	}
	usage := response.Usage
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.InputTokens > math.MaxInt-usage.OutputTokens || usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		return result, errors.New("invalid Bedrock usage")
	}
	result.Usage = openai.Usage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens}
	message := openai.Message{Role: "assistant"}
	if len(response.Output.Message.Content) == 0 || len(response.Output.Message.Content) > 128 {
		return result, errors.New("invalid Bedrock output content length")
	}
	var texts []string
	offset := 0
	for blockIndex, block := range response.Output.Message.Content {
		fields := 0
		if block.Text != "" {
			fields++
			texts = append(texts, block.Text)
			offset += utf8.RuneCountInString(block.Text)
		}
		if block.ToolUse != nil {
			fields++
			arguments, err := json.Marshal(block.ToolUse.Input)
			if err != nil || block.ToolUse.ID == "" || block.ToolUse.Name == "" {
				return result, errors.New("invalid Bedrock tool use")
			}
			message.ToolCalls = append(message.ToolCalls, openai.ToolCall{ID: block.ToolUse.ID, Type: "function", Function: openai.FunctionCall{Name: block.ToolUse.Name, Arguments: string(arguments)}})
		}
		if block.CitationsContent != nil {
			fields++
			text, annotations, err := bedrockCitationAnnotations(*block.CitationsContent, offset)
			if err != nil {
				return result, err
			}
			texts = append(texts, text)
			message.Annotations = append(message.Annotations, annotations...)
			offset += utf8.RuneCountInString(text)
		}
		if block.ReasoningContent != nil {
			fields++
			reasoning := block.ReasoningContent
			members := 0
			index := blockIndex
			converted := openai.ReasoningBlock{Index: &index}
			if reasoning.ReasoningText != nil {
				members++
				converted.Type = "thinking"
				converted.Thinking = reasoning.ReasoningText.Text
				converted.Signature = reasoning.ReasoningText.Signature
			}
			if reasoning.RedactedContent != "" {
				members++
				if _, err := base64.StdEncoding.DecodeString(reasoning.RedactedContent); err != nil {
					return result, errors.New("invalid Bedrock redacted reasoning content")
				}
				converted.Type = "redacted_thinking"
				converted.Data = reasoning.RedactedContent
			}
			if members != 1 {
				return result, errors.New("invalid Bedrock reasoning content union")
			}
			message.Reasoning = append(message.Reasoning, converted)
		}
		if block.Image != nil || block.Document != nil || block.ToolResult != nil || fields != 1 {
			return result, errors.New("invalid Bedrock output content block")
		}
	}
	if err := openai.ValidateChatAnnotations(message.Annotations); err != nil {
		return result, fmt.Errorf("invalid Bedrock citations: %w", err)
	}
	if err := openai.ValidateReasoningBlocks(message.Reasoning); err != nil {
		return result, fmt.Errorf("invalid Bedrock reasoning content: %w", err)
	}
	if len(texts) > 0 {
		message.Content = strings.Join(texts, "")
	}
	if message.Content == nil && len(message.ToolCalls) == 0 && len(message.Reasoning) == 0 {
		return result, errors.New("Bedrock response contains no output")
	}
	finishReasons := map[string]string{
		"end_turn":                      "stop",
		"stop_sequence":                 "stop",
		"max_tokens":                    "length",
		"tool_use":                      "tool_calls",
		"content_filtered":              "content_filter",
		"guardrail_intervened":          "content_filter",
		"malformed_model_output":        "error",
		"malformed_tool_use":            "error",
		"model_context_window_exceeded": "length",
	}
	finishReason, found := finishReasons[response.StopReason]
	if !found {
		return result, errors.New("unknown Bedrock stop reason")
	}
	if response.StopReason == "guardrail_intervened" || response.StopReason == "malformed_model_output" || response.StopReason == "malformed_tool_use" || response.StopReason == "model_context_window_exceeded" {
		marker, _ := json.Marshal(map[string]string{"type": "bedrock_stop_reason", "reason": response.StopReason})
		message.NativeContent = append(message.NativeContent, marker)
	}
	if len(response.AdditionalModelResponseFields) > 0 && string(response.AdditionalModelResponseFields) != "null" {
		marker, err := json.Marshal(struct {
			Type   string          `json:"type"`
			Fields json.RawMessage `json:"fields"`
		}{Type: "bedrock_additional_model_response_fields", Fields: response.AdditionalModelResponseFields})
		if err != nil {
			return result, errors.New("invalid Bedrock additional response fields")
		}
		message.NativeContent = append(message.NativeContent, marker)
	}
	if len(response.Trace) > 0 && string(response.Trace) != "null" {
		marker, err := json.Marshal(struct {
			Type  string          `json:"type"`
			Trace json.RawMessage `json:"trace"`
		}{Type: "bedrock_guardrail_trace", Trace: response.Trace})
		if err != nil {
			return result, errors.New("invalid Bedrock guardrail trace")
		}
		message.NativeContent = append(message.NativeContent, marker)
	}
	result.Choices = []openai.Choice{{Index: 0, Message: message, FinishReason: finishReason}}
	return result, nil
}

func bedrockCitationAnnotations(block bedrockCitationsContent, offset int) (string, []openai.ChatAnnotation, error) {
	if len(block.Content) == 0 || len(block.Content) > 128 || len(block.Citations) > 128 {
		return "", nil, errors.New("invalid Bedrock citations content")
	}
	var text strings.Builder
	for _, content := range block.Content {
		if content.Text == nil {
			return "", nil, errors.New("invalid Bedrock generated citation content")
		}
		text.WriteString(*content.Text)
	}
	if text.Len() == 0 {
		return "", nil, errors.New("empty Bedrock generated citation content")
	}
	start := offset
	end := offset + utf8.RuneCountInString(text.String())
	annotations := make([]openai.ChatAnnotation, 0, len(block.Citations))
	for _, citation := range block.Citations {
		annotation, err := bedrockCitationAnnotation(citation, start, end)
		if err != nil {
			return "", nil, err
		}
		annotations = append(annotations, annotation)
	}
	return text.String(), annotations, nil
}

func bedrockCitationAnnotation(citation bedrockCitation, start, end int) (openai.ChatAnnotation, error) {
	locations := 0
	for _, present := range []bool{citation.Location.DocumentChar != nil, citation.Location.DocumentChunk != nil, citation.Location.DocumentPage != nil, citation.Location.SearchResultLocation != nil, citation.Location.Web != nil} {
		if present {
			locations++
		}
	}
	if locations != 1 {
		return openai.ChatAnnotation{}, errors.New("invalid Bedrock citation location")
	}
	if citation.Location.Web != nil {
		title := citation.Title
		if title == "" {
			title = citation.Location.Web.Domain
		}
		if title == "" {
			if parsed, err := url.Parse(citation.Location.Web.URL); err == nil {
				title = parsed.Host
			}
		}
		return openai.ChatAnnotation{Type: "url_citation", URLCitation: &openai.ChatURLCitation{StartIndex: start, EndIndex: end, Title: title, URL: citation.Location.Web.URL}}, nil
	}
	source := &openai.ChatSourceCitation{StartIndex: start, EndIndex: end, Title: citation.Title, Source: citation.Source}
	for _, content := range citation.SourceContent {
		if content.Text == nil {
			return openai.ChatAnnotation{}, errors.New("invalid Bedrock citation source content")
		}
		source.SourceContent = append(source.SourceContent, *content.Text)
	}
	setLocation := func(locationType string, documentIndex, locationStart, locationEnd *int) error {
		if documentIndex == nil || locationStart == nil || locationEnd == nil {
			return errors.New("incomplete Bedrock document citation location")
		}
		source.LocationType, source.DocumentIndex = locationType, documentIndex
		source.LocationStart, source.LocationEnd = *locationStart, *locationEnd
		return nil
	}
	var err error
	switch {
	case citation.Location.DocumentChar != nil:
		location := citation.Location.DocumentChar
		err = setLocation("document_char", location.DocumentIndex, location.Start, location.End)
	case citation.Location.DocumentChunk != nil:
		location := citation.Location.DocumentChunk
		err = setLocation("document_chunk", location.DocumentIndex, location.Start, location.End)
	case citation.Location.DocumentPage != nil:
		location := citation.Location.DocumentPage
		err = setLocation("document_page", location.DocumentIndex, location.Start, location.End)
	case citation.Location.SearchResultLocation != nil:
		location := citation.Location.SearchResultLocation
		if location.SearchResultIndex == nil || location.Start == nil || location.End == nil {
			err = errors.New("incomplete Bedrock search citation location")
		} else {
			source.LocationType, source.SearchResultIndex = "search_result", location.SearchResultIndex
			source.LocationStart, source.LocationEnd = *location.Start, *location.End
		}
	}
	if err != nil {
		return openai.ChatAnnotation{}, err
	}
	return openai.ChatAnnotation{Type: "source_citation", SourceCitation: source}, nil
}

func (Bedrock) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, bedrockInvalid("responses")
}
