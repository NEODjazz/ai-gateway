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

	"ai-gateway-gateway/internal/openai"
)

// Gemini uses the native GenerateContent API with API-key or GCP workload authentication.
type Gemini struct {
	baseURL        string
	apiKey         string
	upstreamStream bool
	client         *http.Client
	authType       string
	tokenSource    *gcpTokenSource
	vertex         bool
	errorProvider  string
}

func NewGemini(baseURL, apiKey string, stream bool) Gemini {
	return NewGeminiWithAuth(baseURL, apiKey, stream, "api_key")
}

func NewGeminiWithAuth(baseURL, credential string, stream bool, authType string) Gemini {
	client := newProviderHTTPClient(180 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	authType = strings.ToLower(strings.TrimSpace(authType))
	if authType == "" {
		authType = "api_key"
	}
	return Gemini{baseURL: strings.TrimRight(baseURL, "/"), apiKey: credential, upstreamStream: stream, client: client, authType: authType, tokenSource: newGCPTokenSource(), errorProvider: "gemini"}
}

func (g Gemini) authorize(request *http.Request) error {
	request.Header.Del("Authorization")
	request.Header.Del("x-goog-api-key")
	switch g.authType {
	case "gcp_adc":
		token, err := g.tokenSource.Token(request.Context())
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		return nil
	case "api_key":
		request.Header.Set("x-goog-api-key", g.apiKey)
		return nil
	default:
		return fmt.Errorf("unsupported Gemini authentication type %q", g.authType)
	}
}

func (Gemini) SupportsVision() bool          { return true }
func (Gemini) SupportsWebSearch() bool       { return true }
func (Gemini) SupportsCodeExecution() bool   { return true }
func (Gemini) SupportsURLContext() bool      { return true }
func (Gemini) SupportsGoogleMaps() bool      { return true }
func (Gemini) SupportsAudioInput() bool      { return true }
func (Gemini) SupportsFileInput() bool       { return true }
func (Gemini) SupportsVideoInput() bool      { return true }
func (Gemini) SupportsMediaResolution() bool { return true }

func (Gemini) SupportsResponses() bool { return false }

func (Gemini) SupportsReasoningBlocks() bool   { return true }
func (Gemini) SupportsUnsignedReasoning() bool { return true }

type geminiPart struct {
	Text                string                            `json:"text,omitempty"`
	InlineData          *geminiInlineData                 `json:"inlineData,omitempty"`
	AudioTranscription  *geminiAudioTranscription         `json:"audioTranscription,omitempty"`
	FunctionCall        *geminiFunctionCall               `json:"functionCall,omitempty"`
	FunctionResponse    *geminiFunctionResponse           `json:"functionResponse,omitempty"`
	ExecutableCode      *openai.GeminiExecutableCode      `json:"executableCode,omitempty"`
	CodeExecutionResult *openai.GeminiCodeExecutionResult `json:"codeExecutionResult,omitempty"`
	Thought             bool                              `json:"thought,omitempty"`
	ThoughtSignature    string                            `json:"thoughtSignature,omitempty"`
}
type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}
type geminiFunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}
type geminiFunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}
type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}
type geminiFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parametersJsonSchema,omitempty"`
}
type geminiTool struct {
	Functions     []geminiFunction `json:"functionDeclarations,omitempty"`
	GoogleSearch  *struct{}        `json:"googleSearch,omitempty"`
	GoogleMaps    *struct{}        `json:"googleMaps,omitempty"`
	CodeExecution *struct{}        `json:"codeExecution,omitempty"`
	URLContext    *struct{}        `json:"urlContext,omitempty"`
}
type geminiGeneration struct {
	MaxOutputTokens    *int                            `json:"maxOutputTokens,omitempty"`
	Temperature        *float64                        `json:"temperature,omitempty"`
	TopP               *float64                        `json:"topP,omitempty"`
	TopK               *int                            `json:"topK,omitempty"`
	FrequencyPenalty   *float64                        `json:"frequencyPenalty,omitempty"`
	PresencePenalty    *float64                        `json:"presencePenalty,omitempty"`
	ResponseLogprobs   *bool                           `json:"responseLogprobs,omitempty"`
	Logprobs           *int                            `json:"logprobs,omitempty"`
	CandidateCount     *int                            `json:"candidateCount,omitempty"`
	ThinkingConfig     *geminiThinkingConfig           `json:"thinkingConfig,omitempty"`
	ImageConfig        *geminiImageConfig              `json:"imageConfig,omitempty"`
	AudioTranscription *geminiAudioTranscriptionConfig `json:"audioTranscriptionConfig,omitempty"`
	AudioTimestamp     *bool                           `json:"audioTimestamp,omitempty"`
	MediaResolution    string                          `json:"mediaResolution,omitempty"`
	ResponseModalities []string                        `json:"responseModalities,omitempty"`
	Seed               *int64                          `json:"seed,omitempty"`
	Stop               []string                        `json:"stopSequences,omitempty"`
	ResponseMIMEType   string                          `json:"responseMimeType,omitempty"`
	ResponseJSONSchema any                             `json:"responseJsonSchema,omitempty"`
}
type geminiAudioTranscriptionConfig struct {
	LanguageCodes    []string `json:"languageCodes,omitempty"`
	CustomVocabulary []string `json:"customVocabulary,omitempty"`
	WordTimestamp    bool     `json:"wordTimestamp,omitempty"`
	Diarization      bool     `json:"diarization,omitempty"`
	Mode             string   `json:"mode,omitempty"`
}

type geminiAudioTranscription struct {
	SpeakerLabel string            `json:"speakerLabel,omitempty"`
	Words        []geminiAudioWord `json:"words,omitempty"`
}

type geminiAudioWord struct {
	Word        string `json:"word"`
	StartOffset string `json:"startOffset"`
	EndOffset   string `json:"endOffset"`
}
type geminiImageConfig struct {
	AspectRatio string `json:"aspectRatio,omitempty"`
	ImageSize   string `json:"imageSize,omitempty"`
}
type geminiThinkingConfig struct {
	ThinkingLevel string `json:"thinkingLevel"`
}
type geminiLogprobCandidate struct {
	Token          string  `json:"token"`
	TokenID        int     `json:"tokenId"`
	LogProbability float64 `json:"logProbability"`
}
type geminiTopCandidates struct {
	Candidates []geminiLogprobCandidate `json:"candidates"`
}
type geminiLogprobsResult struct {
	TopCandidates     []geminiTopCandidates    `json:"topCandidates"`
	ChosenCandidates  []geminiLogprobCandidate `json:"chosenCandidates"`
	LogProbabilitySum float64                  `json:"logProbabilitySum"`
}
type geminiResponseCandidate struct {
	Index          int                   `json:"index"`
	Content        geminiContent         `json:"content"`
	FinishReason   string                `json:"finishReason"`
	LogprobsResult *geminiLogprobsResult `json:"logprobsResult"`
	Grounding      json.RawMessage       `json:"groundingMetadata"`
	URLContext     json.RawMessage       `json:"urlContextMetadata"`
}
type geminiRequest struct {
	Contents      []geminiContent              `json:"contents"`
	CachedContent string                       `json:"cachedContent,omitempty"`
	System        *geminiContent               `json:"systemInstruction,omitempty"`
	Tools         []geminiTool                 `json:"tools,omitempty"`
	ToolConfig    map[string]any               `json:"toolConfig,omitempty"`
	Generation    geminiGeneration             `json:"generationConfig"`
	ServiceTier   string                       `json:"serviceTier,omitempty"`
	Store         *bool                        `json:"store,omitempty"`
	Safety        []openai.GeminiSafetySetting `json:"safetySettings,omitempty"`
}
type geminiResponse struct {
	ID             string                    `json:"responseId"`
	Model          string                    `json:"modelVersion"`
	Candidates     []geminiResponseCandidate `json:"candidates"`
	Usage          *geminiUsage              `json:"usageMetadata"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error *struct {
		Code int `json:"code"`
	} `json:"error"`
}
type geminiUsage struct {
	Prompt        int    `json:"promptTokenCount"`
	ToolUsePrompt int    `json:"toolUsePromptTokenCount"`
	Cached        int    `json:"cachedContentTokenCount"`
	Candidates    int    `json:"candidatesTokenCount"`
	Thoughts      int    `json:"thoughtsTokenCount"`
	Total         int    `json:"totalTokenCount"`
	ServiceTier   string `json:"serviceTier"`
}

func geminiTokenCounts(usage *geminiUsage) (input, completion int, valid bool) {
	if usage == nil || usage.Prompt < 0 || usage.ToolUsePrompt < 0 || usage.Candidates < 0 || usage.Thoughts < 0 || usage.Cached < 0 || usage.Cached > usage.Prompt || usage.Total < 0 {
		return 0, 0, false
	}
	if usage.Prompt > math.MaxInt-usage.ToolUsePrompt || usage.Candidates > math.MaxInt-usage.Thoughts {
		return 0, 0, false
	}
	input = usage.Prompt + usage.ToolUsePrompt
	completion = usage.Candidates + usage.Thoughts
	if input > math.MaxInt-completion || usage.Total < input+completion {
		return 0, 0, false
	}
	return input, completion, true
}

func geminiInvalid(param string) error {
	return &Error{Class: FailureClientRequest, Provider: "gemini", StatusCode: 400, UpstreamCode: "unsupported_parameter", Param: param, Err: fmt.Errorf("unsupported or invalid %s for Gemini adapter", param)}
}

func (Gemini) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := validateChatReasoningContent("gemini", request.Messages, false); err != nil {
		return err
	}
	_, err := geminiChatRequest(request)
	return err
}
func (Gemini) ValidateResponseParameters(openai.ResponseRequest) error {
	return geminiInvalid("responses")
}
func (g Gemini) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, geminiInvalid("responses")
}

func geminiChatRequest(request openai.ChatCompletionRequest) (geminiRequest, error) {
	result := geminiRequest{Safety: append([]openai.GeminiSafetySetting(nil), request.GeminiSafetySettings...), CachedContent: request.GeminiCachedContent}
	if result.CachedContent != "" && !validGeminiCachedContentName(result.CachedContent) {
		return result, geminiInvalid("cached_content")
	}
	if err := openai.ValidateGeminiSafetySettings(request.GeminiSafetySettings); err != nil {
		return result, geminiInvalid("safety_settings")
	}
	if err := validateChatMessagePrefix("gemini", request.Messages, false); err != nil {
		return result, err
	}
	if err := rejectLegacyFunctionCalling("gemini", request); err != nil {
		return result, err
	}
	if err := validateChatPromptCacheBreakpoints("gemini", request, false); err != nil {
		return result, err
	}
	options := request.ChatGenerationOptions
	if options.TopK != nil && (*options.TopK < 1 || *options.TopK > 1000000) {
		return result, geminiInvalid("top_k")
	}
	for _, penalty := range []struct {
		name  string
		value *float64
	}{
		{name: "frequency_penalty", value: options.FrequencyPenalty},
		{name: "presence_penalty", value: options.PresencePenalty},
	} {
		if penalty.value != nil && (math.IsNaN(*penalty.value) || math.IsInf(*penalty.value, 0) || *penalty.value < -2 || *penalty.value > 2) {
			return result, geminiInvalid(penalty.name)
		}
	}
	if options.TopLogprobs != nil && (*options.TopLogprobs < 0 || *options.TopLogprobs > 20 || options.Logprobs == nil || !*options.Logprobs) {
		return result, geminiInvalid("top_logprobs")
	}
	if options.N != nil && (*options.N < 1 || *options.N > 20) {
		return result, geminiInvalid("n")
	}
	var thinkingConfig *geminiThinkingConfig
	if options.ReasoningEffort != "" {
		switch options.ReasoningEffort {
		case "minimal", "low", "medium", "high":
			thinkingConfig = &geminiThinkingConfig{ThinkingLevel: options.ReasoningEffort}
		default:
			return result, geminiInvalid("reasoning_effort")
		}
	}
	serviceTier := ""
	switch options.ServiceTier {
	case "":
	case "auto":
		serviceTier = "unspecified"
	case "default", "standard_only":
		serviceTier = "standard"
	case "flex", "priority":
		serviceTier = options.ServiceTier
	default:
		return result, geminiInvalid("service_tier")
	}
	if search := options.WebSearchOptions; search != nil {
		if search.SearchContextSize != "" || search.UserLocation != nil || search.MaxUses != nil {
			return result, geminiInvalid("web_search_options")
		}
		if options.N != nil && *options.N != 1 {
			return result, geminiInvalid("n")
		}
	}
	var responseModalities []string
	if options.Modalities != nil {
		if len(options.Modalities) != 1 || options.Modalities[0] != "text" || options.Audio != nil {
			return result, geminiInvalid("modalities")
		}
		responseModalities = []string{"TEXT"}
	}
	options.TopK = nil
	options.FrequencyPenalty = nil
	options.PresencePenalty = nil
	options.Logprobs = nil
	options.TopLogprobs = nil
	options.N = nil
	options.ReasoningEffort = ""
	options.ServiceTier = ""
	options.Store = nil
	options.Modalities = nil
	options.WebSearchOptions = nil
	if err := rejectGenerationOptions("gemini", options); err != nil {
		return result, err
	}
	if err := rejectChatMessageRefusals("gemini", request.Messages); err != nil {
		return result, err
	}
	if err := rejectChatMessageAudio("gemini", request.Messages); err != nil {
		return result, err
	}
	if request.ParallelToolCalls != nil {
		return result, geminiInvalid("parallel_tool_calls")
	}
	if request.MaxTokens != nil && request.MaxCompletionTokens != nil {
		return result, geminiInvalid("max_tokens")
	}
	maxTokens := request.MaxCompletionTokens
	if maxTokens == nil {
		maxTokens = request.MaxTokens
	}
	if maxTokens != nil && (*maxTokens <= 0 || *maxTokens > math.MaxInt32) {
		return result, geminiInvalid("max_completion_tokens")
	}
	if request.Seed != nil && (*request.Seed < math.MinInt32 || *request.Seed > math.MaxInt32) {
		return result, geminiInvalid("seed")
	}
	stop, valid := openai.StopSequences(request.Stop)
	if !valid {
		return result, geminiInvalid("stop")
	}
	result.Generation = geminiGeneration{
		MaxOutputTokens: maxTokens, Temperature: request.Temperature, TopP: request.TopP, TopK: request.TopK,
		FrequencyPenalty: request.FrequencyPenalty, PresencePenalty: request.PresencePenalty,
		ResponseLogprobs: request.Logprobs, Logprobs: request.TopLogprobs, CandidateCount: request.N,
		ThinkingConfig: thinkingConfig, Seed: request.Seed, Stop: stop,
		AudioTimestamp: request.GeminiAudioTimestamp, MediaResolution: request.GeminiMediaResolution, ResponseModalities: responseModalities,
	}
	if request.GeminiMediaResolution != "" {
		switch request.GeminiMediaResolution {
		case "MEDIA_RESOLUTION_UNSPECIFIED", "MEDIA_RESOLUTION_LOW", "MEDIA_RESOLUTION_MEDIUM", "MEDIA_RESOLUTION_HIGH":
		default:
			return result, geminiInvalid("media_resolution")
		}
		if !openai.HasChatMediaInput(request) {
			return result, geminiInvalid("media_resolution")
		}
	}
	result.ServiceTier = serviceTier
	result.Store = request.Store
	if request.ResponseFormat != nil {
		switch request.ResponseFormat.Type {
		case "text":
		case "json_object":
			result.Generation.ResponseMIMEType = "application/json"
		case "json_schema":
			if request.ResponseFormat.JSONSchema == nil || request.ResponseFormat.JSONSchema.Schema == nil {
				return result, geminiInvalid("response_format")
			}
			result.Generation.ResponseMIMEType = "application/json"
			result.Generation.ResponseJSONSchema = request.ResponseFormat.JSONSchema.Schema
		default:
			return result, geminiInvalid("response_format")
		}
	}
	if _, err := openai.ChatImageAttachments(request.Messages); err != nil {
		return result, err
	}
	if _, err := openai.ChatAudioAttachments(request.Messages); err != nil {
		return result, err
	}
	if _, err := openai.ChatFileAttachments(request.Messages); err != nil {
		return result, err
	}
	if _, err := openai.ChatVideoAttachments(request.Messages); err != nil {
		return result, err
	}
	toolNames := make(map[string]string)
	for _, message := range request.Messages {
		if len(message.Reasoning) > 0 && message.Role != "assistant" {
			return result, geminiInvalid("messages.reasoning")
		}
		if len(message.NativeContent) > 0 && message.Role != "assistant" {
			return result, geminiInvalid("messages.native_content")
		}
		if (message.Role != "assistant" && len(message.ToolCalls) > 0) || (message.Role != "tool" && message.ToolCallID != "") {
			return result, geminiInvalid("messages.tool_calls")
		}
		if message.Name != "" {
			return result, geminiInvalid("messages.name")
		}
		content := geminiContent{Role: "user"}
		var parts []geminiPart
		var err error
		if message.Role != "tool" {
			parts, err = geminiMessageParts(message.Content)
		}
		if err != nil {
			return result, err
		}
		content.Parts = parts
		switch message.Role {
		case "system", "developer":
			if result.System == nil {
				result.System = &geminiContent{}
			}
			result.System.Parts = append(result.System.Parts, parts...)
			continue
		case "user":
		case "assistant":
			content.Role = "model"
			for _, call := range message.ToolCalls {
				if call.Type != "function" || call.Function.Name == "" {
					return result, geminiInvalid("messages.tool_calls")
				}
				var args map[string]any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
					return result, geminiInvalid("messages.tool_calls.arguments")
				}
				part := geminiPart{FunctionCall: &geminiFunctionCall{ID: call.ID, Name: call.Function.Name, Args: args}}
				if call.ExtraContent != nil && call.ExtraContent.Google != nil {
					part.ThoughtSignature = call.ExtraContent.Google.ThoughtSignature
				}
				content.Parts = append(content.Parts, part)
				toolNames[call.ID] = call.Function.Name
			}
			if len(message.Reasoning) > 0 {
				if err := validateGeminiReasoning(message.Reasoning); err != nil {
					return result, err
				}
				for _, reasoning := range message.Reasoning {
					part := geminiPart{Text: reasoning.Thinking, Thought: true, ThoughtSignature: reasoning.Signature}
					position := len(content.Parts)
					if reasoning.Index != nil {
						position = min(*reasoning.Index, len(content.Parts))
					}
					content.Parts = append(content.Parts, geminiPart{})
					copy(content.Parts[position+1:], content.Parts[position:])
					content.Parts[position] = part
				}
			}
			signatures, err := openai.GeminiPartSignatures(message.NativeContent)
			if err != nil || len(signatures) != len(message.NativeContent) {
				return result, geminiInvalid("messages.native_content")
			}
			for _, signature := range signatures {
				if signature.Index >= len(content.Parts) || content.Parts[signature.Index].Thought || content.Parts[signature.Index].FunctionCall != nil || content.Parts[signature.Index].FunctionResponse != nil || content.Parts[signature.Index].InlineData != nil {
					return result, geminiInvalid("messages.native_content")
				}
				content.Parts[signature.Index].ThoughtSignature = signature.Signature
			}
			if err := openai.ValidateGeminiCodeExecutionParts(message.GeminiCodeExecutionParts); err != nil {
				return result, geminiInvalid("messages.code_execution")
			}
			for _, block := range message.GeminiCodeExecutionParts {
				part := geminiPart{ExecutableCode: block.Code, CodeExecutionResult: block.Result}
				position := min(block.Index, len(content.Parts))
				content.Parts = append(content.Parts, geminiPart{})
				copy(content.Parts[position+1:], content.Parts[position:])
				content.Parts[position] = part
			}
		case "tool":
			name, ok := toolNames[message.ToolCallID]
			if !ok || message.ToolCallID == "" {
				return result, geminiInvalid("messages.tool_call_id")
			}
			content.Parts = []geminiPart{{FunctionResponse: &geminiFunctionResponse{ID: message.ToolCallID, Name: name, Response: geminiToolResponse(message.Content)}}}
		default:
			return result, geminiInvalid("messages.role")
		}
		if len(content.Parts) == 0 {
			return result, geminiInvalid("messages.content")
		}
		if message.Role == "tool" && len(result.Contents) > 0 {
			last := &result.Contents[len(result.Contents)-1]
			if last.Role == "user" && len(last.Parts) > 0 && last.Parts[0].FunctionResponse != nil {
				last.Parts = append(last.Parts, content.Parts...)
				continue
			}
		}
		result.Contents = append(result.Contents, content)
	}
	if len(result.Contents) == 0 {
		return result, geminiInvalid("messages")
	}
	if len(request.Tools) > 0 {
		tool := geminiTool{}
		for _, definition := range request.Tools {
			if definition.Type != "function" || definition.Function.Name == "" {
				return result, geminiInvalid("tools")
			}
			if definition.Function.Strict != nil && *definition.Function.Strict {
				return result, geminiInvalid("tools.function.strict")
			}
			tool.Functions = append(tool.Functions, geminiFunction{Name: definition.Function.Name, Description: definition.Function.Description, Parameters: definition.Function.Parameters})
		}
		result.Tools = []geminiTool{tool}
	}
	if request.WebSearchOptions != nil {
		result.Tools = append(result.Tools, geminiTool{GoogleSearch: &struct{}{}})
	}
	if request.GeminiCodeExecution {
		result.Tools = append(result.Tools, geminiTool{CodeExecution: &struct{}{}})
	}
	if request.GeminiURLContext {
		result.Tools = append(result.Tools, geminiTool{URLContext: &struct{}{}})
	}
	if request.GeminiGoogleMaps {
		result.Tools = append(result.Tools, geminiTool{GoogleMaps: &struct{}{}})
		if request.GeminiRetrievalLocation != nil {
			location := *request.GeminiRetrievalLocation
			if math.IsNaN(location.Latitude) || math.IsInf(location.Latitude, 0) || location.Latitude < -90 || location.Latitude > 90 || math.IsNaN(location.Longitude) || math.IsInf(location.Longitude, 0) || location.Longitude < -180 || location.Longitude > 180 {
				return result, geminiInvalid("retrieval_config.lat_lng")
			}
			result.ToolConfig = map[string]any{"retrievalConfig": map[string]any{"latLng": location}}
		}
	} else if request.GeminiRetrievalLocation != nil {
		return result, geminiInvalid("retrieval_config")
	}
	if request.ToolChoice != nil {
		if len(request.Tools) == 0 {
			return result, geminiInvalid("tool_choice")
		}
		config := map[string]any{}
		switch choice := request.ToolChoice.(type) {
		case string:
			switch choice {
			case "auto":
				config["mode"] = "AUTO"
			case "none":
				config["mode"] = "NONE"
			case "required":
				config["mode"] = "ANY"
			default:
				return result, geminiInvalid("tool_choice")
			}
		case map[string]any:
			function, ok := choice["function"].(map[string]any)
			name, _ := function["name"].(string)
			if !ok || choice["type"] != "function" || name == "" {
				return result, geminiInvalid("tool_choice")
			}
			config["mode"] = "ANY"
			config["allowedFunctionNames"] = []string{name}
		default:
			return result, geminiInvalid("tool_choice")
		}
		if result.ToolConfig == nil {
			result.ToolConfig = map[string]any{}
		}
		result.ToolConfig["functionCallingConfig"] = config
	}
	return result, nil
}

func geminiMessageParts(value any) ([]geminiPart, error) {
	switch value := value.(type) {
	case nil:
		return nil, nil
	case string:
		if value == "" {
			return nil, nil
		}
		return []geminiPart{{Text: value}}, nil
	case []any:
		parts := make([]geminiPart, 0, len(value))
		for _, raw := range value {
			part, ok := raw.(map[string]any)
			if !ok {
				return nil, geminiInvalid("messages.content")
			}
			switch part["type"] {
			case "text":
				text, ok := part["text"].(string)
				if !ok {
					return nil, geminiInvalid("messages.content.text")
				}
				parts = append(parts, geminiPart{Text: text})
			case "image_url":
				image, _ := part["image_url"].(map[string]any)
				data, _ := image["url"].(string)
				attachment, err := openai.ParseDataImageURL(data)
				if err != nil {
					return nil, err
				}
				parts = append(parts, geminiPart{InlineData: &geminiInlineData{MIMEType: attachment.MediaType, Data: attachment.Data}})
			case "input_audio":
				attachments, err := openai.ResponseAudioAttachments([]any{part})
				if err != nil || len(attachments) != 1 {
					return nil, openai.ErrInvalidAudio
				}
				parts = append(parts, geminiPart{InlineData: &geminiInlineData{MIMEType: attachments[0].MediaType, Data: attachments[0].Data}})
			case "input_file":
				attachments, err := openai.ResponseFileAttachments([]any{part})
				if err != nil || len(attachments) != 1 {
					return nil, openai.ErrInvalidFileInput
				}
				parts = append(parts, geminiPart{InlineData: &geminiInlineData{MIMEType: attachments[0].MediaType, Data: attachments[0].Data}})
			case "input_video":
				attachments, err := openai.ChatVideoAttachments([]openai.Message{{Role: "user", Content: []any{part}}})
				if err != nil || len(attachments) != 1 {
					return nil, openai.ErrInvalidVideoInput
				}
				parts = append(parts, geminiPart{InlineData: &geminiInlineData{MIMEType: attachments[0].MediaType, Data: attachments[0].Data}})
			default:
				return nil, geminiInvalid("messages.content.type")
			}
		}
		return parts, nil
	default:
		return nil, geminiInvalid("messages.content")
	}
}

func geminiToolResponse(value any) map[string]any {
	if object, ok := value.(map[string]any); ok && object != nil {
		return object
	}
	if text, ok := value.(string); ok {
		var object map[string]any
		if json.Unmarshal([]byte(text), &object) == nil && object != nil {
			return object
		}
	}
	return map[string]any{"result": value}
}

func geminiBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(baseURL, "/v1beta") && !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1beta"
	}
	return baseURL
}

func (g Gemini) modelEndpoint(model, action string) (string, error) {
	model = strings.TrimPrefix(model, "models/")
	if model == "" || strings.ContainsAny(model, "/\\?#%") || model == "." || model == ".." {
		return "", geminiInvalid("model")
	}
	base, err := url.Parse(g.baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("invalid Gemini base URL")
	}
	baseURL := geminiBaseURL(g.baseURL)
	if g.vertex {
		if !validVertexGeminiBaseURL(g.baseURL) {
			return "", errors.New("invalid Vertex Gemini base URL")
		}
		baseURL = strings.TrimRight(g.baseURL, "/")
	}
	return baseURL + "/models/" + url.PathEscape(model) + ":" + action, nil
}

func (g Gemini) providerName() string {
	if g.errorProvider != "" {
		return g.errorProvider
	}
	return "gemini"
}

func (g Gemini) generate(ctx context.Context, request openai.ChatCompletionRequest, stream bool) (*http.Response, error) {
	body, err := geminiChatRequest(request)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	method := "generateContent"
	if stream {
		method = "streamGenerateContent"
	}
	endpoint, err := g.modelEndpoint(request.Model, method)
	if err != nil {
		return nil, err
	}
	if stream {
		endpoint += "?alt=sse"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := g.authorize(req); err != nil {
		return nil, err
	}
	response, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := responseStatusError(g.providerName(), response)
		_ = response.Body.Close()
		return nil, failure
	}
	return response, nil
}

func (g Gemini) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	response, err := g.generate(ctx, request, false)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if len(payload) > 32<<20 {
		return openai.ChatCompletionResponse{}, errors.New("Gemini response exceeds limit")
	}
	var body geminiResponse
	if err = json.Unmarshal(payload, &body); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	result, err := geminiToChat(body, request.Model)
	if err == nil && len(result.Choices) == 0 {
		err = errors.New("Gemini response produced no candidates")
	}
	if err == nil {
		for _, choice := range result.Choices {
			if reasoningErr := validateGeminiReasoning(choice.Message.Reasoning); reasoningErr != nil {
				err = reasoningErr
				break
			}
		}
	}
	if err == nil {
		err = validateRequestedChatChoices(request, result)
	}
	return result, err
}

func geminiToChat(body geminiResponse, model string) (openai.ChatCompletionResponse, error) {
	if body.Error != nil {
		code := body.Error.Code
		if code < 400 || code > 599 {
			code = 502
		}
		return openai.ChatCompletionResponse{}, statusError("gemini", code)
	}
	if body.PromptFeedback.BlockReason != "" {
		return openai.ChatCompletionResponse{}, &Error{Class: FailureContentPolicy, Provider: "gemini", StatusCode: 400, UpstreamCode: "content_policy_violation", Err: errors.New("upstream content policy rejected prompt")}
	}
	if body.Model != "" {
		model = body.Model
	}
	result := openai.ChatCompletionResponse{ID: body.ID, Object: "chat.completion", Model: model}
	if result.ID == "" {
		result.ID = "chatcmpl-" + rand.Text()
	}
	if body.Usage != nil {
		u := body.Usage
		inputTokens, completionTokens, valid := geminiTokenCounts(u)
		if !valid {
			return result, errors.New("inconsistent Gemini usage")
		}
		result.Usage = openai.Usage{PromptTokens: inputTokens, CompletionTokens: completionTokens, TotalTokens: u.Total, ProviderToolInputTokens: u.ToolUsePrompt}
		if u.Thoughts > 0 {
			result.Usage.CompletionTokensDetails = &openai.CompletionTokenDetails{ReasoningTokens: u.Thoughts}
		}
		if u.Cached > 0 {
			result.Usage.PromptTokensDetails = &openai.PromptTokenDetails{CachedTokens: u.Cached}
		}
		switch u.ServiceTier {
		case "":
		case "unspecified":
			result.ServiceTier = "default"
		case "standard", "flex", "priority":
			result.ServiceTier = u.ServiceTier
		default:
			return result, errors.New("invalid Gemini service tier")
		}
	}
	if len(body.Candidates) > maxChatStreamChoices {
		return result, errors.New("too many Gemini candidates")
	}
	seen := map[int]bool{}
	searchRequests := 0
	for _, candidate := range body.Candidates {
		if seen[candidate.Index] {
			return result, errors.New("duplicate Gemini candidate index")
		}
		seen[candidate.Index] = true
		if candidate.Index < 0 || candidate.Index >= maxChatStreamChoices {
			return result, errors.New("invalid Gemini candidate index")
		}
		choice := openai.Choice{Index: candidate.Index, Message: openai.Message{Role: "assistant"}}
		if candidate.LogprobsResult != nil {
			logprobs, err := geminiChoiceLogprobs(*candidate.LogprobsResult)
			if err != nil {
				return result, err
			}
			choice.Logprobs = &logprobs
		}
		var text strings.Builder
		for partIndex, part := range candidate.Content.Parts {
			if part.Thought {
				if part.Text == "" && part.ThoughtSignature == "" || part.InlineData != nil || part.FunctionCall != nil || part.FunctionResponse != nil {
					return result, errors.New("invalid Gemini thought part")
				}
				index := partIndex
				choice.Message.Reasoning = append(choice.Message.Reasoning, openai.ReasoningBlock{Index: &index, Type: "thinking", Thinking: part.Text, Signature: part.ThoughtSignature})
				continue
			}
			if part.InlineData != nil || part.FunctionResponse != nil {
				return result, errors.New("unsupported Gemini output modality")
			}
			if part.ExecutableCode != nil || part.CodeExecutionResult != nil {
				block := openai.GeminiCodeExecutionPart{Index: partIndex, Code: part.ExecutableCode, Result: part.CodeExecutionResult}
				if err := openai.ValidateGeminiCodeExecutionParts([]openai.GeminiCodeExecutionPart{block}); err != nil {
					return result, err
				}
				if part.Text != "" || part.FunctionCall != nil || part.ThoughtSignature != "" {
					return result, errors.New("invalid Gemini code execution part")
				}
				choice.Message.GeminiCodeExecutionParts = append(choice.Message.GeminiCodeExecutionParts, block)
				continue
			}
			text.WriteString(part.Text)
			if part.ThoughtSignature != "" && part.FunctionCall == nil {
				var err error
				choice.Message.NativeContent, err = openai.AddGeminiPartSignature(choice.Message.NativeContent, partIndex, part.ThoughtSignature)
				if err != nil {
					return result, err
				}
			}
			if part.FunctionCall != nil {
				if part.FunctionCall.Name == "" {
					return result, errors.New("invalid Gemini function call")
				}
				if len(choice.Message.ToolCalls) >= maxChatStreamToolCalls {
					return result, errors.New("too many Gemini tool calls")
				}
				arguments := part.FunctionCall.Args
				if arguments == nil {
					arguments = map[string]any{}
				}
				args, err := json.Marshal(arguments)
				if err != nil {
					return result, err
				}
				id := part.FunctionCall.ID
				if id == "" {
					id = "call_" + rand.Text()
				}
				call := openai.ToolCall{ID: id, Type: "function", Function: openai.FunctionCall{Name: part.FunctionCall.Name, Arguments: string(args)}}
				if part.ThoughtSignature != "" {
					call.ExtraContent = &openai.ToolCallExtraContent{Google: &openai.GoogleToolCallContent{ThoughtSignature: part.ThoughtSignature}}
				}
				choice.Message.ToolCalls = append(choice.Message.ToolCalls, call)
			}
		}
		if err := openai.ValidateGeminiCodeExecutionParts(choice.Message.GeminiCodeExecutionParts); err != nil {
			return result, err
		}
		choice.Message.Content = text.String()
		annotations, searches, err := geminiGrounding(candidate.Grounding, text.String())
		if err != nil {
			return result, err
		}
		if searchRequests > openai.WebSearchMaxUses-searches {
			return result, errors.New("Gemini search usage exceeds limit")
		}
		searchRequests += searches
		choice.Message.Annotations = annotations
		if len(candidate.Grounding) > 0 {
			choice.GeminiGroundingMetadata = append(json.RawMessage(nil), candidate.Grounding...)
		}
		if err := openai.ValidateGeminiURLContextMetadata(candidate.URLContext); err != nil {
			return result, err
		}
		if len(candidate.URLContext) > 0 {
			choice.GeminiURLContextMetadata = append(json.RawMessage(nil), candidate.URLContext...)
		}
		switch candidate.FinishReason {
		case "":
		case "STOP":
			choice.FinishReason = "stop"
		case "MAX_TOKENS":
			choice.FinishReason = "length"
		case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
			choice.FinishReason = "content_filter"
		default:
			return result, errors.New("Gemini generation failed")
		}
		if len(choice.Message.ToolCalls) > 0 && choice.FinishReason == "stop" {
			choice.FinishReason = "tool_calls"
		}
		result.Choices = append(result.Choices, choice)
	}
	result.Usage.SearchRequests = searchRequests
	return result, nil
}

func geminiGrounding(raw json.RawMessage, text string) ([]openai.ChatAnnotation, int, error) {
	if len(raw) == 0 {
		return nil, 0, nil
	}
	if len(raw) > 1<<20 {
		return nil, 0, errors.New("Gemini grounding metadata exceeds limit")
	}
	var metadata struct {
		Chunks []struct {
			Web *struct {
				URI   string `json:"uri"`
				Title string `json:"title"`
			} `json:"web"`
			Maps *struct {
				URI                string `json:"uri"`
				Title              string `json:"title"`
				Text               string `json:"text"`
				PlaceID            string `json:"placeId"`
				PlaceAnswerSources *struct {
					Reviews []struct {
						ReviewID      string `json:"reviewId"`
						GoogleMapsURI string `json:"googleMapsUri"`
						Title         string `json:"title"`
					} `json:"reviewSnippets"`
				} `json:"placeAnswerSources"`
			} `json:"maps"`
		} `json:"groundingChunks"`
		Supports []struct {
			Indices []int `json:"groundingChunkIndices"`
			Segment struct {
				Start int    `json:"startIndex"`
				End   int    `json:"endIndex"`
				Text  string `json:"text"`
			} `json:"segment"`
		} `json:"groundingSupports"`
		Queries    []string `json:"webSearchQueries"`
		MapsWidget string   `json:"googleMapsWidgetContextToken"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata.Chunks == nil && metadata.Supports == nil && metadata.Queries == nil && metadata.MapsWidget == "" {
		return nil, 0, errors.New("invalid Gemini grounding metadata")
	}
	if len(metadata.Queries) > openai.WebSearchMaxUses || len(metadata.Chunks) > 128 || len(metadata.Supports) > 128 {
		return nil, 0, errors.New("Gemini grounding metadata exceeds limit")
	}
	for _, query := range metadata.Queries {
		if strings.TrimSpace(query) == "" || len(query) > 8192 {
			return nil, 0, errors.New("invalid Gemini search query metadata")
		}
	}
	if len(metadata.MapsWidget) > 65536 {
		return nil, 0, errors.New("invalid Gemini Maps widget token")
	}
	mapsUsed := metadata.MapsWidget != ""
	for _, chunk := range metadata.Chunks {
		if (chunk.Web == nil) == (chunk.Maps == nil) {
			return nil, 0, errors.New("invalid Gemini grounding source")
		}
		if chunk.Web != nil {
			if !validGeminiGroundingURL(chunk.Web.URI, false) || len(chunk.Web.Title) > 8192 {
				return nil, 0, errors.New("invalid Gemini grounding source")
			}
			continue
		}
		mapsUsed = true
		maps := chunk.Maps
		if !validGeminiGroundingURL(maps.URI, true) || strings.TrimSpace(maps.Title) == "" || len(maps.Title) > 8192 || len(maps.Text) > 65536 || !strings.HasPrefix(maps.PlaceID, "places/") || len(maps.PlaceID) > 512 {
			return nil, 0, errors.New("invalid Gemini Maps grounding source")
		}
		if maps.PlaceAnswerSources != nil {
			if len(maps.PlaceAnswerSources.Reviews) > 32 {
				return nil, 0, errors.New("Gemini Maps grounding metadata exceeds limit")
			}
			for _, review := range maps.PlaceAnswerSources.Reviews {
				if strings.TrimSpace(review.ReviewID) == "" || len(review.ReviewID) > 512 || !validGeminiGroundingURL(review.GoogleMapsURI, true) || len(review.Title) > 8192 {
					return nil, 0, errors.New("invalid Gemini Maps review source")
				}
			}
		}
	}
	annotations := make([]openai.ChatAnnotation, 0)
	for _, support := range metadata.Supports {
		if support.Segment.Start < 0 || support.Segment.End < support.Segment.Start || support.Segment.End > len(text) || support.Segment.Text != text[support.Segment.Start:support.Segment.End] || len(support.Indices) == 0 {
			return nil, 0, errors.New("invalid Gemini grounding support")
		}
		seenIndices := map[int]bool{}
		for _, index := range support.Indices {
			if index < 0 || index >= len(metadata.Chunks) || seenIndices[index] || len(annotations) >= 128 {
				return nil, 0, errors.New("invalid Gemini grounding support")
			}
			seenIndices[index] = true
			chunk := metadata.Chunks[index]
			if chunk.Web != nil {
				annotations = append(annotations, openai.ChatAnnotation{Type: "url_citation", URLCitation: &openai.ChatURLCitation{StartIndex: support.Segment.Start, EndIndex: support.Segment.End, Title: chunk.Web.Title, URL: chunk.Web.URI}})
			} else {
				annotations = append(annotations, openai.ChatAnnotation{Type: "url_citation", URLCitation: &openai.ChatURLCitation{StartIndex: support.Segment.Start, EndIndex: support.Segment.End, Title: chunk.Maps.Title, URL: chunk.Maps.URI}})
			}
		}
	}
	if err := openai.ValidateChatAnnotations(annotations); err != nil {
		return nil, 0, err
	}
	searches := len(metadata.Queries)
	if mapsUsed {
		searches++
	}
	return annotations, searches, nil
}

func validGeminiGroundingURL(value string, httpsOnly bool) bool {
	parsed, err := url.Parse(value)
	validScheme := parsed.Scheme == "https" || !httpsOnly && parsed.Scheme == "http"
	return err == nil && validScheme && parsed.Host != "" && parsed.User == nil && len(value) <= 8192
}

func validateGeminiReasoning(blocks []openai.ReasoningBlock) error {
	if err := openai.ValidateBedrockReasoningBlocks(blocks); err != nil {
		return fmt.Errorf("invalid Gemini reasoning: %w", err)
	}
	for _, block := range blocks {
		if block.Type != "thinking" || block.Signature != "" && !validGeminiBase64(block.Signature) {
			return errors.New("invalid Gemini reasoning block")
		}
	}
	return nil
}

func validGeminiBase64(value string) bool {
	_, err := base64.StdEncoding.DecodeString(value)
	return err == nil
}

func geminiChoiceLogprobs(result geminiLogprobsResult) (openai.ChoiceLogprobs, error) {
	if len(result.ChosenCandidates) != len(result.TopCandidates) {
		return openai.ChoiceLogprobs{}, errors.New("inconsistent Gemini logprobs")
	}
	converted := openai.ChoiceLogprobs{Content: make([]openai.TokenLogprob, 0, len(result.ChosenCandidates))}
	for index, chosen := range result.ChosenCandidates {
		if !finiteProbability(chosen.LogProbability) || len(result.TopCandidates[index].Candidates) > 20 {
			return openai.ChoiceLogprobs{}, errors.New("invalid Gemini logprobs")
		}
		token := openai.TokenLogprob{Token: chosen.Token, Logprob: chosen.LogProbability, Bytes: tokenBytes(chosen.Token)}
		for _, candidate := range result.TopCandidates[index].Candidates {
			if !finiteProbability(candidate.LogProbability) {
				return openai.ChoiceLogprobs{}, errors.New("invalid Gemini logprobs")
			}
			token.TopLogprobs = append(token.TopLogprobs, openai.TopLogprob{Token: candidate.Token, Logprob: candidate.LogProbability, Bytes: tokenBytes(candidate.Token)})
		}
		converted.Content = append(converted.Content, token)
	}
	return converted, nil
}

func finiteProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value <= 0
}

func tokenBytes(token string) []int {
	data := []byte(token)
	result := make([]int, len(data))
	for index, value := range data {
		result[index] = int(value)
	}
	return result
}

func (g Gemini) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := g.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if !g.upstreamStream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}
	response, err := g.generate(ctx, request, true)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer response.Body.Close()
	result := openai.ChatCompletionResponse{ID: "chatcmpl-" + rand.Text(), Object: "chat.completion", Model: request.Model}
	consumed := 0
	searchRequests := 0
	err = scanSSEData(response.Body, func(payload string) error {
		consumed += len(payload)
		if consumed > 64<<20 {
			return errors.New("Gemini stream exceeds limit")
		}
		var body geminiResponse
		if err := json.Unmarshal([]byte(payload), &body); err != nil {
			return err
		}
		if body.ID != "" {
			result.ID = body.ID
		}
		body.ID = result.ID
		chunk, err := geminiToChat(body, result.Model)
		if err != nil {
			return err
		}
		result.Model = chunk.Model
		if searchRequests > openai.WebSearchMaxUses-chunk.Usage.SearchRequests {
			return errors.New("Gemini search usage exceeds limit")
		}
		searchRequests += chunk.Usage.SearchRequests
		if chunk.ServiceTier != "" {
			if result.ServiceTier != "" && result.ServiceTier != chunk.ServiceTier {
				return errors.New("Gemini service tier changed during stream")
			}
			result.ServiceTier = chunk.ServiceTier
		}
		if body.Usage != nil {
			result.Usage = chunk.Usage
		}
		choices := make([]map[string]any, 0, len(chunk.Choices))
		for _, choice := range chunk.Choices {
			for len(result.Choices) <= choice.Index {
				result.Choices = append(result.Choices, openai.Choice{Index: len(result.Choices), Message: openai.Message{Role: "assistant"}})
			}
			current := &result.Choices[choice.Index]
			if len(choice.GeminiGroundingMetadata) > 0 {
				current.GeminiGroundingMetadata = append(json.RawMessage(nil), choice.GeminiGroundingMetadata...)
			}
			if len(choice.GeminiURLContextMetadata) > 0 {
				current.GeminiURLContextMetadata = append(json.RawMessage(nil), choice.GeminiURLContextMetadata...)
			}
			if choice.Logprobs != nil {
				if current.Logprobs == nil {
					current.Logprobs = &openai.ChoiceLogprobs{}
				}
				current.Logprobs.Content = append(current.Logprobs.Content, choice.Logprobs.Content...)
			}
			current.Message.Content = openai.ContentText(current.Message.Content) + openai.ContentText(choice.Message.Content)
			for _, block := range choice.Message.GeminiCodeExecutionParts {
				block.Index = len(current.Message.GeminiCodeExecutionParts)
				current.Message.GeminiCodeExecutionParts = append(current.Message.GeminiCodeExecutionParts, block)
			}
			if err := openai.ValidateGeminiCodeExecutionParts(current.Message.GeminiCodeExecutionParts); err != nil {
				return err
			}
			incomingSignatures, err := openai.GeminiPartSignatures(choice.Message.NativeContent)
			if err != nil {
				return err
			}
			storedSignatures, err := openai.GeminiPartSignatures(current.Message.NativeContent)
			if err != nil {
				return err
			}
			for _, incoming := range incomingSignatures {
				found := false
				for _, stored := range storedSignatures {
					if stored.Index == incoming.Index {
						if stored.Signature != incoming.Signature {
							return errors.New("Gemini part signature changed during stream")
						}
						found = true
					}
				}
				if !found {
					current.Message.NativeContent, err = openai.AddGeminiPartSignature(current.Message.NativeContent, incoming.Index, incoming.Signature)
					if err != nil {
						return err
					}
					storedSignatures = append(storedSignatures, incoming)
				}
			}
			for _, block := range choice.Message.Reasoning {
				stored := -1
				if block.Index != nil {
					for index := range current.Message.Reasoning {
						if current.Message.Reasoning[index].Index != nil && *current.Message.Reasoning[index].Index == *block.Index {
							stored = index
							break
						}
					}
				}
				if stored < 0 {
					current.Message.Reasoning = append(current.Message.Reasoning, block)
					continue
				}
				current.Message.Reasoning[stored].Thinking += block.Thinking
				if block.Signature != "" {
					if current.Message.Reasoning[stored].Signature != "" && current.Message.Reasoning[stored].Signature != block.Signature {
						return errors.New("Gemini thought signature changed during stream")
					}
					current.Message.Reasoning[stored].Signature = block.Signature
				}
			}
			for i := range choice.Message.ToolCalls {
				index := len(current.Message.ToolCalls)
				if index >= maxChatStreamToolCalls {
					return errors.New("too many Gemini tool calls")
				}
				choice.Message.ToolCalls[i].Index = &index
				current.Message.ToolCalls = append(current.Message.ToolCalls, choice.Message.ToolCalls[i])
			}
			if choice.FinishReason == "stop" && len(current.Message.ToolCalls) > 0 {
				choice.FinishReason = "tool_calls"
			}
			if choice.FinishReason != "" {
				current.FinishReason = choice.FinishReason
			}
			var finish any
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			wireChoice := map[string]any{"index": choice.Index, "delta": choice.Message, "finish_reason": finish, "logprobs": choice.Logprobs}
			if len(choice.GeminiGroundingMetadata) > 0 {
				wireChoice["gemini_grounding_metadata"] = choice.GeminiGroundingMetadata
			}
			if len(choice.GeminiURLContextMetadata) > 0 {
				wireChoice["gemini_url_context_metadata"] = choice.GeminiURLContextMetadata
			}
			choices = append(choices, wireChoice)
		}
		event := map[string]any{"id": result.ID, "object": "chat.completion.chunk", "model": result.Model, "choices": choices}
		if chunk.ServiceTier != "" {
			event["service_tier"] = chunk.ServiceTier
		}
		if body.Usage != nil {
			event["usage"] = chunk.Usage
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		return write(string(encoded))
	})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	result.Usage.SearchRequests = searchRequests
	if len(result.Choices) == 0 {
		return openai.ChatCompletionResponse{}, errors.New("Gemini stream produced no candidates")
	}
	for _, choice := range result.Choices {
		if choice.FinishReason == "" {
			return openai.ChatCompletionResponse{}, errors.New("Gemini stream ended before completion")
		}
		if err := validateGeminiReasoning(choice.Message.Reasoning); err != nil {
			return openai.ChatCompletionResponse{}, err
		}
	}
	if err := validateRequestedChatChoices(request, result); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return result, nil
}
