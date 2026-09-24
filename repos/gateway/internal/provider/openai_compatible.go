package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

type openAICompatibleChatRequest struct {
	openai.ChatGenerationOptions
	Model               string                         `json:"model"`
	Messages            []openai.Message               `json:"messages"`
	messagesOverride    any                            `json:"-"`
	nativeLogprobs      *int                           `json:"-"`
	nativeReasoning     *togetherReasoning             `json:"-"`
	Functions           []openai.FunctionDefinition    `json:"functions,omitempty"`
	FunctionCall        *openai.LegacyFunctionChoice   `json:"function_call,omitempty"`
	Tools               []openai.Tool                  `json:"tools,omitempty"`
	ToolChoice          any                            `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool                          `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      *openai.ResponseFormat         `json:"response_format,omitempty"`
	Stream              bool                           `json:"stream,omitempty"`
	StreamOptions       *openAICompatibleStreamOptions `json:"stream_options,omitempty"`
	MaxTokens           *int                           `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                           `json:"max_completion_tokens,omitempty"`
	Temperature         *float64                       `json:"temperature,omitempty"`
	TopP                *float64                       `json:"top_p,omitempty"`
	Stop                any                            `json:"stop,omitempty"`
	Seed                *int64                         `json:"seed,omitempty"`
	RandomSeed          *int64                         `json:"random_seed,omitempty"`
	UserID              string                         `json:"user_id,omitempty"`
	NativeThinking      *deepSeekThinking              `json:"thinking,omitempty"`
}

type deepSeekThinking struct {
	Type string `json:"type"`
}

type togetherReasoning struct {
	Enabled bool `json:"enabled"`
}

func (r openAICompatibleChatRequest) MarshalJSON() ([]byte, error) {
	type wire openAICompatibleChatRequest
	payload, err := json.Marshal(wire(r))
	if err != nil || r.messagesOverride == nil && r.nativeLogprobs == nil && r.nativeReasoning == nil {
		return payload, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, err
	}
	if r.messagesOverride != nil {
		messages, err := json.Marshal(r.messagesOverride)
		if err != nil {
			return nil, err
		}
		object["messages"] = messages
	}
	if r.nativeLogprobs != nil {
		object["logprobs"], _ = json.Marshal(*r.nativeLogprobs)
		delete(object, "top_logprobs")
	}
	if r.nativeReasoning != nil {
		object["reasoning"], _ = json.Marshal(r.nativeReasoning)
		delete(object, "reasoning_effort")
	}
	return json.Marshal(object)
}

type openAICompatibleResponseRequest struct {
	Metadata             map[string]string             `json:"metadata,omitempty"`
	ContextManagement    []openai.ResponseContextEntry `json:"context_management,omitempty"`
	Moderation           *openai.ProviderModeration    `json:"moderation,omitempty"`
	TopLogprobs          *int                          `json:"top_logprobs,omitempty"`
	Truncation           *string                       `json:"truncation,omitempty"`
	Reasoning            *openai.ResponseReasoning     `json:"reasoning,omitempty"`
	Store                *bool                         `json:"store,omitempty"`
	Include              []string                      `json:"include,omitempty"`
	Model                string                        `json:"model"`
	Input                any                           `json:"input"`
	Instructions         string                        `json:"instructions,omitempty"`
	Tools                []openai.ResponseTool         `json:"tools,omitempty"`
	ToolChoice           any                           `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool                         `json:"parallel_tool_calls,omitempty"`
	Text                 any                           `json:"text,omitempty"`
	PreviousResponse     string                        `json:"previous_response_id,omitempty"`
	User                 string                        `json:"user,omitempty"`
	SafetyIdentifier     string                        `json:"safety_identifier,omitempty"`
	PromptCacheKey       string                        `json:"prompt_cache_key,omitempty"`
	PromptCacheOptions   *openai.PromptCacheOptions    `json:"prompt_cache_options,omitempty"`
	PromptCacheRetention string                        `json:"prompt_cache_retention,omitempty"`
	ServiceTier          string                        `json:"service_tier,omitempty"`
	Background           bool                          `json:"background,omitempty"`
	Stream               bool                          `json:"stream,omitempty"`
	StreamOptions        *openai.ResponseStreamOptions `json:"stream_options,omitempty"`
	MaxOutputTokens      *int                          `json:"max_output_tokens,omitempty"`
	Temperature          *float64                      `json:"temperature,omitempty"`
	TopP                 *float64                      `json:"top_p,omitempty"`
	FrequencyPenalty     *float64                      `json:"frequency_penalty,omitempty"`
	PresencePenalty      *float64                      `json:"presence_penalty,omitempty"`
	MaxToolCalls         *int                          `json:"max_tool_calls,omitempty"`
}

type openAICompatibleCompactRequest struct {
	Model        string `json:"model"`
	Input        any    `json:"input"`
	Instructions string `json:"instructions,omitempty"`
}

type openAICompatibleCompletionRequest struct {
	Model            string                         `json:"model"`
	Prompt           any                            `json:"prompt,omitempty"`
	BestOf           *int                           `json:"best_of,omitempty"`
	Echo             *bool                          `json:"echo,omitempty"`
	FrequencyPenalty *float64                       `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]int                 `json:"logit_bias,omitempty"`
	Logprobs         *int                           `json:"logprobs,omitempty"`
	MaxTokens        *int                           `json:"max_tokens,omitempty"`
	N                *int                           `json:"n,omitempty"`
	PresencePenalty  *float64                       `json:"presence_penalty,omitempty"`
	Seed             *int64                         `json:"seed,omitempty"`
	Stop             any                            `json:"stop,omitempty"`
	Stream           bool                           `json:"stream"`
	StreamOptions    *openAICompatibleStreamOptions `json:"stream_options,omitempty"`
	Suffix           string                         `json:"suffix,omitempty"`
	Temperature      *float64                       `json:"temperature,omitempty"`
	TopP             *float64                       `json:"top_p,omitempty"`
	User             string                         `json:"user,omitempty"`
}

type openAICompatibleStreamOptions struct {
	IncludeUsage       bool  `json:"include_usage"`
	IncludeObfuscation *bool `json:"include_obfuscation,omitempty"`
}

type openAICompatibleEmbeddingRequest struct {
	Model           string            `json:"model"`
	Input           any               `json:"input"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	EncodingFormat  string            `json:"encoding_format,omitempty"`
	Dimensions      *int              `json:"dimensions,omitempty"`
	OutputDimension *int              `json:"output_dimension,omitempty"`
	OutputDType     string            `json:"output_dtype,omitempty"`
	User            string            `json:"user,omitempty"`
}

type openAICompatibleRerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []any    `json:"documents"`
	TopN            *int     `json:"top_n,omitempty"`
	RankFields      []string `json:"rank_fields,omitempty"`
	ReturnDocuments *bool    `json:"return_documents,omitempty"`
	MaxChunksPerDoc *int     `json:"max_chunks_per_doc,omitempty"`
	MaxTokensPerDoc *int     `json:"max_tokens_per_doc,omitempty"`
}

type openAICompatibleModerationRequest struct {
	Model    string            `json:"model,omitempty"`
	Input    any               `json:"input"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type OpenAICompatible struct {
	baseURL               string
	apiKey                string
	errorProvider         string
	exactChatUsage        bool
	exactResponseUsage    bool
	exactEmbeddingUsage   bool
	exactCompletionUsage  bool
	upstreamStream        bool
	rerankPath            string
	completionStreamUsage bool
	supportsSafePrompt    bool
	supportsPromptMode    bool
	supportsMessagePrefix bool
	realtimeURL           func(string) (*url.URL, error)
	realtimeAuth          func(context.Context) (http.Header, error)
	chatMessages          func(openai.ChatCompletionRequest) (any, error)
	client                *http.Client
}

func NewOpenAICompatible(baseURL string, apiKey string, upstreamStream bool) OpenAICompatible {
	return NewOpenAICompatibleWithRerankPath(baseURL, apiKey, upstreamStream, "")
}

func NewOpenAICompatibleWithRerankPath(baseURL string, apiKey string, upstreamStream bool, rerankPath string) OpenAICompatible {
	return OpenAICompatible{
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         apiKey,
		errorProvider:  "openai-compatible",
		upstreamStream: upstreamStream,
		rerankPath:     rerankPath,
		client:         newProviderHTTPClient(180 * time.Second),
	}
}

func (p OpenAICompatible) providerName() string {
	if p.errorProvider == "" {
		return "openai-compatible"
	}
	return p.errorProvider
}

func (p OpenAICompatible) mapChatParameters(request *openAICompatibleChatRequest) {
	switch p.providerName() {
	case "mistral":
		request.RandomSeed = request.Seed
		request.Seed = nil
		if request.MaxCompletionTokens != nil {
			request.MaxTokens = request.MaxCompletionTokens
			request.MaxCompletionTokens = nil
		}
	case "deepseek":
		if request.MaxCompletionTokens != nil {
			request.MaxTokens = request.MaxCompletionTokens
			request.MaxCompletionTokens = nil
		}
		request.UserID = request.User
		request.User = ""
		thinkingType := "disabled"
		if request.Thinking != nil {
			thinkingType = request.Thinking.Type
		} else if request.ReasoningEffort != "" && request.ReasoningEffort != "none" {
			thinkingType = "enabled"
		}
		request.NativeThinking = &deepSeekThinking{Type: thinkingType}
	case "nvidia-nim":
		if request.MaxCompletionTokens != nil {
			request.MaxTokens = request.MaxCompletionTokens
			request.MaxCompletionTokens = nil
		}
	case "together":
		if request.MaxCompletionTokens != nil {
			request.MaxTokens = request.MaxCompletionTokens
			request.MaxCompletionTokens = nil
		}
		if request.Logprobs != nil {
			if *request.Logprobs {
				value := 0
				request.nativeLogprobs = &value
			}
			request.Logprobs = nil
			request.TopLogprobs = nil
		}
		if request.ReasoningEffort == "none" {
			request.nativeReasoning = &togetherReasoning{Enabled: false}
			request.ReasoningEffort = ""
		}
	}
}

func (p OpenAICompatible) mapEmbeddingDimensions(request *openAICompatibleEmbeddingRequest) {
	if p.providerName() == "mistral" {
		request.OutputDimension = request.Dimensions
		request.Dimensions = nil
	}
}

func (p OpenAICompatible) Rerank(ctx context.Context, request openai.RerankRequest) (openai.RerankResponse, error) {
	if err := p.ValidateRerankParameters(request); err != nil {
		return openai.RerankResponse{}, err
	}
	body, err := json.Marshal(openAICompatibleRerankRequest{
		Model: request.Model, Query: request.Query, Documents: request.Documents, TopN: request.TopN,
		RankFields: request.RankFields, ReturnDocuments: request.ReturnDocuments,
		MaxChunksPerDoc: request.MaxChunksPerDoc, MaxTokensPerDoc: request.MaxTokensPerDoc,
	})
	if err != nil {
		return openai.RerankResponse{}, err
	}
	url := providerURL(p.baseURL, "rerank")
	if p.rerankPath != "" {
		url = strings.TrimRight(p.baseURL, "/")
		if index := strings.Index(url, "://"); index >= 0 {
			if slash := strings.Index(url[index+3:], "/"); slash >= 0 {
				url = url[:index+3+slash]
			}
		}
		url += p.rerankPath
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.RerankResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.RerankResponse{}, responseStatusError(p.providerName(), resp)
	}
	var response openai.RerankResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&response); err != nil {
		return openai.RerankResponse{}, err
	}
	return response, nil
}

func (p OpenAICompatible) ValidateRerankParameters(request openai.RerankRequest) error {
	return rejectParameters(p.providerName(), parameterCheck{"truncate", request.Truncate != ""})
}

func (p OpenAICompatible) Moderations(ctx context.Context, request openai.ModerationRequest) (openai.ModerationResponse, error) {
	if err := p.ValidateModerationParameters(request); err != nil {
		return openai.ModerationResponse{}, err
	}
	body, err := json.Marshal(openAICompatibleModerationRequest{Model: request.Model, Input: request.Input, Metadata: request.Metadata})
	if err != nil {
		return openai.ModerationResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "moderations"), bytes.NewReader(body))
	if err != nil {
		return openai.ModerationResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ModerationResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.ModerationResponse{}, responseStatusError(p.providerName(), resp)
	}
	response, err := decodeModerationResponse(resp.Body)
	if err != nil {
		return openai.ModerationResponse{}, err
	}
	p.normalizeModerationResponse(&response)
	info, err := openai.InspectModerationInput(request.Input)
	if err != nil {
		return openai.ModerationResponse{}, err
	}
	if err := validateModerationResponse(response, info.ResultCount); err != nil {
		return openai.ModerationResponse{}, err
	}
	return response, nil
}

func (p OpenAICompatible) normalizeModerationResponse(response *openai.ModerationResponse) {
	if p.providerName() != "mistral" {
		return
	}
	for index := range response.Results {
		result := &response.Results[index]
		if len(result.CategoryAppliedInputTypes) != 0 {
			continue
		}
		result.CategoryAppliedInputTypes = make(map[string][]string, len(result.Categories))
		for category := range result.Categories {
			result.CategoryAppliedInputTypes[category] = []string{"text"}
		}
	}
}

func (OpenAICompatible) SupportsMCP() bool    { return true }
func (OpenAICompatible) SupportsVision() bool { return true }

func (p OpenAICompatible) Completions(ctx context.Context, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	return p.completion(ctx, request, false, nil)
}

func (p OpenAICompatible) StreamCompletions(ctx context.Context, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	if !p.upstreamStream {
		return openai.CompletionResponse{}, ErrStreamingUnsupported
	}
	return p.completion(ctx, request, true, write)
}

func (p OpenAICompatible) completion(ctx context.Context, request openai.CompletionRequest, stream bool, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	if err := p.ValidateCompletionParameters(request); err != nil {
		return openai.CompletionResponse{}, err
	}
	upstream := openAICompatibleCompletionRequest{
		Model: request.Model, Prompt: request.Prompt, BestOf: request.BestOf, Echo: request.Echo,
		FrequencyPenalty: request.FrequencyPenalty, LogitBias: request.LogitBias, Logprobs: request.Logprobs,
		MaxTokens: request.MaxTokens, N: request.N, PresencePenalty: request.PresencePenalty, Seed: request.Seed,
		Stop: request.Stop, Stream: stream, Suffix: request.Suffix, Temperature: request.Temperature, TopP: request.TopP, User: request.User,
	}
	if stream && p.completionStreamUsage {
		upstream.StreamOptions = &openAICompatibleStreamOptions{IncludeUsage: true}
	}
	body, err := json.Marshal(upstream)
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "completions"), bytes.NewReader(body))
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.CompletionResponse{}, responseStatusError(p.providerName(), resp)
	}
	var response openai.CompletionResponse
	if stream {
		response, err = streamCompletionData(resp.Body, request, write)
	} else {
		response, err = decodeCompletionResponse(resp.Body)
	}
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	if p.exactCompletionUsage && (!response.UsageReported || response.Usage.TotalTokens != response.Usage.PromptTokens+response.Usage.CompletionTokens) {
		return openai.CompletionResponse{}, fmt.Errorf("%s Completions requires exact prompt, completion and total token usage", p.providerName())
	}
	return response, nil
}

func streamCompletionData(body io.Reader, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	response := openai.CompletionResponse{}
	objectSeen, createdSeen, modelSeen := false, false, false
	err := scanSSEData(&responseStreamReader{source: body, remaining: maxResponseStreamBytes}, func(payload string) error {
		if payload == "[DONE]" {
			return io.EOF
		}
		var chunk struct {
			ID                string                    `json:"id"`
			Object            string                    `json:"object"`
			Created           *int64                    `json:"created"`
			Model             string                    `json:"model"`
			SystemFingerprint string                    `json:"system_fingerprint"`
			Choices           []openai.CompletionChoice `json:"choices"`
			Usage             *openai.Usage             `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return err
		}
		if chunk.ID != "" {
			if response.ID != "" && response.ID != chunk.ID {
				return errors.New("provider changed completion stream ID")
			}
			response.ID = chunk.ID
		}
		if chunk.Object != "" {
			if chunk.Object != "text_completion" || objectSeen && response.Object != chunk.Object {
				return errors.New("provider returned invalid completion stream object")
			}
			response.Object = chunk.Object
			objectSeen = true
		}
		if chunk.Created != nil {
			if *chunk.Created < 0 || createdSeen && response.Created != *chunk.Created {
				return errors.New("provider returned invalid completion stream timestamp")
			}
			response.Created = *chunk.Created
			createdSeen = true
		}
		if chunk.Model != "" {
			if modelSeen && response.Model != chunk.Model {
				return errors.New("provider changed completion stream model")
			}
			response.Model = chunk.Model
			modelSeen = true
		}
		if chunk.SystemFingerprint != "" {
			response.SystemFingerprint = chunk.SystemFingerprint
		}
		if chunk.Usage != nil {
			if err := validateCompletionUsage(*chunk.Usage); err != nil {
				return err
			}
			response.Usage = *chunk.Usage
			reported, err := completeChatUsageFields([]byte(payload))
			if err != nil {
				return err
			}
			response.UsageReported = reported
		}
		for _, choice := range chunk.Choices {
			if choice.Index < 0 || choice.Index >= 128 {
				return errors.New("provider returned invalid completion stream choice index")
			}
			if err := validateCompletionLogprobs(choice.Logprobs); err != nil {
				return err
			}
			for len(response.Choices) <= choice.Index {
				response.Choices = append(response.Choices, openai.CompletionChoice{Index: len(response.Choices)})
			}
			current := &response.Choices[choice.Index]
			current.Text += choice.Text
			if choice.FinishReason != "" {
				current.FinishReason = choice.FinishReason
			}
			if choice.Logprobs != nil {
				if current.Logprobs == nil {
					current.Logprobs = &openai.CompletionLogprobs{}
				}
				current.Logprobs.TextOffset = append(current.Logprobs.TextOffset, choice.Logprobs.TextOffset...)
				current.Logprobs.TokenLogprobs = append(current.Logprobs.TokenLogprobs, choice.Logprobs.TokenLogprobs...)
				current.Logprobs.Tokens = append(current.Logprobs.Tokens, choice.Logprobs.Tokens...)
				current.Logprobs.TopLogprobs = append(current.Logprobs.TopLogprobs, choice.Logprobs.TopLogprobs...)
			}
		}
		if write != nil {
			return write(payload)
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return openai.CompletionResponse{}, err
	}
	if !objectSeen || !createdSeen || !modelSeen {
		return openai.CompletionResponse{}, errors.New("provider returned incomplete completion stream identity")
	}
	if err := validateCompletionResult(response, request); err != nil {
		return openai.CompletionResponse{}, err
	}
	return response, nil
}

func (p OpenAICompatible) CompactResponse(ctx context.Context, request openai.ResponseCompactRequest) (openai.CompactedResponse, error) {
	body, err := json.Marshal(openAICompatibleCompactRequest{Model: request.Model, Input: request.Input, Instructions: request.Instructions})
	if err != nil {
		return openai.CompactedResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "responses/compact"), bytes.NewReader(body))
	if err != nil {
		return openai.CompactedResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.CompactedResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.CompactedResponse{}, responseStatusError(p.providerName(), resp)
	}
	return decodeCompactedResponse(resp.Body)
}

func (p OpenAICompatible) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return p.chatCompletions(ctx, request, decodeChatCompletionResponse, nil)
}

func (p OpenAICompatible) chatCompletions(ctx context.Context, request openai.ChatCompletionRequest, decodeResponse func(io.Reader, *openai.ChatCompletionResponse) error, normalizeStream func(string) (string, error)) (openai.ChatCompletionResponse, error) {
	var messages any
	if p.chatMessages != nil {
		var err error
		messages, err = p.chatMessages(request)
		if err != nil {
			return openai.ChatCompletionResponse{}, err
		}
	}
	upstreamRequest := openAICompatibleChatRequest{
		ChatGenerationOptions: request.ChatGenerationOptions,
		Model:                 request.Model, Messages: request.Messages, messagesOverride: messages, Functions: request.Functions, FunctionCall: request.FunctionCall, Tools: request.Tools,
		ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls,
		ResponseFormat: request.ResponseFormat, Stream: request.Stream && p.upstreamStream,
		MaxTokens: request.MaxTokens, MaxCompletionTokens: request.MaxCompletionTokens,
		Temperature: request.Temperature, TopP: request.TopP,
		Stop: request.Stop, Seed: request.Seed,
	}
	p.mapChatParameters(&upstreamRequest)
	if upstreamRequest.Stream {
		upstreamRequest.StreamOptions = p.chatStreamOptions(request)
	}
	resp, err := p.chatCompletionResponse(ctx, &upstreamRequest)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	if request.Stream && p.upstreamStream {
		response, err := streamChatCompletionDataWithNormalizer(resp.Body, request.Model, nil, normalizeStream)
		if err == nil {
			err = validateChatCompletionEnvelope(response)
		}
		if err == nil {
			err = validateCompletionUsage(response.Usage)
		}
		if err == nil && p.exactChatUsage {
			err = validateExactChatUsage(response)
		}
		if err == nil {
			err = validateRequestedChatChoices(request, response)
		}
		if err == nil {
			err = validateRequestedChatAudio(request, response)
		}
		if err == nil {
			err = validateRequestedLegacyFunctionCalls(request, response)
		}
		return response, err
	}

	var response openai.ChatCompletionResponse
	if err := decodeResponse(resp.Body, &response); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if err := validateChatCompletionEnvelope(response); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if err := validateCompletionUsage(response.Usage); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if p.exactChatUsage {
		if err := validateExactChatUsage(response); err != nil {
			return openai.ChatCompletionResponse{}, err
		}
	}
	if err := validateRequestedChatChoices(request, response); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if err := validateRequestedChatAudio(request, response); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if err := validateRequestedLegacyFunctionCalls(request, response); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return response, nil
}

func validateChatCompletionEnvelope(response openai.ChatCompletionResponse) error {
	if response.Created < 0 {
		return errors.New("provider returned invalid chat completion timestamp")
	}
	if message := openai.ValidateMetadata(response.Metadata); message != "" {
		return fmt.Errorf("provider returned invalid chat completion metadata: %s", message)
	}
	if !openai.ValidReportedServiceTier(response.ServiceTier) {
		return errors.New("provider returned invalid chat completion service tier")
	}
	for _, choice := range response.Choices {
		if err := openai.ValidateLegacyFunctionResponse(choice.Message.FunctionCall); err != nil {
			return fmt.Errorf("provider returned invalid legacy function call: %w", err)
		}
		if err := openai.ValidateChatAnnotations(choice.Message.Annotations); err != nil {
			return fmt.Errorf("provider returned invalid chat completion annotations: %w", err)
		}
		if err := openai.ValidateChatAudioResponse(choice.Message.Audio); err != nil {
			return fmt.Errorf("provider returned invalid chat completion audio: %w", err)
		}
		if err := openai.ValidateReasoningBlocks(choice.Message.Reasoning); err != nil {
			return fmt.Errorf("provider returned invalid chat completion reasoning: %w", err)
		}
		if err := openai.ValidateChatReasoningContent(choice.Message.Role, choice.Message.ReasoningContent); err != nil {
			return fmt.Errorf("provider returned invalid chat completion reasoning_content: %w", err)
		}
	}
	return nil
}

const maxChatCompletionResponseBytes = 32 << 20

func decodeChatCompletionResponse(reader io.Reader, target *openai.ChatCompletionResponse) error {
	payload, err := io.ReadAll(io.LimitReader(reader, maxChatCompletionResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxChatCompletionResponseBytes {
		return errors.New("chat completion response exceeds limit")
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return err
	}
	reported, err := completeChatUsageFields(payload)
	if err != nil {
		return err
	}
	target.UsageReported = reported
	return nil
}

func completeChatUsageFields(payload []byte) (bool, error) {
	var wire struct {
		Usage *struct {
			PromptTokens     *int `json:"prompt_tokens"`
			CompletionTokens *int `json:"completion_tokens"`
			TotalTokens      *int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return false, err
	}
	return wire.Usage != nil && wire.Usage.PromptTokens != nil && wire.Usage.CompletionTokens != nil && wire.Usage.TotalTokens != nil, nil
}

func validateExactChatUsage(response openai.ChatCompletionResponse) error {
	if !response.UsageReported || response.Usage.TotalTokens != response.Usage.PromptTokens+response.Usage.CompletionTokens {
		return errors.New("Azure Chat requires exact prompt, completion and total token usage")
	}
	return nil
}

func validateRequestedChatChoices(request openai.ChatCompletionRequest, response openai.ChatCompletionResponse) error {
	if request.N == nil {
		return nil
	}
	if len(response.Choices) != *request.N {
		return errors.New("chat completion choice count does not match n")
	}
	seen := make([]bool, len(response.Choices))
	for _, choice := range response.Choices {
		if choice.Index < 0 || choice.Index >= len(seen) || seen[choice.Index] {
			return errors.New("invalid chat completion choice index")
		}
		seen[choice.Index] = true
	}
	return nil
}

func (p OpenAICompatible) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := p.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	upstreamRequest := openAICompatibleEmbeddingRequest{
		Model: request.Model, Input: request.Input, Metadata: request.Metadata, EncodingFormat: request.EncodingFormat,
		Dimensions: request.Dimensions, OutputDType: request.OutputDType, User: request.User,
	}
	p.mapEmbeddingDimensions(&upstreamRequest)
	body, err := json.Marshal(upstreamRequest)
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "embeddings"), bytes.NewReader(body))
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openai.EmbeddingResponse{}, responseStatusError(p.providerName(), resp)
	}
	var upstream struct {
		openai.EmbeddingResponse
		Usage *struct {
			PromptTokens            *int                           `json:"prompt_tokens"`
			CompletionTokens        int                            `json:"completion_tokens"`
			TotalTokens             *int                           `json:"total_tokens"`
			PromptTokensDetails     *openai.PromptTokenDetails     `json:"prompt_tokens_details"`
			CompletionTokensDetails *openai.CompletionTokenDetails `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := decodeEmbeddingResponse(resp.Body, &upstream); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	response := upstream.EmbeddingResponse
	if usage := upstream.Usage; usage != nil {
		if usage.PromptTokens == nil || usage.TotalTokens == nil || *usage.PromptTokens < 0 || *usage.TotalTokens < *usage.PromptTokens || usage.CompletionTokens != 0 {
			return openai.EmbeddingResponse{}, errors.New("invalid embedding usage")
		}
		response.Usage = openai.Usage{
			PromptTokens: *usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: *usage.TotalTokens,
			PromptTokensDetails: usage.PromptTokensDetails, CompletionTokensDetails: usage.CompletionTokensDetails,
		}
		response.UsageReported = true
	}
	if err := validateEmbeddingVectors(request, response.Data); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if p.exactEmbeddingUsage && (!response.UsageReported || response.Usage.TotalTokens != response.Usage.PromptTokens) {
		return openai.EmbeddingResponse{}, errors.New("Azure embeddings requires exact prompt and total token usage")
	}
	return response, nil
}

func (p OpenAICompatible) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return p.streamChatCompletions(ctx, request, write, nil)
}

func (p OpenAICompatible) streamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter, normalizeStream func(string) (string, error)) (openai.ChatCompletionResponse, error) {
	if !p.upstreamStream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}

	var messages any
	if p.chatMessages != nil {
		var err error
		messages, err = p.chatMessages(request)
		if err != nil {
			return openai.ChatCompletionResponse{}, err
		}
	}
	upstreamRequest := openAICompatibleChatRequest{
		ChatGenerationOptions: request.ChatGenerationOptions,
		Model:                 request.Model, Messages: request.Messages, messagesOverride: messages, Functions: request.Functions, FunctionCall: request.FunctionCall, Tools: request.Tools,
		ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls,
		ResponseFormat: request.ResponseFormat, Stream: true,
		MaxTokens: request.MaxTokens, MaxCompletionTokens: request.MaxCompletionTokens,
		Temperature: request.Temperature, TopP: request.TopP,
		Stop: request.Stop, Seed: request.Seed,
	}
	p.mapChatParameters(&upstreamRequest)
	upstreamRequest.StreamOptions = p.chatStreamOptions(request)
	resp, err := p.chatCompletionResponse(ctx, &upstreamRequest)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()

	response, err := streamChatCompletionDataWithNormalizer(resp.Body, request.Model, write, normalizeStream)
	if err == nil {
		err = validateChatCompletionEnvelope(response)
	}
	if err == nil {
		err = validateRequestedChatChoices(request, response)
	}
	if err == nil {
		err = validateRequestedChatAudio(request, response)
	}
	if err == nil {
		err = validateRequestedLegacyFunctionCalls(request, response)
	}
	if err == nil && p.exactChatUsage {
		err = validateExactChatUsage(response)
	}
	return response, err
}

func (p OpenAICompatible) chatStreamOptions(request openai.ChatCompletionRequest) *openAICompatibleStreamOptions {
	if p.providerName() == "mistral" {
		return nil
	}
	options := &openAICompatibleStreamOptions{IncludeUsage: true}
	if request.StreamOptions != nil {
		options.IncludeObfuscation = request.StreamOptions.IncludeObfuscation
	}
	return options
}

func (p OpenAICompatible) chatCompletionResponse(ctx context.Context, request *openAICompatibleChatRequest) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		body, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "chat/completions"), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if p.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		response, err := p.client.Do(httpReq)
		if err != nil {
			return nil, err
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}
		providerErr := responseStatusError(p.providerName(), response)
		_ = response.Body.Close()
		if attempt == 0 && useMaxCompletionTokens(request, providerErr) {
			continue
		}
		return nil, providerErr
	}
	return nil, fmt.Errorf("%s chat compatibility retry exhausted", p.providerName())
}

func useMaxCompletionTokens(request *openAICompatibleChatRequest, err error) bool {
	if request.MaxTokens == nil || request.MaxCompletionTokens != nil {
		return false
	}
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.UpstreamCode != "unsupported_parameter" || providerErr.Param != "max_tokens" {
		return false
	}
	request.MaxCompletionTokens = request.MaxTokens
	request.MaxTokens = nil
	return true
}

func (p OpenAICompatible) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if err := p.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	body, err := json.Marshal(openAICompatibleResponseRequest{
		Include: request.Include, Store: request.Store, Reasoning: request.Reasoning, Truncation: request.Truncation, TopLogprobs: request.TopLogprobs, Metadata: request.Metadata, ContextManagement: request.ContextManagement, Moderation: request.Moderation,
		Model: request.Model, Input: request.Input, Instructions: request.Instructions,
		Tools: request.Tools, ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls,
		Text: request.Text, PreviousResponse: request.PreviousResponse, User: request.User, SafetyIdentifier: request.SafetyIdentifier, PromptCacheKey: request.PromptCacheKey, PromptCacheOptions: request.PromptCacheOptions, PromptCacheRetention: request.PromptCacheRetention, ServiceTier: request.ServiceTier, Background: request.Background, Stream: false, StreamOptions: request.StreamOptions,
		MaxOutputTokens: responseOutputTokenLimit(request),
		Temperature:     request.Temperature, TopP: request.TopP, FrequencyPenalty: request.FrequencyPenalty,
		PresencePenalty: request.PresencePenalty, MaxToolCalls: request.MaxToolCalls,
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
		return openai.ResponseResponse{}, responseStatusError(p.providerName(), resp)
	}

	response, err := decodeResponseJSON(resp.Body)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	if p.exactResponseUsage {
		if err := validateExactResponseUsage(response, "Azure"); err != nil {
			return openai.ResponseResponse{}, err
		}
	}
	return response, nil
}

func (p OpenAICompatible) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if err := p.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	if !p.upstreamStream {
		return openai.ResponseResponse{}, ErrStreamingUnsupported
	}

	body, err := json.Marshal(openAICompatibleResponseRequest{
		Include: request.Include, Store: request.Store, Reasoning: request.Reasoning, Truncation: request.Truncation, TopLogprobs: request.TopLogprobs, Metadata: request.Metadata, ContextManagement: request.ContextManagement, Moderation: request.Moderation,
		Model: request.Model, Input: request.Input, Instructions: request.Instructions,
		Tools: request.Tools, ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls,
		Text: request.Text, PreviousResponse: request.PreviousResponse, User: request.User, SafetyIdentifier: request.SafetyIdentifier, PromptCacheKey: request.PromptCacheKey, PromptCacheOptions: request.PromptCacheOptions, PromptCacheRetention: request.PromptCacheRetention, ServiceTier: request.ServiceTier, Stream: true, StreamOptions: request.StreamOptions,
		MaxOutputTokens: responseOutputTokenLimit(request),
		Temperature:     request.Temperature, TopP: request.TopP, FrequencyPenalty: request.FrequencyPenalty,
		PresencePenalty: request.PresencePenalty, MaxToolCalls: request.MaxToolCalls,
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
		return openai.ResponseResponse{}, responseStatusError(p.providerName(), resp)
	}

	forward := write
	if p.exactResponseUsage {
		forward = func(event, payload string) error {
			if event == "response.completed" || event == "response.incomplete" {
				if err := validateExactResponseTerminalUsage(payload, "Azure"); err != nil {
					return err
				}
			}
			if write != nil {
				return write(event, payload)
			}
			return nil
		}
	}
	response, err := streamResponseData(resp.Body, request.Model, forward)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	if p.exactResponseUsage {
		if err := validateExactResponseUsage(response, "Azure"); err != nil {
			return openai.ResponseResponse{}, err
		}
	}
	return response, nil
}

func providerURL(baseURL string, path string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/" + strings.TrimLeft(path, "/")
	}
	return baseURL + "/v1/" + strings.TrimLeft(path, "/")
}

func openAIChatCompletionChunkPayload(id string, model string, index int, role string, content string, finishReason *string, stopSequence ...*string) string {
	delta := map[string]any{}
	if role != "" {
		delta["role"] = role
	}
	if content != "" {
		delta["content"] = content
	}
	choice := map[string]any{"index": index, "delta": delta, "finish_reason": finishReason}
	if len(stopSequence) > 0 && stopSequence[0] != nil {
		choice["stop_sequence"] = stopSequence[0]
	}
	payload, err := json.Marshal(map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().UTC().Unix(),
		"model":   model,
		"choices": []map[string]any{choice},
	})
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func decodeChatCompletionStream(body io.Reader, fallbackModel string) (openai.ChatCompletionResponse, error) {
	return streamChatCompletionData(body, fallbackModel, nil)
}

const maxChatStreamChoices = 128
const maxChatStreamToolCalls = 128

func streamChatCompletionData(body io.Reader, fallbackModel string, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	return streamChatCompletionDataWithNormalizer(body, fallbackModel, write, nil)
}

func streamChatCompletionDataWithNormalizer(body io.Reader, fallbackModel string, write ChatCompletionStreamWriter, normalize func(string) (string, error)) (openai.ChatCompletionResponse, error) {
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
	var idSeen, modelSeen, createdSeen, metadataSeen, serviceTierSeen, fingerprintSeen bool
	err := scanSSEData(body, func(payload string) error {
		if payload == "[DONE]" {
			return io.EOF
		}
		if normalize != nil {
			var err error
			payload, err = normalize(payload)
			if err != nil {
				return err
			}
		}
		var chunk struct {
			ID                string             `json:"id"`
			Created           *int64             `json:"created"`
			Model             string             `json:"model"`
			Metadata          *map[string]string `json:"metadata"`
			ServiceTier       string             `json:"service_tier"`
			SystemFingerprint string             `json:"system_fingerprint"`
			Usage             *openai.Usage      `json:"usage"`
			Choices           []struct {
				Index int `json:"index"`
				Delta struct {
					Role             string               `json:"role"`
					Content          string               `json:"content"`
					Refusal          *string              `json:"refusal"`
					Audio            *openai.ChatAudio    `json:"audio"`
					FunctionCall     *openai.FunctionCall `json:"function_call"`
					ToolCalls        []openai.ToolCall    `json:"tool_calls,omitempty"`
					ReasoningContent string               `json:"reasoning_content"`
				} `json:"delta"`
				FinishReason *string                `json:"finish_reason"`
				StopSequence *string                `json:"stop_sequence"`
				Logprobs     *openai.ChoiceLogprobs `json:"logprobs"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return err
		}
		if chunk.ID != "" {
			if idSeen && response.ID != chunk.ID {
				return errors.New("provider changed chat completion ID during stream")
			}
			response.ID = chunk.ID
			idSeen = true
		}
		if chunk.Model != "" {
			if modelSeen && response.Model != chunk.Model {
				return errors.New("provider changed chat completion model during stream")
			}
			response.Model = chunk.Model
			modelSeen = true
		}
		if chunk.Created != nil {
			if *chunk.Created < 0 {
				return errors.New("provider returned invalid chat completion timestamp")
			}
			if createdSeen && response.Created != *chunk.Created {
				return errors.New("provider changed chat completion timestamp during stream")
			}
			response.Created = *chunk.Created
			createdSeen = true
		}
		if chunk.Metadata != nil {
			if message := openai.ValidateMetadata(*chunk.Metadata); message != "" {
				return fmt.Errorf("provider returned invalid chat completion metadata: %s", message)
			}
			if metadataSeen && !maps.Equal(response.Metadata, *chunk.Metadata) {
				return errors.New("provider changed chat completion metadata during stream")
			}
			response.Metadata = *chunk.Metadata
			metadataSeen = true
		}
		if chunk.ServiceTier != "" {
			if !openai.ValidReportedServiceTier(chunk.ServiceTier) {
				return errors.New("provider returned invalid chat completion service tier")
			}
			if serviceTierSeen && response.ServiceTier != chunk.ServiceTier {
				return errors.New("provider changed chat completion service tier during stream")
			}
			response.ServiceTier = chunk.ServiceTier
			serviceTierSeen = true
		}
		if chunk.SystemFingerprint != "" {
			if fingerprintSeen && response.SystemFingerprint != chunk.SystemFingerprint {
				return errors.New("provider changed chat completion system fingerprint during stream")
			}
			response.SystemFingerprint = chunk.SystemFingerprint
			fingerprintSeen = true
		}
		if chunk.Usage != nil {
			if err := validateCompletionUsage(*chunk.Usage); err != nil {
				return err
			}
			response.Usage = *chunk.Usage
			reported, err := completeChatUsageFields([]byte(payload))
			if err != nil {
				return err
			}
			response.UsageReported = reported
		}
		for _, choice := range chunk.Choices {
			if choice.Index < 0 || choice.Index >= maxChatStreamChoices {
				return fmt.Errorf("invalid upstream choice index")
			}
			for len(response.Choices) <= choice.Index {
				response.Choices = append(response.Choices, openai.Choice{Index: len(response.Choices), Message: openai.Message{Role: "assistant"}})
			}
			current := &response.Choices[choice.Index]
			current.Index = choice.Index
			if choice.Logprobs != nil {
				if current.Logprobs == nil {
					current.Logprobs = &openai.ChoiceLogprobs{}
				}
				current.Logprobs.Content = append(current.Logprobs.Content, choice.Logprobs.Content...)
				current.Logprobs.Refusal = append(current.Logprobs.Refusal, choice.Logprobs.Refusal...)
			}
			if choice.Delta.Role != "" {
				current.Message.Role = choice.Delta.Role
			}
			if choice.Delta.Content != "" {
				current.Message.Content = openai.ContentText(current.Message.Content) + choice.Delta.Content
			}
			if choice.Delta.ReasoningContent != "" {
				if len(current.Message.ReasoningContent) > openai.MaxChatReasoningContentBytes-len(choice.Delta.ReasoningContent) {
					return errors.New("provider chat reasoning_content stream exceeds limit")
				}
				current.Message.ReasoningContent += choice.Delta.ReasoningContent
			}
			if choice.Delta.Refusal != nil {
				value := *choice.Delta.Refusal
				if current.Message.Refusal != nil {
					value = *current.Message.Refusal + value
				}
				current.Message.Refusal = &value
			}
			if err := mergeChatAudioDelta(&current.Message.Audio, choice.Delta.Audio); err != nil {
				return err
			}
			if err := mergeLegacyFunctionCallDelta(&current.Message.FunctionCall, choice.Delta.FunctionCall); err != nil {
				return err
			}
			if err := mergeToolCallDeltas(&current.Message.ToolCalls, choice.Delta.ToolCalls); err != nil {
				return err
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				current.FinishReason = *choice.FinishReason
			}
			if choice.StopSequence != nil {
				current.StopSequence = choice.StopSequence
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

func mergeLegacyFunctionCallDelta(target **openai.FunctionCall, delta *openai.FunctionCall) error {
	if delta == nil {
		return nil
	}
	if *target == nil {
		*target = &openai.FunctionCall{}
	}
	current := *target
	current.Name += delta.Name
	current.Arguments += delta.Arguments
	if len(current.Name) > 64 || len(current.Arguments) > openai.MaxChatFunctionArgumentsChars {
		return errors.New("legacy function call stream exceeds limit")
	}
	return nil
}

func validateRequestedChatAudio(request openai.ChatCompletionRequest, response openai.ChatCompletionResponse) error {
	if !openai.ChatRequestsAudio(request) {
		for _, choice := range response.Choices {
			if choice.Message.Audio != nil {
				return errors.New("provider returned unrequested chat audio")
			}
		}
		return nil
	}
	if len(response.Choices) == 0 {
		return errors.New("provider omitted requested chat audio")
	}
	for _, choice := range response.Choices {
		if choice.Message.Audio == nil {
			return errors.New("provider omitted requested chat audio")
		}
	}
	return nil
}

func validateRequestedLegacyFunctionCalls(request openai.ChatCompletionRequest, response openai.ChatCompletionResponse) error {
	allowed := make(map[string]struct{}, len(request.Functions))
	for _, function := range request.Functions {
		allowed[function.Name] = struct{}{}
	}
	for _, choice := range response.Choices {
		call := choice.Message.FunctionCall
		if call == nil {
			continue
		}
		if len(allowed) == 0 || (request.FunctionCall != nil && request.FunctionCall.Mode == "none") {
			return errors.New("provider returned an unrequested legacy function call")
		}
		if _, ok := allowed[call.Name]; !ok {
			return errors.New("provider returned an undeclared legacy function call")
		}
		if request.FunctionCall != nil && request.FunctionCall.Name != "" && call.Name != request.FunctionCall.Name {
			return errors.New("provider returned a different legacy function call")
		}
	}
	return nil
}

func mergeChatAudioDelta(target **openai.ChatAudio, delta *openai.ChatAudio) error {
	if delta == nil {
		return nil
	}
	if err := openai.ValidateChatAudioDelta(delta); err != nil {
		return err
	}
	if *target == nil {
		*target = &openai.ChatAudio{}
	}
	current := *target
	if delta.ID != "" {
		if current.ID != "" && current.ID != delta.ID {
			return errors.New("provider changed chat audio ID during stream")
		}
		current.ID = delta.ID
	}
	if delta.Data != nil {
		value := *delta.Data
		if current.Data != nil {
			value = *current.Data + value
		}
		if len(value) > openai.MaxChatAudioDataChars {
			return errors.New("chat audio stream exceeds data limit")
		}
		current.Data = &value
	}
	if delta.Transcript != nil {
		value := *delta.Transcript
		if current.Transcript != nil {
			value = *current.Transcript + value
		}
		if utf8.RuneCountInString(value) > openai.MaxChatAudioTranscriptChars {
			return errors.New("chat audio stream exceeds transcript limit")
		}
		current.Transcript = &value
	}
	if delta.ExpiresAt != nil {
		if current.ExpiresAt != nil && *current.ExpiresAt != *delta.ExpiresAt {
			return errors.New("provider changed chat audio expiry during stream")
		}
		value := *delta.ExpiresAt
		current.ExpiresAt = &value
	}
	return nil
}

func mergeToolCallDeltas(target *[]openai.ToolCall, deltas []openai.ToolCall) error {
	for order, delta := range deltas {
		index := order
		if delta.Index != nil {
			index = *delta.Index
		}
		if index < 0 || index >= maxChatStreamToolCalls {
			return fmt.Errorf("invalid upstream tool call index")
		}
		for len(*target) <= index {
			*target = append(*target, openai.ToolCall{Type: "function"})
		}
		current := &(*target)[index]
		if delta.ExtraContent != nil {
			current.ExtraContent = delta.ExtraContent
		}
		if delta.ID != "" {
			current.ID = delta.ID
		}
		if delta.Type != "" {
			current.Type = delta.Type
		}
		if delta.Function.Name != "" {
			current.Function.Name += delta.Function.Name
		}
		current.Function.Arguments += delta.Function.Arguments
	}
	return nil
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
	terminal := false
	var lastSequenceNumber int64
	sequenceNumberSeen := false
	err := scanSSEEvents(&responseStreamReader{source: body, remaining: maxResponseStreamBytes}, func(event string, payload string) error {
		if payload == "[DONE]" {
			if !terminal {
				return io.ErrUnexpectedEOF
			}
			return io.EOF
		}
		var decoded map[string]any
		decoder := json.NewDecoder(strings.NewReader(payload))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return err
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return errors.New("invalid trailing data in Responses SSE event")
		}
		if payloadType, present := decoded["type"]; present {
			typedEvent, ok := payloadType.(string)
			if !ok || typedEvent == "" {
				return errors.New("Responses event contains an invalid type")
			}
			if event != "" && event != typedEvent {
				return errors.New("Responses SSE event name contradicts payload type")
			}
			event = typedEvent
		} else if event == "" {
			return errors.New("Responses event is missing its type")
		}
		if rawSequence, present := decoded["sequence_number"]; present {
			sequence, ok := rawSequence.(json.Number)
			if !ok {
				return errors.New("Responses event contains an invalid sequence_number")
			}
			value, err := sequence.Int64()
			if err != nil || value < 0 {
				return errors.New("Responses event contains an invalid sequence_number")
			}
			if sequenceNumberSeen && value <= lastSequenceNumber {
				return errors.New("Responses event sequence_number is not increasing")
			}
			lastSequenceNumber, sequenceNumberSeen = value, true
		}
		if value, present := decoded["item_id"]; present {
			itemID, ok := value.(string)
			if !ok || !validResponseResourceID(itemID) {
				return errors.New("Responses event contains an invalid item_id")
			}
		}
		if value, present := decoded["response_id"]; present {
			responseID, ok := value.(string)
			if !ok || !validResponseResourceID(responseID) {
				return errors.New("Responses event contains an invalid response_id")
			}
			if response.ID != "" && response.ID != responseID {
				return errors.New("provider changed response ID during stream")
			}
		}
		outputIndex, err := responseOutputIndex(decoded)
		if err != nil {
			return err
		}
		if itemID, present := decoded["item_id"].(string); present {
			if err := validateResponseStreamOutputIdentity(response.Output, outputIndex, openai.ResponseOutputItem{ID: itemID}); err != nil {
				return err
			}
		}
		if id, ok := decoded["response_id"].(string); ok && response.ID == "" {
			response.ID = id
		}
		if event == "response.function_call_arguments.delta" || event == "response.function_call_arguments.done" {
			field := "delta"
			if event == "response.function_call_arguments.done" {
				field = "arguments"
			}
			value, err := requiredResponseEventString(decoded, field)
			if err != nil {
				return err
			}
			if event == "response.function_call_arguments.done" {
				var arguments map[string]json.RawMessage
				if json.Unmarshal([]byte(value), &arguments) != nil || arguments == nil {
					return errors.New("Responses function arguments event must contain a JSON object")
				}
			}
			item := ensureResponseOutputItem(&response, outputIndex)
			item.Type = "function_call"
			item.Role, item.Content = "", nil
			if id, ok := decoded["item_id"].(string); ok {
				item.ID = id
			}
			if event == "response.function_call_arguments.delta" {
				item.Arguments += value
			} else {
				item.Arguments = value
			}
		}
		if event == "response.custom_tool_call_input.delta" || event == "response.custom_tool_call_input.done" {
			field := "delta"
			if event == "response.custom_tool_call_input.done" {
				field = "input"
			}
			value, err := requiredResponseEventString(decoded, field)
			if err != nil {
				return err
			}
			item := ensureResponseOutputItem(&response, outputIndex)
			item.Type = "custom_tool_call"
			item.Role, item.Content = "", nil
			if id, ok := decoded["item_id"].(string); ok {
				item.ID = id
			}
			if event == "response.custom_tool_call_input.delta" {
				item.Input += value
			} else {
				item.Input = value
			}
		}
		if event == "response.apply_patch_call_operation_diff.delta" || event == "response.apply_patch_call_operation_diff.done" {
			item := ensureResponseOutputItem(&response, outputIndex)
			var value string
			field := "delta"
			replace := false
			if event == "response.apply_patch_call_operation_diff.done" {
				field, replace = "diff", true
			}
			var ok bool
			if value, ok = decoded[field].(string); !ok {
				return errors.New("Responses apply_patch diff event is missing text")
			}
			if message := openai.UpdateResponseApplyPatchDiff(item, value, replace); message != "" {
				return errors.New(message)
			}
		}
		if event == "response.refusal.delta" || event == "response.refusal.done" ||
			event == "response.output_text.delta" || event == "response.output_text.done" {
			field := "delta"
			if strings.HasSuffix(event, ".done") {
				if strings.HasPrefix(event, "response.refusal.") {
					field = "refusal"
				} else {
					field = "text"
				}
			}
			value, err := requiredResponseEventString(decoded, field)
			if err != nil {
				return err
			}
			contentIndex, err := boundedResponseStreamIndex(decoded, "content_index", maxResponseStreamContentParts)
			if err != nil {
				return err
			}
			item := ensureResponseOutputItem(&response, outputIndex)
			item.Type, item.Role = "message", "assistant"
			if id, ok := decoded["item_id"].(string); ok {
				item.ID = id
			}
			for len(item.Content) <= contentIndex {
				item.Content = append(item.Content, openai.ResponseOutputContent{})
			}
			part := &item.Content[contentIndex]
			switch event {
			case "response.output_text.delta", "response.output_text.done":
				part.Type, part.Refusal = "output_text", ""
				if value, exists := decoded["logprobs"]; exists {
					payload, err := json.Marshal(value)
					if err != nil {
						return err
					}
					var logprobs []openai.TokenLogprob
					if err := json.Unmarshal(payload, &logprobs); err != nil {
						return errors.New("invalid Responses logprobs")
					}
					if event == "response.output_text.delta" {
						part.Logprobs = append(part.Logprobs, logprobs...)
					} else {
						part.Logprobs = logprobs
					}
				}
				if event == "response.output_text.delta" {
					part.Text += value
				} else {
					part.Text = value
				}
			case "response.refusal.delta", "response.refusal.done":
				part.Type, part.Text = "refusal", ""
				part.Annotations, part.Logprobs = nil, nil
				if event == "response.refusal.delta" {
					part.Refusal += value
				} else {
					part.Refusal = value
				}
			}
			if err := validateResponseOutputContent(*part); err != nil {
				return err
			}
		}
		if event == "response.content_part.added" || event == "response.content_part.done" {
			contentIndex, err := boundedResponseStreamIndex(decoded, "content_index", maxResponseStreamContentParts)
			if err != nil {
				return err
			}
			part, ok := decoded["part"].(map[string]any)
			if !ok {
				return errors.New("Responses content event is missing its part")
			}
			payload, err := json.Marshal(part)
			if err != nil {
				return err
			}
			var snapshot openai.ResponseOutputContent
			if err := json.Unmarshal(payload, &snapshot); err != nil {
				return err
			}
			if snapshot.Type != "output_text" && snapshot.Type != "refusal" {
				return errors.New("Responses content event has an unsupported part type")
			}
			if err := validateResponseOutputContent(snapshot); err != nil {
				return err
			}
			item := ensureResponseOutputItem(&response, outputIndex)
			item.Type, item.Role = "message", "assistant"
			if id, ok := decoded["item_id"].(string); ok {
				item.ID = id
			}
			for len(item.Content) <= contentIndex {
				item.Content = append(item.Content, openai.ResponseOutputContent{})
			}
			item.Content[contentIndex] = snapshot
			response.OutputText = ""
		}
		if err := applyResponseSummaryEvent(&response, outputIndex, event, decoded); err != nil {
			return err
		}
		if event == "response.output_text.annotation.added" {
			contentIndex, err := boundedResponseStreamIndex(decoded, "content_index", maxResponseStreamContentParts)
			if err != nil {
				return err
			}
			annotationIndex, err := boundedResponseStreamIndex(decoded, "annotation_index", maxResponseStreamContentParts)
			if err != nil {
				return err
			}
			annotation, present := decoded["annotation"]
			if !present {
				return errors.New("Responses annotation event is missing its annotation")
			}
			if annotation != nil {
				if _, ok := annotation.(map[string]any); !ok {
					return errors.New("invalid Responses annotation")
				}
			}
			encoded, err := json.Marshal(annotation)
			if err != nil {
				return err
			}
			item := ensureResponseOutputItem(&response, outputIndex)
			item.Type, item.Role = "message", "assistant"
			if id, ok := decoded["item_id"].(string); ok {
				item.ID = id
			}
			for len(item.Content) <= contentIndex {
				item.Content = append(item.Content, openai.ResponseOutputContent{})
			}
			part := &item.Content[contentIndex]
			part.Type, part.Refusal = "output_text", ""
			for len(part.Annotations) <= annotationIndex {
				part.Annotations = append(part.Annotations, json.RawMessage("null"))
			}
			part.Annotations[annotationIndex] = encoded
		}
		if event == "response.output_item.added" || event == "response.output_item.done" {
			itemValue, ok := decoded["item"].(map[string]any)
			if !ok {
				return errors.New("Responses output item event is missing its item")
			}
			marshaled, err := json.Marshal(itemValue)
			if err != nil {
				return err
			}
			var snapshot openai.ResponseOutputItem
			if err := json.Unmarshal(marshaled, &snapshot); err != nil {
				return err
			}
			if eventItemID, present := decoded["item_id"].(string); present {
				if snapshot.ID != "" && snapshot.ID != eventItemID {
					return errors.New("Responses output item event contains contradictory item IDs")
				}
				if snapshot.ID == "" {
					snapshot.ID = eventItemID
				}
			}
			if err := validateResponseStreamOutputIdentity(response.Output, outputIndex, snapshot); err != nil {
				return err
			}
			if event == "response.output_item.added" && snapshot.Type == "apply_patch_call" {
				if message := openai.ValidateResponseApplyPatchCallPartial(snapshot); message != "" {
					return errors.New(message)
				}
			} else if event == "response.output_item.added" && (snapshot.Type == "function_call" || snapshot.Type == "custom_tool_call") {
				if err := validatePartialResponseOutputItems([]openai.ResponseOutputItem{snapshot}); err != nil {
					return err
				}
			} else if err := validateResponseOutputItems([]openai.ResponseOutputItem{snapshot}); err != nil {
				return err
			}
			*ensureResponseOutputItem(&response, outputIndex) = snapshot
			response.OutputText = ""
		}
		responseSnapshotEvent := event == "response.created" || event == "response.in_progress" ||
			event == "response.completed" || event == "response.incomplete" || event == "response.failed"
		if responseSnapshotEvent {
			typed, ok := decoded["response"].(map[string]any)
			if !ok {
				return errors.New("Responses lifecycle event is missing its response")
			}
			marshaled, err := json.Marshal(typed)
			if err != nil {
				return err
			}
			// An output snapshot replaces text assembled from earlier events.
			_, outputPresent := typed["output"]
			if outputPresent {
				response.OutputText = ""
				response.Output = nil
			}
			if _, present := typed["metadata"]; present {
				response.Metadata = nil
			}
			if err := validateResponseConfigurationPayload(marshaled, &response); err != nil {
				return err
			}
			previousResponseID := response.ID
			if err := json.Unmarshal(marshaled, &response); err != nil {
				return err
			}
			if previousResponseID != "" && response.ID != "" && previousResponseID != response.ID {
				return errors.New("provider changed response ID during stream")
			}
			if err := recordResponseInputUsage(marshaled, &response); err != nil {
				return err
			}
			if err := validateResponseUsage(response.Usage); err != nil {
				return err
			}
			if err := validateResponseEnvelope(response); err != nil {
				return err
			}
			if err := validateResponseControls(response); err != nil {
				return err
			}
			if err := validateResponseCitations(response.Citations); err != nil {
				return err
			}
			if err := validateResponseOutputItemsAllowSparse(response.Output, !outputPresent); err != nil {
				return err
			}
			response.OutputText = responseText(response)
		}
		switch event {
		case "response.completed", "response.incomplete", "response.failed":
			outcome, ok := decoded["response"].(map[string]any)
			if !ok {
				return errors.New("Responses terminal event is missing its response")
			}
			status := strings.TrimPrefix(event, "response.")
			if reported, exists := outcome["status"]; exists && reported != status {
				return errors.New("Responses terminal event has contradictory status")
			}
			response.Status = status
			terminal = true
		}
		if write != nil {
			if err := write(event, payload); err != nil {
				return err
			}
		}
		if terminal {
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return openai.ResponseResponse{}, err
	}
	if !terminal {
		return openai.ResponseResponse{}, io.ErrUnexpectedEOF
	}
	response.OutputText = responseText(response)
	return response, nil
}

const maxResponseStreamOutputItems = 1024
const maxResponseStreamContentParts = 128

func responseOutputIndex(decoded map[string]any) (int, error) {
	return boundedResponseStreamIndex(decoded, "output_index", maxResponseStreamOutputItems)
}

func boundedResponseStreamIndex(decoded map[string]any, field string, limit int) (int, error) {
	raw, present := decoded[field]
	if !present {
		return 0, nil
	}
	value, ok := raw.(float64)
	if number, isNumber := raw.(json.Number); isNumber {
		var err error
		value, err = number.Float64()
		ok = err == nil
	}
	// Check the small range before converting to int, including on 32-bit builds.
	if !ok || !(value >= 0 && value < float64(limit)) || value != float64(int(value)) {
		return 0, fmt.Errorf("invalid upstream response %s", field)
	}
	return int(value), nil
}

func requiredResponseEventString(decoded map[string]any, field string) (string, error) {
	value, ok := decoded[field].(string)
	if !ok {
		return "", fmt.Errorf("Responses event is missing string field %s", field)
	}
	return value, nil
}

func ensureResponseOutputItem(response *openai.ResponseResponse, index int) *openai.ResponseOutputItem {
	for len(response.Output) <= index {
		response.Output = append(response.Output, openai.ResponseOutputItem{})
	}
	return &response.Output[index]
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
	if err := scanner.Err(); err != nil {
		return err
	}
	if builder.Len() > 0 {
		if err := handle(event, strings.TrimSpace(builder.String())); err != nil {
			return err
		}
	}
	return scanner.Err()
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
