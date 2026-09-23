package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"ai-gateway-gateway/internal/openai"
)

// Cerebras exposes the provider operations and optional fields whose native
// wire contracts are validated by this adapter.
type Cerebras struct {
	compatible OpenAICompatible
}

func NewCerebras(baseURL, apiKey string, stream bool) Cerebras {
	compatible := NewOpenAICompatible(baseURL, apiKey, stream)
	compatible.errorProvider = "cerebras"
	return Cerebras{compatible: compatible}
}

func (Cerebras) SupportsTools() bool            { return true }
func (Cerebras) SupportsStructuredOutput() bool { return true }
func (Cerebras) SupportsResponses() bool        { return false }

func (Cerebras) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, rejectParameters("cerebras", parameterCheck{"responses", true})
}

func (c Cerebras) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectChatModeration("cerebras", request); err != nil {
		return err
	}
	if err := rejectLegacyFunctionCalling("cerebras", request); err != nil {
		return err
	}
	if err := validateChatMessagePrefix("cerebras", request.Messages, false); err != nil {
		return err
	}
	if err := validateChatPromptCacheBreakpoints("cerebras", request, false); err != nil {
		return err
	}
	if err := validateChatReasoningContent("cerebras", request.Messages, false); err != nil {
		return err
	}
	if err := rejectToolCallMetadata("cerebras", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageRefusals("cerebras", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageAudio("cerebras", request.Messages); err != nil {
		return err
	}
	for _, message := range request.Messages {
		if len(message.Reasoning) > 0 {
			return rejectParameters("cerebras", parameterCheck{"messages.reasoning", true})
		}
	}
	for _, tool := range request.Tools {
		if tool.Type != "function" {
			return rejectParameters("cerebras", parameterCheck{"tools.type", true})
		}
	}
	if request.N != nil && *request.N != 1 {
		return unsupportedCerebrasParameter("n")
	}
	if request.ReasoningEffort != "" {
		switch request.ReasoningEffort {
		case "none", "low", "medium", "high":
		default:
			return &Error{Class: FailureClientRequest, Provider: "cerebras", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "reasoning_effort", Err: errors.New("reasoning_effort must be none, low, medium, or high")}
		}
	}
	if request.ServiceTier != "" {
		switch request.ServiceTier {
		case "auto", "default", "flex", "priority":
		default:
			return &Error{Class: FailureClientRequest, Provider: "cerebras", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "service_tier", Err: errUnsupportedServiceTier}
		}
	}
	if request.Stream && request.ResponseFormat != nil && request.ResponseFormat.Type == "json_object" {
		return &Error{Class: FailureClientRequest, Provider: "cerebras", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "response_format", Err: errors.New("json_object response format does not support streaming")}
	}
	if err := rejectParameters("cerebras",
		parameterCheck{"metadata", request.Metadata != nil}, parameterCheck{"store", request.Store != nil},
		parameterCheck{"modalities", request.Modalities != nil}, parameterCheck{"audio", request.Audio != nil},
		parameterCheck{"safe_prompt", request.SafePrompt != nil}, parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil}, parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""},
		parameterCheck{"prompt_mode", request.PromptMode != ""}, parameterCheck{"verbosity", request.Verbosity != ""},
		parameterCheck{"web_search_options", request.WebSearchOptions != nil}, parameterCheck{"web_fetch_options", request.WebFetchOptions != nil},
		parameterCheck{"min_p", request.MinP != nil}, parameterCheck{"top_k", request.TopK != nil},
		parameterCheck{"top_a", request.TopA != nil}, parameterCheck{"repetition_penalty", request.RepetitionPenalty != nil},
	); err != nil {
		return err
	}
	return c.compatible.ValidateChatParameters(request)
}

func (c Cerebras) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := c.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return c.compatible.chatCompletions(ctx, request, decodeCerebrasChatCompletionResponse, normalizeCerebrasChatStreamPayload)
}

func (c Cerebras) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := c.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return c.compatible.streamChatCompletions(ctx, request, write, normalizeCerebrasChatStreamPayload)
}

func decodeCerebrasChatCompletionResponse(reader io.Reader, target *openai.ChatCompletionResponse) error {
	payload, err := io.ReadAll(io.LimitReader(reader, maxChatCompletionResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxChatCompletionResponseBytes {
		return errors.New("chat completion response exceeds limit")
	}
	normalized, err := normalizeCerebrasChatPayload(payload, "message")
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, target)
}

func normalizeCerebrasChatStreamPayload(payload string) (string, error) {
	normalized, err := normalizeCerebrasChatPayload([]byte(payload), "delta")
	return string(normalized), err
}

func normalizeCerebrasChatPayload(payload []byte, messageField string) ([]byte, error) {
	normalized, err := normalizeChatReasoningAliasPayload("Cerebras", payload, messageField)
	if err != nil {
		return nil, err
	}
	return normalizeCerebrasServiceTier(normalized)
}

func normalizeCerebrasServiceTier(payload []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	rawUsed, found := envelope["service_tier_used"]
	if !found {
		return payload, nil
	}
	var used string
	if err := json.Unmarshal(rawUsed, &used); err != nil || used == "" {
		return nil, errors.New("provider returned invalid Cerebras service_tier_used")
	}
	switch used {
	case "priority", "default", "flex":
	default:
		return nil, errors.New("provider returned unsupported Cerebras service_tier_used")
	}
	if rawTier := envelope["service_tier"]; len(rawTier) > 0 && string(rawTier) != "null" {
		var tier string
		if err := json.Unmarshal(rawTier, &tier); err != nil {
			return nil, errors.New("provider returned invalid Cerebras service_tier")
		}
		if tier != "" && tier != "auto" && tier != used {
			return nil, errors.New("provider returned conflicting Cerebras service tiers")
		}
	}
	delete(envelope, "service_tier_used")
	envelope["service_tier"], _ = json.Marshal(used)
	return json.Marshal(envelope)
}

func normalizeChatReasoningAliasPayload(providerName string, payload []byte, messageField string) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	var choices []map[string]json.RawMessage
	rawChoices, hasChoices := envelope["choices"]
	if hasChoices {
		if len(rawChoices) == 0 {
			return nil, fmt.Errorf("provider returned invalid %s choices", providerName)
		}
		if err := json.Unmarshal(rawChoices, &choices); err != nil {
			return nil, err
		}
	}
	for _, choice := range choices {
		raw := choice[messageField]
		if len(raw) == 0 {
			continue
		}
		var message map[string]json.RawMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, err
		}
		reasoningRaw, found := message["reasoning"]
		if !found {
			continue
		}
		var reasoning string
		if err := json.Unmarshal(reasoningRaw, &reasoning); err != nil {
			return nil, fmt.Errorf("provider returned invalid %s reasoning", providerName)
		}
		if len(reasoning) > openai.MaxChatReasoningContentBytes {
			return nil, fmt.Errorf("provider %s reasoning exceeds limit", providerName)
		}
		if existing := message["reasoning_content"]; len(existing) > 0 {
			var value string
			if json.Unmarshal(existing, &value) != nil || value != reasoning {
				return nil, fmt.Errorf("provider returned conflicting %s reasoning", providerName)
			}
		}
		delete(message, "reasoning")
		message["reasoning_content"], _ = json.Marshal(reasoning)
		normalizedMessage, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		choice[messageField] = normalizedMessage
	}
	if hasChoices {
		normalizedChoices, err := json.Marshal(choices)
		if err != nil {
			return nil, err
		}
		envelope["choices"] = normalizedChoices
	}
	return json.Marshal(envelope)
}

func unsupportedCerebrasParameter(name string) error {
	return &Error{Class: FailureClientRequest, Provider: "cerebras", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: name, Err: fmt.Errorf("parameter %s is not supported by this adapter", name)}
}
