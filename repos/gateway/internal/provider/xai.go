package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

// XAI exposes the provider operations whose wire contracts are validated here.
type XAI struct {
	compatible OpenAICompatible
}

func NewXAI(baseURL, apiKey string, stream bool) XAI {
	compatible := NewOpenAICompatible(baseURL, apiKey, stream)
	compatible.errorProvider = "xai"
	return XAI{compatible: compatible}
}

func (XAI) SupportsResponses() bool        { return true }
func (XAI) SupportsEmbeddings() bool       { return true }
func (XAI) SupportsTools() bool            { return true }
func (XAI) SupportsStructuredOutput() bool { return true }
func (XAI) SupportsVision() bool           { return true }
func (XAI) SupportsWebSearch() bool        { return true }
func (XAI) SupportsImageGeneration() bool  { return true }
func (XAI) SupportsImageEdit() bool        { return true }

func (x XAI) GenerateImage(ctx context.Context, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	if message := request.Validate(); message != "" {
		return openai.ImageGenerationResponse{}, xaiParameterError("", message)
	}
	if request.Quality != "" && request.Quality != "auto" && request.Quality != "low" && request.Quality != "medium" {
		return openai.ImageGenerationResponse{}, xaiParameterError("quality", "quality must be auto, low, or medium")
	}
	if request.Resolution != "" && request.Resolution != "1K" && request.Resolution != "2K" {
		return openai.ImageGenerationResponse{}, xaiParameterError("resolution", "resolution must be 1K or 2K")
	}
	if err := rejectParameters("xai",
		parameterCheck{"size", request.Size != ""},
		parameterCheck{"style", request.Style != ""},
		parameterCheck{"user", request.User != ""},
		parameterCheck{"background", request.Background != ""},
		parameterCheck{"output_format", request.OutputFormat != ""},
		parameterCheck{"output_compression", request.OutputCompression != nil},
		parameterCheck{"seed", request.Seed != nil},
	); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	body, err := json.Marshal(struct {
		Model          string `json:"model"`
		Prompt         string `json:"prompt"`
		N              *int   `json:"n,omitempty"`
		Quality        string `json:"quality,omitempty"`
		ResponseFormat string `json:"response_format,omitempty"`
		Resolution     string `json:"resolution,omitempty"`
		AspectRatio    string `json:"aspect_ratio,omitempty"`
	}{request.Model, request.Prompt, request.N, request.Quality, request.ResponseFormat, strings.ToLower(request.Resolution), request.AspectRatio})
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	return x.compatible.postImageJSON(ctx, "images/generations", body, request, true)
}

func (x XAI) EditImage(ctx context.Context, request openai.ImageEditRequest) (openai.ImageGenerationResponse, error) {
	if message := request.Validate(); message != "" {
		return openai.ImageGenerationResponse{}, xaiParameterError("", message)
	}
	if len(request.Images) > 5 {
		return openai.ImageGenerationResponse{}, xaiParameterError("images", "xAI image edits accept at most five source images")
	}
	if request.Quality != "" && request.Quality != "auto" && request.Quality != "low" && request.Quality != "medium" {
		return openai.ImageGenerationResponse{}, xaiParameterError("quality", "quality must be auto, low, or medium")
	}
	if err := rejectParameters("xai",
		parameterCheck{"mask", request.Mask != nil},
		parameterCheck{"size", request.Size != ""},
		parameterCheck{"user", request.User != ""},
		parameterCheck{"background", request.Background != ""},
		parameterCheck{"output_format", request.OutputFormat != ""},
		parameterCheck{"output_compression", request.OutputCompression != nil},
	); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	type source struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	sources := make([]source, len(request.Images))
	for index, image := range request.Images {
		sources[index] = source{Type: "image_url", URL: "data:" + image.MediaType + ";base64," + image.Data}
	}
	body := struct {
		Model          string   `json:"model"`
		Prompt         string   `json:"prompt"`
		Image          *source  `json:"image,omitempty"`
		Images         []source `json:"images,omitempty"`
		N              *int     `json:"n,omitempty"`
		Quality        string   `json:"quality,omitempty"`
		ResponseFormat string   `json:"response_format,omitempty"`
	}{Model: request.Model, Prompt: request.Prompt, N: request.N, Quality: request.Quality, ResponseFormat: request.ResponseFormat}
	if len(sources) == 1 {
		body.Image = &sources[0]
	} else {
		body.Images = sources
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	return x.compatible.postImageJSON(ctx, "images/edits", payload, request.GenerationRequest(), true)
}

func (x XAI) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if !validXAIServiceTier(request.ServiceTier) {
		return xaiParameterError("service_tier", "service_tier must be default or priority")
	}
	if !validXAIReasoningEffort(request.ReasoningEffort) {
		return xaiParameterError("reasoning_effort", "reasoning_effort must be none, low, medium, high, or xhigh")
	}
	if request.TopLogprobs != nil && (*request.TopLogprobs < 0 || *request.TopLogprobs > 8 || request.Logprobs == nil || !*request.Logprobs) {
		return xaiParameterError("top_logprobs", "top_logprobs must be between 0 and 8 and requires logprobs=true")
	}
	if err := rejectParameters("xai",
		parameterCheck{"metadata", request.Metadata != nil},
		parameterCheck{"store", request.Store != nil},
		parameterCheck{"modalities", request.Modalities != nil},
		parameterCheck{"audio", request.Audio != nil},
		parameterCheck{"safe_prompt", request.SafePrompt != nil},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""},
		parameterCheck{"prompt_mode", request.PromptMode != ""},
		parameterCheck{"prediction", request.Prediction != nil},
		parameterCheck{"verbosity", request.Verbosity != ""},
		parameterCheck{"web_fetch_options", request.WebFetchOptions != nil},
		parameterCheck{"min_p", request.MinP != nil},
		parameterCheck{"top_k", request.TopK != nil},
		parameterCheck{"top_a", request.TopA != nil},
		parameterCheck{"repetition_penalty", request.RepetitionPenalty != nil},
		parameterCheck{"logit_bias", request.LogitBias != nil},
	); err != nil {
		return err
	}
	return x.compatible.ValidateChatParameters(request)
}

func (x XAI) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := x.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return x.compatible.ChatCompletions(ctx, request)
}

func (x XAI) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := x.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return x.compatible.StreamChatCompletions(ctx, request, write)
}

func (x XAI) ValidateResponseParameters(request openai.ResponseRequest) error {
	if request.Background {
		return xaiUnsupportedParameter("background")
	}
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "xai", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if !validXAIServiceTier(request.ServiceTier) {
		return xaiParameterError("service_tier", "service_tier must be default or priority")
	}
	if request.TopLogprobs != nil {
		return xaiUnsupportedParameter("top_logprobs")
	}
	if request.FrequencyPenalty != nil {
		return xaiUnsupportedParameter("frequency_penalty")
	}
	if request.PresencePenalty != nil {
		return xaiUnsupportedParameter("presence_penalty")
	}
	if len(request.Metadata) > 0 {
		return xaiUnsupportedParameter("metadata")
	}
	if request.Truncation != nil {
		return xaiUnsupportedParameter("truncation")
	}
	if request.SafetyIdentifier != "" {
		return xaiUnsupportedParameter("safety_identifier")
	}
	if request.MaxToolCalls != nil {
		return xaiUnsupportedParameter("max_tool_calls")
	}
	if _, supplied := openai.ResponseTextVerbosity(request.Text); supplied {
		return xaiUnsupportedParameter("text.verbosity")
	}
	if reasoning := request.Reasoning; reasoning != nil {
		if reasoning.Summary != nil || reasoning.GenerateSummary != nil || reasoning.Context != nil || reasoning.Mode != nil {
			return xaiUnsupportedParameter("reasoning")
		}
		if reasoning.Effort != nil && !validXAIReasoningEffort(*reasoning.Effort) {
			return xaiParameterError("reasoning.effort", "reasoning effort must be low, medium, high, or xhigh")
		}
	}
	return x.compatible.ValidateResponseParameters(request)
}

func (x XAI) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if err := x.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return x.compatible.Responses(ctx, request)
}

func (x XAI) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if err := x.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return x.compatible.StreamResponses(ctx, request, write)
}

func (x XAI) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	return rejectParameters("xai",
		parameterCheck{"metadata", request.Metadata != nil},
		parameterCheck{"input_type", request.InputType != ""},
		parameterCheck{"output_dtype", request.OutputDType != ""},
	)
}

func (x XAI) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := x.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	return x.compatible.Embeddings(ctx, request)
}

func (x XAI) RetrieveResponse(ctx context.Context, id string) (openai.ResponseResponse, error) {
	return x.compatible.RetrieveResponse(ctx, id)
}

func (x XAI) ListResponseInputItems(ctx context.Context, id string, options ResponseInputItemsOptions) (openai.ResponseInputItemList, error) {
	return x.compatible.ListResponseInputItems(ctx, id, options)
}

func (x XAI) DeleteResponse(ctx context.Context, id string) (openai.ResponseDeletion, error) {
	return x.compatible.deleteResponse(ctx, id, "response")
}

func (x XAI) CompactResponse(ctx context.Context, request openai.ResponseCompactRequest) (openai.CompactedResponse, error) {
	return x.compatible.CompactResponse(ctx, request)
}

func validXAIServiceTier(value string) bool {
	return value == "" || value == "default" || value == "priority"
}

func validXAIReasoningEffort(value string) bool {
	return value == "" || value == "none" || value == "low" || value == "medium" || value == "high" || value == "xhigh"
}

func xaiParameterError(param, message string) error {
	return &Error{Class: FailureClientRequest, Provider: "xai", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: param, Err: errors.New(message)}
}

func xaiUnsupportedParameter(param string) error {
	return &Error{Class: FailureClientRequest, Provider: "xai", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: param, Err: errors.New(param + " is not supported by this adapter")}
}
