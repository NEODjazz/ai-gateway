package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

type Bedrock struct {
	baseURL  string
	apiKey   string
	authType string
	region   string
	client   *http.Client
	now      func() time.Time
}

type bedrockContentBlock struct {
	Text       string             `json:"text,omitempty"`
	ToolUse    *bedrockToolUse    `json:"toolUse,omitempty"`
	ToolResult *bedrockToolResult `json:"toolResult,omitempty"`
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
	Messages        []bedrockMessage       `json:"messages"`
	System          []bedrockContentBlock  `json:"system,omitempty"`
	InferenceConfig bedrockInferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig      *struct {
		Tools []bedrockTool `json:"tools"`
	} `json:"toolConfig,omitempty"`
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
	StopReason string `json:"stopReason"`
	Usage      *struct {
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
	return Bedrock{baseURL: strings.TrimRight(baseURL, "/"), apiKey: credential, authType: authType, region: strings.ToLower(strings.TrimSpace(region)), client: client, now: time.Now}
}

func (Bedrock) SupportsResponses() bool { return false }
func (Bedrock) SupportsTools() bool     { return true }

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
	attachments, err := openai.ChatImageAttachments(request.Messages)
	if err != nil {
		return result, err
	} else if len(attachments) > 0 {
		return result, bedrockInvalid("messages.content")
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
		parameterCheck{"stream", request.Stream}, parameterCheck{"stream_options", request.StreamOptions != nil},
		parameterCheck{"tool_choice", request.ToolChoice != nil}, parameterCheck{"parallel_tool_calls", request.ParallelToolCalls != nil},
		parameterCheck{"response_format", request.ResponseFormat != nil}, parameterCheck{"seed", request.Seed != nil},
		parameterCheck{"metadata", request.Metadata != nil}, parameterCheck{"store", request.Store != nil},
		parameterCheck{"modalities", request.Modalities != nil}, parameterCheck{"reasoning_effort", request.ReasoningEffort != ""},
		parameterCheck{"safe_prompt", request.SafePrompt != nil}, parameterCheck{"n", request.N != nil && *request.N != 1},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""}, parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""},
		parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil}, parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""},
		parameterCheck{"prompt_mode", request.PromptMode != ""}, parameterCheck{"prediction", request.Prediction != nil},
		parameterCheck{"service_tier", request.ServiceTier != ""}, parameterCheck{"user", request.User != ""},
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
	toolCalls := make(map[string]bool)
	for _, message := range request.Messages {
		if message.Name != "" || len(message.Annotations) > 0 || len(message.Reasoning) > 0 {
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
			if text != "" {
				content = append(content, bedrockContentBlock{Text: text})
			}
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
		result.ToolConfig = &struct {
			Tools []bedrockTool `json:"tools"`
		}{}
		seen := make(map[string]bool)
		for _, tool := range request.Tools {
			function := tool.Function
			if tool.Type != "function" || function.Name == "" || seen[function.Name] || function.Strict != nil || function.PromptCacheBreakpoint != nil {
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
	}
	return result, nil
}

func (b Bedrock) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
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
	if b.authType == "aws_sigv4" {
		credential, err := parseAWSCredential(b.apiKey)
		if err != nil {
			return openai.ChatCompletionResponse{}, bedrockInvalid("credential")
		}
		if err := signAWSRequest(httpRequest, payload, credential, b.region, "bedrock", b.now()); err != nil {
			return openai.ChatCompletionResponse{}, bedrockInvalid("credential")
		}
	} else if b.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+b.apiKey)
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
	return bedrockToChat(decoded, request.Model)
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
	var texts []string
	for _, block := range response.Output.Message.Content {
		if block.Text != "" {
			texts = append(texts, block.Text)
		}
		if block.ToolUse != nil {
			arguments, err := json.Marshal(block.ToolUse.Input)
			if err != nil || block.ToolUse.ID == "" || block.ToolUse.Name == "" {
				return result, errors.New("invalid Bedrock tool use")
			}
			message.ToolCalls = append(message.ToolCalls, openai.ToolCall{ID: block.ToolUse.ID, Type: "function", Function: openai.FunctionCall{Name: block.ToolUse.Name, Arguments: string(arguments)}})
		}
	}
	if len(texts) > 0 {
		message.Content = strings.Join(texts, "")
	}
	if message.Content == nil && len(message.ToolCalls) == 0 {
		return result, errors.New("Bedrock response contains no output")
	}
	finishReasons := map[string]string{"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length", "tool_use": "tool_calls", "content_filtered": "content_filter"}
	finishReason, found := finishReasons[response.StopReason]
	if !found {
		return result, errors.New("unknown Bedrock stop reason")
	}
	result.Choices = []openai.Choice{{Index: 0, Message: message, FinishReason: finishReason}}
	return result, nil
}

func (Bedrock) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, bedrockInvalid("responses")
}
