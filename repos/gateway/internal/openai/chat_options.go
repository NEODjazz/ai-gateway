package openai

import (
	"math"
	"strconv"
	"unicode/utf8"
)

const WebSearchMaxUses = 5
const WebFetchMaxUses = 5
const WebFetchMaxContentTokens = 100000

// ChatGenerationOptions contains optional controls shared with compatible wire
// requests. Pointer fields preserve explicitly supplied false and zero values.
type ChatGenerationOptions struct {
	Metadata             map[string]string     `json:"metadata,omitempty"`
	Store                *bool                 `json:"store,omitempty"`
	Modalities           []string              `json:"modalities,omitempty"`
	Audio                *ChatAudioOptions     `json:"audio,omitempty"`
	Moderation           *ProviderModeration   `json:"moderation,omitempty"`
	ClearThinking        *bool                 `json:"clear_thinking,omitempty"`
	ReasoningEffort      string                `json:"reasoning_effort,omitempty"`
	SafePrompt           *bool                 `json:"safe_prompt,omitempty"`
	N                    *int                  `json:"n,omitempty"`
	SafetyIdentifier     string                `json:"safety_identifier,omitempty"`
	PromptCacheKey       string                `json:"prompt_cache_key,omitempty"`
	PromptCacheOptions   *PromptCacheOptions   `json:"prompt_cache_options,omitempty"`
	PromptCacheRetention string                `json:"prompt_cache_retention,omitempty"`
	PromptMode           string                `json:"prompt_mode,omitempty"`
	Prediction           *ChatPrediction       `json:"prediction,omitempty"`
	ServiceTier          string                `json:"service_tier,omitempty"`
	User                 string                `json:"user,omitempty"`
	Verbosity            string                `json:"verbosity,omitempty"`
	WebSearchOptions     *ChatWebSearchOptions `json:"web_search_options,omitempty"`
	WebFetchOptions      *ChatWebFetchOptions  `json:"web_fetch_options,omitempty"`
	Logprobs             *bool                 `json:"logprobs,omitempty"`
	TopLogprobs          *int                  `json:"top_logprobs,omitempty"`
	FrequencyPenalty     *float64              `json:"frequency_penalty,omitempty"`
	PresencePenalty      *float64              `json:"presence_penalty,omitempty"`
	MinP                 *float64              `json:"min_p,omitempty"`
	TopK                 *int                  `json:"top_k,omitempty"`
	TopA                 *float64              `json:"top_a,omitempty"`
	RepetitionPenalty    *float64              `json:"repetition_penalty,omitempty"`
	LogitBias            map[string]int        `json:"logit_bias,omitempty"`
}

type ChatWebSearchOptions struct {
	SearchContextSize string                     `json:"search_context_size,omitempty"`
	UserLocation      *ChatWebSearchUserLocation `json:"user_location,omitempty"`
	MaxUses           *int                       `json:"max_uses,omitempty"`
	NativeType        string                     `json:"-"`
	AllowedDomains    []string                   `json:"-"`
	BlockedDomains    []string                   `json:"-"`
	AllowedCallers    []string                   `json:"-"`
	ResponseInclusion string                     `json:"-"`
	GeminiTimeRange   *GeminiSearchTimeRange     `json:"-"`
}

type GeminiSearchTimeRange struct {
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
}

type ChatWebFetchOptions struct {
	AllowedDomains    []string `json:"allowed_domains"`
	MaxUses           *int     `json:"max_uses,omitempty"`
	MaxContentTokens  int      `json:"max_content_tokens"`
	NativeType        string   `json:"-"`
	AllowedCallers    []string `json:"-"`
	UseCache          *bool    `json:"-"`
	ResponseInclusion string   `json:"-"`
}

type ChatWebSearchUserLocation struct {
	Type        string                            `json:"type"`
	Approximate *ChatWebSearchApproximateLocation `json:"approximate"`
}

type ChatWebSearchApproximateLocation struct {
	City     string `json:"city,omitempty"`
	Country  string `json:"country,omitempty"`
	Region   string `json:"region,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

type PromptCacheOptions struct {
	Mode                 string `json:"mode,omitempty"`
	TTL                  string `json:"ttl,omitempty"`
	ComparisonResponseID string `json:"comparison_response_id,omitempty"`
	Prewarm              *bool  `json:"prewarm,omitempty"`
}

type ChatPrediction struct {
	Content any    `json:"content"`
	Type    string `json:"type"`
}

func (o ChatGenerationOptions) Validate() string {
	if message := ValidateMetadata(o.Metadata); message != "" {
		return message
	}
	if message := validateProviderModeration(o.Moderation); message != "" {
		return message
	}
	if o.N != nil && (*o.N < 1 || *o.N > 128) {
		return "n must be between 1 and 128"
	}
	hasText, hasAudio := false, false
	for _, modality := range o.Modalities {
		switch modality {
		case "text":
			if hasText {
				return "modalities must contain unique text or audio values"
			}
			hasText = true
		case "audio":
			if hasAudio {
				return "modalities must contain unique text or audio values"
			}
			hasAudio = true
		default:
			return "modalities must contain unique text or audio values"
		}
	}
	if o.Modalities != nil && len(o.Modalities) == 0 {
		return "modalities must contain unique text or audio values"
	}
	if hasAudio {
		if message := validateChatAudioOptions(o.Audio); message != "" {
			return message
		}
	} else if o.Audio != nil {
		return "audio requires the audio output modality"
	}
	if utf8.RuneCountInString(o.SafetyIdentifier) > 64 {
		return "safety_identifier must contain at most 64 characters"
	}
	if o.PromptCacheOptions != nil {
		if message := ValidatePromptCacheOptions(o.PromptCacheOptions); message != "" {
			return message
		}
		if o.PromptCacheOptions.Prewarm != nil {
			return "prompt_cache_options.prewarm is only supported by Responses"
		}
		if o.PromptCacheOptions.ComparisonResponseID != "" {
			return "prompt_cache_options.comparison_response_id is only supported by Responses"
		}
	}
	if o.PromptCacheRetention != "" && o.PromptCacheRetention != "in_memory" && o.PromptCacheRetention != "24h" {
		return "prompt_cache_retention must be in_memory or 24h"
	}
	if o.PromptMode != "" && o.PromptMode != "reasoning" {
		return "prompt_mode must be reasoning"
	}
	if message := validateChatPrediction(o.Prediction); message != "" {
		return message
	}
	if !ValidServiceTier(o.ServiceTier) {
		return "unsupported service_tier value"
	}
	if !validVerbosity(o.Verbosity) {
		return "verbosity must be low, medium, or high"
	}
	if o.WebSearchOptions != nil {
		switch o.WebSearchOptions.SearchContextSize {
		case "", "low", "medium", "high":
		default:
			return "web_search_options.search_context_size must be low, medium, or high"
		}
		if location := o.WebSearchOptions.UserLocation; location != nil && (location.Type != "approximate" || location.Approximate == nil) {
			return "web_search_options.user_location requires type=approximate and approximate"
		}
		if o.WebSearchOptions.MaxUses != nil && (*o.WebSearchOptions.MaxUses < 1 || *o.WebSearchOptions.MaxUses > WebSearchMaxUses) {
			return "web_search_options.max_uses must be between 1 and 5"
		}
		if len(o.WebSearchOptions.AllowedDomains) > 0 && len(o.WebSearchOptions.BlockedDomains) > 0 {
			return "web search allowed_domains and blocked_domains are mutually exclusive"
		}
		if !validNativeSearchDomains(o.WebSearchOptions.AllowedDomains) || !validNativeSearchDomains(o.WebSearchOptions.BlockedDomains) {
			return "web search domains contain an invalid or duplicate value"
		}
		if !validNativeAllowedCallers(o.WebSearchOptions.AllowedCallers) {
			return "web search allowed_callers contains an invalid or duplicate value"
		}
		if o.WebSearchOptions.ResponseInclusion != "" && o.WebSearchOptions.ResponseInclusion != "full" && o.WebSearchOptions.ResponseInclusion != "excluded" {
			return "web search response_inclusion must be full or excluded"
		}
	}
	if options := o.WebFetchOptions; options != nil {
		if len(options.AllowedDomains) == 0 || len(options.AllowedDomains) > MaxSearchDomains {
			return "web_fetch_options.allowed_domains must contain between 1 and 20 domains"
		}
		seen := map[string]bool{}
		for _, domain := range options.AllowedDomains {
			if !validSearchDomain(domain) || seen[domain] {
				return "web_fetch_options.allowed_domains contains an invalid or duplicate domain"
			}
			seen[domain] = true
		}
		if options.MaxUses != nil && (*options.MaxUses < 1 || *options.MaxUses > WebFetchMaxUses) {
			return "web_fetch_options.max_uses must be between 1 and 5"
		}
		if options.MaxContentTokens < 1 || options.MaxContentTokens > WebFetchMaxContentTokens {
			return "web_fetch_options.max_content_tokens must be between 1 and 100000"
		}
		if !validNativeAllowedCallers(options.AllowedCallers) {
			return "web fetch allowed_callers contains an invalid or duplicate value"
		}
		if options.ResponseInclusion != "" && options.ResponseInclusion != "full" && options.ResponseInclusion != "excluded" {
			return "web fetch response_inclusion must be full or excluded"
		}
	}
	switch o.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "default":
	default:
		return "unsupported reasoning_effort value"
	}
	if o.TopLogprobs != nil {
		if *o.TopLogprobs < 0 || *o.TopLogprobs > 20 {
			return "top_logprobs must be between 0 and 20"
		}
		if o.Logprobs == nil || !*o.Logprobs {
			return "top_logprobs requires logprobs=true"
		}
	}
	for _, value := range []*float64{o.FrequencyPenalty, o.PresencePenalty} {
		if value != nil && (!finiteFloat(*value) || *value < -2 || *value > 2) {
			return "frequency_penalty and presence_penalty must be between -2 and 2"
		}
	}
	for _, value := range []*float64{o.MinP, o.TopA} {
		if value != nil && (!finiteFloat(*value) || *value < 0 || *value > 1) {
			return "min_p and top_a must be between 0 and 1"
		}
	}
	if o.TopK != nil && (*o.TopK < 0 || *o.TopK > 1000000) {
		return "top_k must be between 0 and 1000000"
	}
	if o.RepetitionPenalty != nil && (!finiteFloat(*o.RepetitionPenalty) || *o.RepetitionPenalty <= 0) {
		return "repetition_penalty must be greater than 0"
	}
	for token, bias := range o.LogitBias {
		if _, err := strconv.ParseUint(token, 10, 64); err != nil || bias < -100 || bias > 100 {
			return "logit_bias requires nonnegative token IDs and biases between -100 and 100"
		}
	}
	return ""
}

// ValidatePromptCacheOptions checks the shared Chat and Responses prompt-cache
// controls before an adapter can forward them.
func ValidatePromptCacheOptions(options *PromptCacheOptions) string {
	if options == nil {
		return ""
	}
	switch options.Mode {
	case "", "implicit", "explicit":
	default:
		return "prompt_cache_options.mode must be implicit or explicit"
	}
	if options.TTL != "" && options.TTL != "30m" {
		return "prompt_cache_options.ttl must be 30m"
	}
	if utf8.RuneCountInString(options.ComparisonResponseID) > 256 {
		return "prompt_cache_options.comparison_response_id must contain at most 256 characters"
	}
	return ""
}

func validNativeSearchDomains(domains []string) bool {
	if len(domains) > MaxSearchDomains {
		return false
	}
	seen := map[string]bool{}
	for _, domain := range domains {
		if !validSearchDomain(domain) || seen[domain] {
			return false
		}
		seen[domain] = true
	}
	return true
}

func validNativeAllowedCallers(callers []string) bool {
	if len(callers) > 4 {
		return false
	}
	seen := map[string]bool{}
	for _, caller := range callers {
		switch caller {
		case "direct", "code_execution_20250825", "code_execution_20260120", "code_execution_20260521":
		default:
			return false
		}
		if seen[caller] {
			return false
		}
		seen[caller] = true
	}
	return true
}

func finiteFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateChatPrediction(prediction *ChatPrediction) string {
	if prediction == nil {
		return ""
	}
	if prediction.Type != "content" {
		return "prediction.type must be content"
	}
	switch content := prediction.Content.(type) {
	case string:
		return ""
	case []any:
		for _, item := range content {
			part, ok := item.(map[string]any)
			if !ok || !validPredictionTextPart(part) {
				return "prediction.content must be text or an array of text parts"
			}
		}
		return ""
	default:
		return "prediction.content must be text or an array of text parts"
	}
}

func validPredictionTextPart(part map[string]any) bool {
	for key := range part {
		if key != "type" && key != "text" && key != "prompt_cache_breakpoint" {
			return false
		}
	}
	typeName, typeOK := part["type"].(string)
	_, textOK := part["text"].(string)
	if !typeOK || typeName != "text" || !textOK {
		return false
	}
	breakpoint, supplied := part["prompt_cache_breakpoint"]
	if !supplied || breakpoint == nil {
		return true
	}
	value, ok := breakpoint.(map[string]any)
	if !ok || len(value) != 1 {
		return false
	}
	mode, ok := value["mode"].(string)
	return ok && mode == "explicit"
}

func validVerbosity(value string) bool {
	switch value {
	case "", "low", "medium", "high":
		return true
	default:
		return false
	}
}

// ResponseTextVerbosity returns a Responses text verbosity value and whether
// the field was supplied. JSON null is treated as omitted.
func ResponseTextVerbosity(text any) (string, bool) {
	verbosity, supplied, _ := responseTextVerbosity(text)
	return verbosity, supplied
}

func responseTextVerbosity(text any) (string, bool, bool) {
	config, ok := text.(map[string]any)
	if !ok {
		return "", false, true
	}
	value, supplied := config["verbosity"]
	if !supplied || value == nil {
		return "", false, true
	}
	verbosity, ok := value.(string)
	if !ok {
		return "", true, false
	}
	return verbosity, true, validVerbosity(verbosity)
}

func ValidServiceTier(value string) bool {
	switch value {
	case "", "auto", "default", "on_demand", "flex", "performance", "scale", "priority", "fast", "ultrafast", "standard_only":
		return true
	default:
		return false
	}
}

func ValidReportedServiceTier(value string) bool {
	if ValidServiceTier(value) {
		return true
	}
	switch value {
	case "standard", "batch":
		return true
	default:
		return false
	}
}

type ChoiceLogprobs struct {
	Content []TokenLogprob `json:"content,omitempty"`
	Refusal []TokenLogprob `json:"refusal,omitempty"`
}

type TokenLogprob struct {
	Token       string       `json:"token"`
	Logprob     float64      `json:"logprob"`
	Bytes       []int        `json:"bytes"`
	TopLogprobs []TopLogprob `json:"top_logprobs"`
}

type TopLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes"`
}
