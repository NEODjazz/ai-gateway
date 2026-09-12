package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

const maxCohereRerankResponseBytes = 8 << 20
const maxCohereEmbeddingInputs = 96

type Cohere struct {
	baseURL        string
	apiKey         string
	upstreamStream bool
	client         *http.Client
}

type cohereRerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            *int     `json:"top_n,omitempty"`
	MaxTokensPerDoc *int     `json:"max_tokens_per_doc,omitempty"`
}

type cohereEmbeddingRequest struct {
	Model           string   `json:"model"`
	Texts           []string `json:"texts"`
	InputType       string   `json:"input_type"`
	EmbeddingTypes  []string `json:"embedding_types"`
	OutputDimension *int     `json:"output_dimension,omitempty"`
}

type cohereChatRequest struct {
	Model            string                `json:"model"`
	Messages         []cohereChatMessage   `json:"messages"`
	ResponseFormat   *cohereResponseFormat `json:"response_format,omitempty"`
	MaxTokens        *int                  `json:"max_tokens,omitempty"`
	StopSequences    []string              `json:"stop_sequences,omitempty"`
	Temperature      *float64              `json:"temperature,omitempty"`
	P                *float64              `json:"p,omitempty"`
	K                *int                  `json:"k,omitempty"`
	Seed             *int64                `json:"seed,omitempty"`
	FrequencyPenalty *float64              `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64              `json:"presence_penalty,omitempty"`
	Logprobs         *bool                 `json:"logprobs,omitempty"`
	Tools            []openai.Tool         `json:"tools,omitempty"`
	ToolChoice       string                `json:"tool_choice,omitempty"`
	StrictTools      bool                  `json:"strict_tools,omitempty"`
	Stream           bool                  `json:"stream,omitempty"`
}

type cohereChatMessage struct {
	Role       string            `json:"role"`
	Content    any               `json:"content,omitempty"`
	ToolCalls  []openai.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
}

type cohereToolResultContent struct {
	Type     string                   `json:"type"`
	Document cohereToolResultDocument `json:"document"`
}

type cohereToolResultDocument struct {
	Data string `json:"data"`
}

type cohereResponseFormat struct {
	Type   string `json:"type"`
	Schema any    `json:"schema,omitempty"`
}

type cohereChatUsageValue struct {
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
}

type cohereChatUsageResponse struct {
	BilledUnits *cohereChatUsageValue `json:"billed_units"`
	Tokens      *cohereChatUsageValue `json:"tokens"`
}

type cohereChatResponse struct {
	ID           string `json:"id"`
	FinishReason string `json:"finish_reason"`
	Message      struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		ToolCalls []openai.ToolCall `json:"tool_calls"`
	} `json:"message"`
	Usage    *cohereChatUsageResponse `json:"usage"`
	Logprobs []cohereLogprobItem      `json:"logprobs"`
}

type cohereLogprobItem struct {
	Text     *string    `json:"text"`
	TokenIDs []int      `json:"token_ids"`
	Logprobs *[]float64 `json:"logprobs"`
}

type cohereChatStreamEvent struct {
	Type     string             `json:"type"`
	ID       string             `json:"id"`
	Index    *int               `json:"index"`
	Logprobs *cohereLogprobItem `json:"logprobs"`
	Delta    struct {
		Message struct {
			Role      string           `json:"role"`
			ToolCalls *openai.ToolCall `json:"tool_calls"`
			Content   struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
		FinishReason string                   `json:"finish_reason"`
		Usage        *cohereChatUsageResponse `json:"usage"`
	} `json:"delta"`
}

func NewCohere(baseURL, apiKey string, stream ...bool) Cohere {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.cohere.com"
	}
	client := newProviderHTTPClient(180 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return Cohere{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, upstreamStream: len(stream) > 0 && stream[0], client: client}
}

func (Cohere) SupportsResponses() bool { return false }

func (Cohere) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := validateChatReasoningContent("cohere", request.Messages, false); err != nil {
		return err
	}
	if err := validateChatMessagePrefix("cohere", request.Messages, false); err != nil {
		return err
	}
	if strings.TrimSpace(request.Model) == "" {
		return cohereChatError("model", "model is required")
	}
	if len(request.Messages) == 0 {
		return cohereChatError("messages", "messages are required")
	}
	if err := rejectLegacyFunctionCalling("cohere", request); err != nil {
		return err
	}
	if err := rejectChatMessageAudio("cohere", request.Messages); err != nil {
		return err
	}
	if err := validateChatPromptCacheBreakpoints("cohere", request, false); err != nil {
		return err
	}
	if request.MaxTokens != nil && request.MaxCompletionTokens != nil {
		return cohereChatError("max_tokens", "max_tokens and max_completion_tokens cannot be combined")
	}
	if (request.MaxTokens != nil && *request.MaxTokens <= 0) || (request.MaxCompletionTokens != nil && *request.MaxCompletionTokens <= 0) {
		return cohereChatError("max_tokens", "output token limit must be positive")
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 1) {
		return cohereChatError("temperature", "temperature must be between 0 and 1")
	}
	if request.TopP != nil && (*request.TopP < 0.01 || *request.TopP > 0.99) {
		return cohereChatError("top_p", "top_p must be between 0.01 and 0.99")
	}
	if request.TopK != nil && (*request.TopK < 0 || *request.TopK > 500) {
		return cohereChatError("top_k", "top_k must be between 0 and 500")
	}
	if request.Seed != nil && *request.Seed < 0 {
		return cohereChatError("seed", "seed must be nonnegative")
	}
	if request.FrequencyPenalty != nil && (*request.FrequencyPenalty < 0 || *request.FrequencyPenalty > 1) {
		return cohereChatError("frequency_penalty", "frequency_penalty must be between 0 and 1")
	}
	if request.PresencePenalty != nil && (*request.PresencePenalty < 0 || *request.PresencePenalty > 1) {
		return cohereChatError("presence_penalty", "presence_penalty must be between 0 and 1")
	}
	if _, ok := openai.StopSequences(request.Stop); !ok {
		return cohereChatError("stop", "stop must contain at most five non-empty strings")
	}
	if format := request.ResponseFormat; format != nil {
		if format.Type != "text" && format.Type != "json_object" && format.Type != "json_schema" {
			return cohereChatError("response_format", "response_format is not supported")
		}
		if format.Type == "json_schema" && (format.JSONSchema == nil || format.JSONSchema.Schema == nil) {
			return cohereChatError("response_format", "json_schema response_format requires a schema")
		}
	}
	toolCalls := map[string]bool{}
	for _, message := range request.Messages {
		if message.Role != "system" && message.Role != "developer" && message.Role != "user" && message.Role != "assistant" && message.Role != "tool" {
			return rejectParameters("cohere", parameterCheck{"messages", true})
		}
		if message.Role == "function" || message.FunctionCall != nil || message.Refusal != nil || len(message.Annotations) > 0 || message.Name != "" {
			return rejectParameters("cohere", parameterCheck{"messages", true})
		}
		if attachments, err := openai.ChatImageAttachments([]openai.Message{message}); err != nil || len(attachments) > 0 {
			return rejectParameters("cohere", parameterCheck{"messages", true})
		}
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			for _, call := range message.ToolCalls {
				if err := validateCohereToolCall(call); err != nil || toolCalls[call.ID] {
					return rejectParameters("cohere", parameterCheck{"messages", true})
				}
				toolCalls[call.ID] = true
			}
			if message.Content != nil && openai.ContentText(message.Content) != "" {
				return rejectParameters("cohere", parameterCheck{"messages", true})
			}
			continue
		}
		if message.Role == "tool" {
			if message.ToolCallID == "" || !toolCalls[message.ToolCallID] || len(message.ToolCalls) > 0 {
				return rejectParameters("cohere", parameterCheck{"messages", true})
			}
		} else if message.ToolCallID != "" || len(message.ToolCalls) > 0 {
			return rejectParameters("cohere", parameterCheck{"messages", true})
		}
		if _, err := cohereChatText(message.Content); err != nil {
			return rejectParameters("cohere", parameterCheck{"messages", true})
		}
	}
	strictTools := false
	if len(request.Tools) > 128 {
		return cohereChatError("tools", "tools must contain at most 128 definitions")
	}
	toolNames := make(map[string]bool, len(request.Tools))
	for index, tool := range request.Tools {
		name := strings.TrimSpace(tool.Function.Name)
		if tool.Type != "function" || name == "" || toolNames[name] || tool.Function.PromptCacheBreakpoint != nil {
			return rejectParameters("cohere", parameterCheck{"tools", true})
		}
		toolNames[name] = true
		if tool.Function.Parameters != nil {
			if _, ok := tool.Function.Parameters.(map[string]any); !ok {
				return rejectParameters("cohere", parameterCheck{"tools", true})
			}
		}
		strict := tool.Function.Strict != nil && *tool.Function.Strict
		if index > 0 && strict != strictTools {
			return cohereChatError("tools", "Cohere strict_tools requires the same strict setting for every tool")
		}
		strictTools = strict
	}
	if request.ResponseFormat != nil && len(request.Tools) > 0 {
		return cohereChatError("response_format", "response_format cannot be combined with tools")
	}
	if _, err := cohereToolChoice(request.ToolChoice, len(request.Tools)); err != nil {
		return err
	}
	if request.ParallelToolCalls != nil && !*request.ParallelToolCalls {
		return cohereChatError("parallel_tool_calls", "Cohere cannot disable parallel tool calls")
	}
	return rejectParameters("cohere",
		parameterCheck{"metadata", request.Metadata != nil}, parameterCheck{"store", request.Store != nil}, parameterCheck{"modalities", request.Modalities != nil}, parameterCheck{"audio", request.Audio != nil},
		parameterCheck{"reasoning_effort", request.ReasoningEffort != ""}, parameterCheck{"n", request.N != nil}, parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"safe_prompt", request.SafePrompt != nil},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""}, parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil}, parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""},
		parameterCheck{"prompt_mode", request.PromptMode != ""},
		parameterCheck{"prediction", request.Prediction != nil}, parameterCheck{"service_tier", request.ServiceTier != ""}, parameterCheck{"user", request.User != ""}, parameterCheck{"verbosity", request.Verbosity != ""},
		parameterCheck{"web_search_options", request.WebSearchOptions != nil}, parameterCheck{"web_fetch_options", request.WebFetchOptions != nil}, parameterCheck{"top_logprobs", request.TopLogprobs != nil},
		parameterCheck{"min_p", request.MinP != nil}, parameterCheck{"top_a", request.TopA != nil},
		parameterCheck{"repetition_penalty", request.RepetitionPenalty != nil},
		parameterCheck{"logit_bias", request.LogitBias != nil},
	)
}

func (p Cohere) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	native, err := cohereNativeChatRequest(request, false)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	response, err := p.doChat(ctx, native, "application/json")
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer response.Body.Close()
	var upstream cohereChatResponse
	if err := decodeCohereChatResponse(response.Body, &upstream); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if upstream.ID == "" || upstream.Message.Role != "assistant" || (len(upstream.Message.Content) == 0 && len(upstream.Message.ToolCalls) == 0) {
		return openai.ChatCompletionResponse{}, errors.New("invalid Cohere chat response")
	}
	var content strings.Builder
	for _, block := range upstream.Message.Content {
		if block.Type != "text" {
			return openai.ChatCompletionResponse{}, errors.New("unsupported Cohere chat content block")
		}
		content.WriteString(block.Text)
	}
	finishReason, err := cohereFinishReason(upstream.FinishReason)
	if strings.EqualFold(upstream.FinishReason, "TOOL_CALL") {
		finishReason, err = "tool_calls", nil
		if len(upstream.Message.ToolCalls) == 0 {
			err = errors.New("Cohere tool-call response omitted tool calls")
		}
		for _, call := range upstream.Message.ToolCalls {
			if callErr := validateCohereToolCall(call); callErr != nil {
				err = callErr
				break
			}
		}
	} else if len(upstream.Message.ToolCalls) > 0 {
		err = errors.New("Cohere returned tool calls without TOOL_CALL finish reason")
	}
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	usage, err := cohereChatUsage(upstream.Usage)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	var logprobs *openai.ChoiceLogprobs
	if len(upstream.Logprobs) > 0 {
		converted, logprobText, err := cohereChoiceLogprobs(upstream.Logprobs)
		if err != nil || logprobText != content.String() {
			return openai.ChatCompletionResponse{}, errors.New("invalid Cohere chat logprobs")
		}
		logprobs = &converted
	} else if request.Logprobs != nil && *request.Logprobs && content.Len() > 0 {
		return openai.ChatCompletionResponse{}, errors.New("Cohere chat response omitted requested logprobs")
	}
	return openai.ChatCompletionResponse{ID: upstream.ID, Object: "chat.completion", Created: time.Now().Unix(), Model: request.Model, Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: content.String(), ToolCalls: upstream.Message.ToolCalls}, FinishReason: finishReason, Logprobs: logprobs}}, Usage: usage}, nil
}

func cohereNativeChatRequest(request openai.ChatCompletionRequest, stream bool) (cohereChatRequest, error) {
	messages := make([]cohereChatMessage, len(request.Messages))
	for index, message := range request.Messages {
		role := message.Role
		if role == "developer" {
			role = "system"
		}
		content, err := cohereChatText(message.Content)
		if err != nil {
			if message.Role == "assistant" && len(message.ToolCalls) > 0 {
				messages[index] = cohereChatMessage{Role: role, ToolCalls: message.ToolCalls}
				continue
			}
			return cohereChatRequest{}, err
		}
		if message.Role == "tool" {
			messages[index] = cohereChatMessage{Role: role, ToolCallID: message.ToolCallID, Content: []cohereToolResultContent{{Type: "document", Document: cohereToolResultDocument{Data: content}}}}
		} else {
			messages[index] = cohereChatMessage{Role: role, Content: content}
		}
	}
	maxTokens := request.MaxCompletionTokens
	if maxTokens == nil {
		maxTokens = request.MaxTokens
	}
	stop, _ := openai.StopSequences(request.Stop)
	native := cohereChatRequest{
		Model: request.Model, Messages: messages, MaxTokens: maxTokens, StopSequences: stop,
		Temperature: request.Temperature, P: request.TopP, K: request.TopK, Seed: request.Seed,
		FrequencyPenalty: request.FrequencyPenalty, PresencePenalty: request.PresencePenalty, Logprobs: request.Logprobs, Stream: stream,
	}
	native.Tools = append([]openai.Tool(nil), request.Tools...)
	for index := range native.Tools {
		native.Tools[index].Function.Strict = nil
	}
	native.ToolChoice, _ = cohereToolChoice(request.ToolChoice, len(request.Tools))
	if len(request.Tools) > 0 && request.Tools[0].Function.Strict != nil {
		native.StrictTools = *request.Tools[0].Function.Strict
	}
	if format := request.ResponseFormat; format != nil && format.Type != "text" {
		native.ResponseFormat = &cohereResponseFormat{Type: "json_object"}
		if format.Type == "json_schema" && format.JSONSchema != nil {
			native.ResponseFormat.Schema = format.JSONSchema.Schema
		}
	}
	return native, nil
}

func cohereToolChoice(value any, tools int) (string, error) {
	if value == nil {
		return "", nil
	}
	choice, ok := value.(string)
	if ok && choice == "auto" {
		return "", nil
	}
	if !ok || tools == 0 || (choice != "required" && choice != "none") {
		return "", cohereChatError("tool_choice", "tool_choice must be auto, required, or none with declared tools")
	}
	return strings.ToUpper(choice), nil
}

func validateCohereToolCall(call openai.ToolCall) error {
	if call.ID == "" || call.Type != "function" || strings.TrimSpace(call.Function.Name) == "" || call.Index != nil || call.ExtraContent != nil || len(call.Function.Arguments) > openai.MaxChatFunctionArgumentsChars {
		return errors.New("invalid Cohere tool call")
	}
	var arguments map[string]any
	if json.Unmarshal([]byte(call.Function.Arguments), &arguments) != nil || arguments == nil {
		return errors.New("invalid Cohere tool call arguments")
	}
	return nil
}

func (p Cohere) doChat(ctx context.Context, native cohereChatRequest, accept string) (*http.Response, error) {
	body, err := json.Marshal(native)
	if err != nil {
		return nil, err
	}
	if len(body) > openai.MaxInferenceBodyBytes {
		return nil, cohereChatError("messages", "chat request exceeds limit")
	}
	endpoint, err := cohereEndpoint(p.baseURL, "v2/chat")
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", accept)
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		return nil, responseStatusError("cohere", response)
	}
	return response, nil
}

func (p Cohere) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if !p.upstreamStream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}
	native, err := cohereNativeChatRequest(request, true)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	response, err := p.doChat(ctx, native, "text/event-stream")
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer response.Body.Close()
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return openai.ChatCompletionResponse{}, errors.New("Cohere chat stream returned non-SSE content")
	}
	wantLogprobs := request.Logprobs != nil && *request.Logprobs
	return streamCohereChat(&responseStreamReader{source: response.Body, remaining: maxResponseStreamBytes}, request.Model, wantLogprobs, write)
}

func streamCohereChat(body io.Reader, model string, wantLogprobs bool, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	result := openai.ChatCompletionResponse{Object: "chat.completion", Created: time.Now().Unix(), Model: model, Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant"}}}}
	started, active, ended := false, false, false
	nextIndex := 0
	toolIndexes := make(map[int]int)
	activeTools := make(map[int]bool)
	err := scanSSEEvents(body, func(event, payload string) error {
		var item cohereChatStreamEvent
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return err
		}
		if event == "" || item.Type != event || ended {
			return errors.New("invalid Cohere chat stream event")
		}
		switch event {
		case "message-start":
			if started || item.ID == "" || item.Delta.Message.Role != "assistant" {
				return errors.New("invalid Cohere message-start event")
			}
			started = true
			result.ID = item.ID
			return write(openAIChatCompletionChunkPayload(result.ID, result.Model, 0, "assistant", "", nil))
		case "content-start":
			if !started || active || len(toolIndexes) > 0 || item.Index == nil || *item.Index != nextIndex || item.Delta.Message.Content.Type != "text" {
				return errors.New("invalid Cohere content-start event")
			}
			active = true
		case "content-delta":
			if !active || item.Index == nil || *item.Index != nextIndex {
				return errors.New("invalid Cohere content-delta event")
			}
			text := item.Delta.Message.Content.Text
			if text == "" {
				return nil
			}
			var logprobs *openai.ChoiceLogprobs
			if item.Logprobs != nil {
				converted, logprobText, err := cohereChoiceLogprobs([]cohereLogprobItem{*item.Logprobs})
				if err != nil || logprobText != text {
					return errors.New("invalid Cohere content-delta logprobs")
				}
				logprobs = &converted
				if result.Choices[0].Logprobs == nil {
					result.Choices[0].Logprobs = &openai.ChoiceLogprobs{}
				}
				result.Choices[0].Logprobs.Content = append(result.Choices[0].Logprobs.Content, converted.Content...)
			} else if wantLogprobs {
				return errors.New("Cohere content-delta omitted requested logprobs")
			}
			result.Choices[0].Message.Content = openai.ContentText(result.Choices[0].Message.Content) + text
			if logprobs == nil {
				return write(openAIChatCompletionChunkPayload(result.ID, result.Model, 0, "", text, nil))
			}
			payload, err := chatCompletionLogprobChunkPayload(result.ID, result.Model, result.Created, "", text, logprobs)
			if err != nil {
				return err
			}
			return write(payload)
		case "content-end":
			if !active || item.Index == nil || *item.Index != nextIndex {
				return errors.New("invalid Cohere content-end event")
			}
			active = false
			nextIndex++
		case "tool-plan-delta":
			if !started || active || nextIndex > 0 || len(toolIndexes) > 0 {
				return errors.New("invalid Cohere tool-plan-delta event")
			}
		case "tool-call-start":
			if !started || active || nextIndex > 0 || item.Index == nil || *item.Index < 0 || *item.Index >= maxChatStreamToolCalls || item.Delta.Message.ToolCalls == nil {
				return errors.New("invalid Cohere tool-call-start event")
			}
			upstreamIndex := *item.Index
			if _, exists := toolIndexes[upstreamIndex]; exists {
				return errors.New("duplicate Cohere tool call index")
			}
			call := *item.Delta.Message.ToolCalls
			if call.ID == "" || call.Type != "function" || strings.TrimSpace(call.Function.Name) == "" || call.Index != nil || call.ExtraContent != nil || len(call.Function.Arguments) > openai.MaxChatFunctionArgumentsChars {
				return errors.New("invalid Cohere tool-call-start event")
			}
			toolIndex := len(result.Choices[0].Message.ToolCalls)
			toolIndexes[upstreamIndex] = toolIndex
			activeTools[upstreamIndex] = true
			result.Choices[0].Message.ToolCalls = append(result.Choices[0].Message.ToolCalls, call)
			return write(openAIChatToolCallChunkPayload(result.ID, result.Model, toolIndex, call))
		case "tool-call-delta":
			if !started || item.Index == nil || item.Delta.Message.ToolCalls == nil || !activeTools[*item.Index] {
				return errors.New("invalid Cohere tool-call-delta event")
			}
			delta := *item.Delta.Message.ToolCalls
			if delta.ID != "" || delta.Type != "" || delta.Function.Name != "" || delta.Index != nil || delta.ExtraContent != nil {
				return errors.New("invalid Cohere tool-call-delta event")
			}
			toolIndex := toolIndexes[*item.Index]
			current := &result.Choices[0].Message.ToolCalls[toolIndex]
			if len(delta.Function.Arguments) > openai.MaxChatFunctionArgumentsChars-len(current.Function.Arguments) {
				return errors.New("Cohere tool call arguments exceed limit")
			}
			current.Function.Arguments += delta.Function.Arguments
			return write(openAIChatToolCallChunkPayload(result.ID, result.Model, toolIndex, delta))
		case "tool-call-end":
			if !started || item.Index == nil || !activeTools[*item.Index] {
				return errors.New("invalid Cohere tool-call-end event")
			}
			call := result.Choices[0].Message.ToolCalls[toolIndexes[*item.Index]]
			if err := validateCohereToolCall(call); err != nil {
				return err
			}
			activeTools[*item.Index] = false
		case "message-end":
			if !started || active || (nextIndex == 0 && len(toolIndexes) == 0) {
				return errors.New("invalid Cohere message-end event")
			}
			for _, toolActive := range activeTools {
				if toolActive {
					return errors.New("Cohere tool call ended before tool-call-end")
				}
			}
			finish := "tool_calls"
			if len(toolIndexes) > 0 {
				if !strings.EqualFold(item.Delta.FinishReason, "TOOL_CALL") {
					return errors.New("Cohere tool calls ended without TOOL_CALL finish reason")
				}
			} else {
				var err error
				finish, err = cohereFinishReason(item.Delta.FinishReason)
				if err != nil {
					return err
				}
			}
			usage, err := cohereChatUsage(item.Delta.Usage)
			if err != nil {
				return err
			}
			result.Choices[0].FinishReason = finish
			result.Usage = usage
			ended = true
			return write(openAIChatCompletionChunkPayload(result.ID, result.Model, 0, "", "", &finish))
		case "debug":
			return nil
		default:
			return errors.New("unsupported Cohere chat stream event")
		}
		return nil
	})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if !ended {
		return openai.ChatCompletionResponse{}, errors.New("Cohere chat stream ended before message-end")
	}
	return result, nil
}

func cohereChoiceLogprobs(items []cohereLogprobItem) (openai.ChoiceLogprobs, string, error) {
	converted := openai.ChoiceLogprobs{Content: make([]openai.TokenLogprob, 0, len(items))}
	var text strings.Builder
	for _, item := range items {
		if item.Text == nil || *item.Text == "" || len(item.TokenIDs) != 1 || item.TokenIDs[0] < 0 || item.Logprobs == nil || len(*item.Logprobs) != 1 || !finiteProbability((*item.Logprobs)[0]) {
			return openai.ChoiceLogprobs{}, "", errors.New("invalid Cohere logprob item")
		}
		text.WriteString(*item.Text)
		converted.Content = append(converted.Content, openai.TokenLogprob{Token: *item.Text, Logprob: (*item.Logprobs)[0], Bytes: tokenBytes(*item.Text), TopLogprobs: []openai.TopLogprob{}})
	}
	return converted, text.String(), nil
}

func chatCompletionLogprobChunkPayload(id, model string, created int64, role, content string, logprobs *openai.ChoiceLogprobs) (string, error) {
	delta := map[string]any{"content": content}
	if role != "" {
		delta["role"] = role
	}
	payload, err := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil, "logprobs": logprobs}},
	})
	return string(payload), err
}

func cohereChatError(param, message string) error {
	return &Error{Class: FailureClientRequest, Provider: "cohere", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_parameter", Param: param, Err: errors.New(message)}
}

func cohereChatText(value any) (string, error) {
	switch content := value.(type) {
	case string:
		if content == "" {
			return "", errors.New("Cohere chat message content is empty")
		}
		return content, nil
	case []any:
		var text strings.Builder
		for _, item := range content {
			block, ok := item.(map[string]any)
			if !ok || block["type"] != "text" || len(block) != 2 {
				return "", errors.New("Cohere chat accepts text content blocks only")
			}
			value, ok := block["text"].(string)
			if !ok || value == "" {
				return "", errors.New("Cohere chat text content is invalid")
			}
			text.WriteString(value)
		}
		if text.Len() == 0 {
			return "", errors.New("Cohere chat message content is empty")
		}
		return text.String(), nil
	default:
		return "", errors.New("Cohere chat message content must be text")
	}
}

func cohereFinishReason(value string) (string, error) {
	switch strings.ToUpper(value) {
	case "COMPLETE", "STOP_SEQUENCE":
		return "stop", nil
	case "MAX_TOKENS":
		return "length", nil
	default:
		return "", errors.New("invalid Cohere finish reason")
	}
}

func cohereChatUsage(value *cohereChatUsageResponse) (openai.Usage, error) {
	if value == nil {
		return openai.Usage{}, errors.New("Cohere chat response is missing usage")
	}
	input, output := (*int)(nil), (*int)(nil)
	if value.BilledUnits != nil {
		input, output = value.BilledUnits.InputTokens, value.BilledUnits.OutputTokens
	} else if value.Tokens != nil {
		input, output = value.Tokens.InputTokens, value.Tokens.OutputTokens
	}
	if input == nil || output == nil || *input < 0 || *output < 0 || *input > int(^uint(0)>>1)-*output {
		return openai.Usage{}, errors.New("invalid Cohere chat usage")
	}
	return openai.Usage{PromptTokens: *input, CompletionTokens: *output, TotalTokens: *input + *output}, nil
}

func decodeCohereChatResponse(reader io.Reader, target *cohereChatResponse) error {
	payload, err := io.ReadAll(io.LimitReader(reader, maxChatCompletionResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxChatCompletionResponseBytes {
		return errors.New("Cohere chat response exceeds limit")
	}
	return json.Unmarshal(payload, target)
}

func (Cohere) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, rejectParameters("cohere", parameterCheck{"responses", true})
}

func (Cohere) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	if err := rejectParameters("cohere", parameterCheck{"metadata", request.Metadata != nil}, parameterCheck{"output_dtype", request.OutputDType != ""}); err != nil {
		return err
	}
	input, err := openai.InspectEmbeddingInput(request.Input)
	if err != nil || input.Tokenized() || len(input.Texts) > maxCohereEmbeddingInputs {
		return cohereEmbeddingError("input", "Cohere v2 embed requires at most 96 text inputs")
	}
	if request.InputType != "search_document" && request.InputType != "search_query" && request.InputType != "classification" && request.InputType != "clustering" {
		return cohereEmbeddingError("input_type", "Cohere v2 embed requires a supported input_type")
	}
	if request.User != "" {
		return rejectParameters("cohere", parameterCheck{"user", true})
	}
	if request.EncodingFormat != "" && request.EncodingFormat != "float" && request.EncodingFormat != "base64" {
		return cohereEmbeddingError("encoding_format", "encoding_format must be float or base64")
	}
	if request.Dimensions != nil && *request.Dimensions != 256 && *request.Dimensions != 512 && *request.Dimensions != 1024 && *request.Dimensions != 1536 {
		return cohereEmbeddingError("dimensions", "Cohere output dimensions must be 256, 512, 1024, or 1536")
	}
	if strings.TrimSpace(request.Model) == "" || len(request.Model) > 512 {
		return cohereEmbeddingError("model", "model is invalid")
	}
	return nil
}

func (p Cohere) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := p.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	texts, _ := openai.EmbeddingInputStrings(request.Input)
	body, err := json.Marshal(cohereEmbeddingRequest{
		Model: request.Model, Texts: texts, InputType: request.InputType,
		EmbeddingTypes: []string{"float"}, OutputDimension: request.Dimensions,
	})
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if len(body) > openai.MaxInferenceBodyBytes {
		return openai.EmbeddingResponse{}, cohereEmbeddingError("input", "embedding request exceeds limit")
	}
	endpoint, err := cohereEndpoint(p.baseURL, "v2/embed")
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return openai.EmbeddingResponse{}, responseStatusError("cohere", response)
	}
	var upstream struct {
		Embeddings struct {
			Float [][]float64 `json:"float"`
		} `json:"embeddings"`
		Meta *struct {
			BilledUnits *struct {
				InputTokens *int `json:"input_tokens"`
			} `json:"billed_units"`
		} `json:"meta"`
	}
	if err := decodeEmbeddingResponse(response.Body, &upstream); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	result := openai.EmbeddingResponse{Object: "list", Model: request.Model, Data: make([]openai.Embedding, len(upstream.Embeddings.Float))}
	for index, vector := range upstream.Embeddings.Float {
		result.Data[index] = openai.Embedding{Object: "embedding", Embedding: vector, Index: index}
	}
	floatRequest := request
	floatRequest.EncodingFormat = "float"
	if err := validateEmbeddingVectors(floatRequest, result.Data); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if request.EncodingFormat == "base64" {
		for index := range result.Data {
			encoded, err := encodeCohereEmbedding(result.Data[index].Embedding)
			if err != nil {
				return openai.EmbeddingResponse{}, err
			}
			result.Data[index].Embedding = nil
			result.Data[index].EmbeddingBase64 = encoded
		}
		if err := validateEmbeddingVectors(request, result.Data); err != nil {
			return openai.EmbeddingResponse{}, err
		}
	}
	tokens := openai.EmbeddingInputTokenCount(request.Input)
	if upstream.Meta != nil && upstream.Meta.BilledUnits != nil && upstream.Meta.BilledUnits.InputTokens != nil {
		tokens = *upstream.Meta.BilledUnits.InputTokens
		if tokens < 0 {
			return openai.EmbeddingResponse{}, errors.New("invalid Cohere embedding usage")
		}
		result.UsageReported = true
	}
	result.Usage = openai.Usage{PromptTokens: tokens, TotalTokens: tokens}
	return result, nil
}

func cohereEmbeddingError(param, message string) error {
	return &Error{Class: FailureClientRequest, Provider: "cohere", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_parameter", Param: param, Err: errors.New(message)}
}

func encodeCohereEmbedding(values []float64) (string, error) {
	encoded := make([]byte, len(values)*4)
	for index, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.IsInf(float64(float32(value)), 0) {
			return "", errors.New("invalid Cohere embedding value")
		}
		binary.LittleEndian.PutUint32(encoded[index*4:], math.Float32bits(float32(value)))
	}
	return base64.StdEncoding.EncodeToString(encoded), nil
}

func (Cohere) ValidateRerankParameters(request openai.RerankRequest) error {
	if err := rejectParameters("cohere",
		parameterCheck{"rank_fields", len(request.RankFields) > 0},
		parameterCheck{"max_chunks_per_doc", request.MaxChunksPerDoc != nil},
		parameterCheck{"truncate", request.Truncate != ""},
	); err != nil {
		return err
	}
	for _, document := range request.Documents {
		_, ok := document.(string)
		if !ok {
			return &Error{Class: FailureClientRequest, Provider: "cohere", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "documents", Err: errors.New("Cohere v2 rerank requires string documents")}
		}
	}
	return nil
}

func (p Cohere) Rerank(ctx context.Context, request openai.RerankRequest) (openai.RerankResponse, error) {
	if err := p.ValidateRerankParameters(request); err != nil {
		return openai.RerankResponse{}, err
	}
	documents := make([]string, len(request.Documents))
	for index, document := range request.Documents {
		text := document.(string)
		documents[index] = text
	}
	body, err := json.Marshal(cohereRerankRequest{Model: request.Model, Query: request.Query, Documents: documents, TopN: request.TopN, MaxTokensPerDoc: request.MaxTokensPerDoc})
	if err != nil {
		return openai.RerankResponse{}, err
	}
	endpoint, err := cohereEndpoint(p.baseURL, "v2/rerank")
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.RerankResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return openai.RerankResponse{}, responseStatusError("cohere", response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxCohereRerankResponseBytes+1))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	if len(payload) > maxCohereRerankResponseBytes {
		return openai.RerankResponse{}, errors.New("Cohere rerank response exceeds limit")
	}
	var result openai.RerankResponse
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&result); err != nil {
		return openai.RerankResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return openai.RerankResponse{}, errors.New("Cohere rerank response contains trailing data")
	}
	return result, nil
}

func cohereEndpoint(baseURL, suffix string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("invalid Cohere base URL")
	}
	path := strings.TrimRight(base.Path, "/")
	if strings.HasSuffix(path, "/v1") || strings.HasSuffix(path, "/v2") {
		path = path[:len(path)-3]
	}
	base.Path = strings.TrimRight(path, "/") + "/" + suffix
	return base.String(), nil
}
