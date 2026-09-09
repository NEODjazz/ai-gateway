package openai

import (
	"strconv"
	"unicode/utf8"
)

const WebSearchMaxUses = 5

// ChatGenerationOptions contains optional controls shared with compatible wire
// requests. Pointer fields preserve explicitly supplied false and zero values.
type ChatGenerationOptions struct {
	Metadata             map[string]string     `json:"metadata,omitempty"`
	Store                *bool                 `json:"store,omitempty"`
	Modalities           []string              `json:"modalities,omitempty"`
	Audio                *ChatAudioOptions     `json:"audio,omitempty"`
	ReasoningEffort      string                `json:"reasoning_effort,omitempty"`
	N                    *int                  `json:"n,omitempty"`
	SafetyIdentifier     string                `json:"safety_identifier,omitempty"`
	PromptCacheKey       string                `json:"prompt_cache_key,omitempty"`
	PromptCacheOptions   *PromptCacheOptions   `json:"prompt_cache_options,omitempty"`
	PromptCacheRetention string                `json:"prompt_cache_retention,omitempty"`
	Prediction           *ChatPrediction       `json:"prediction,omitempty"`
	ServiceTier          string                `json:"service_tier,omitempty"`
	User                 string                `json:"user,omitempty"`
	Verbosity            string                `json:"verbosity,omitempty"`
	WebSearchOptions     *ChatWebSearchOptions `json:"web_search_options,omitempty"`
	Logprobs             *bool                 `json:"logprobs,omitempty"`
	TopLogprobs          *int                  `json:"top_logprobs,omitempty"`
	FrequencyPenalty     *float64              `json:"frequency_penalty,omitempty"`
	PresencePenalty      *float64              `json:"presence_penalty,omitempty"`
	LogitBias            map[string]int        `json:"logit_bias,omitempty"`
}

type ChatWebSearchOptions struct {
	SearchContextSize string                     `json:"search_context_size,omitempty"`
	UserLocation      *ChatWebSearchUserLocation `json:"user_location,omitempty"`
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
	Mode string `json:"mode,omitempty"`
	TTL  string `json:"ttl,omitempty"`
}

type ChatPrediction struct {
	Content any    `json:"content"`
	Type    string `json:"type"`
}

func (o ChatGenerationOptions) Validate() string {
	if message := ValidateMetadata(o.Metadata); message != "" {
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
		switch o.PromptCacheOptions.Mode {
		case "", "implicit", "explicit":
		default:
			return "prompt_cache_options.mode must be implicit or explicit"
		}
		if o.PromptCacheOptions.TTL != "" && o.PromptCacheOptions.TTL != "30m" {
			return "prompt_cache_options.ttl must be 30m"
		}
	}
	if o.PromptCacheRetention != "" && o.PromptCacheRetention != "in_memory" && o.PromptCacheRetention != "24h" {
		return "prompt_cache_retention must be in_memory or 24h"
	}
	if message := validateChatPrediction(o.Prediction); message != "" {
		return message
	}
	if !validServiceTier(o.ServiceTier) {
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
	}
	switch o.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
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
		if value != nil && (*value < -2 || *value > 2) {
			return "frequency_penalty and presence_penalty must be between -2 and 2"
		}
	}
	for token, bias := range o.LogitBias {
		if _, err := strconv.ParseUint(token, 10, 64); err != nil || bias < -100 || bias > 100 {
			return "logit_bias requires nonnegative token IDs and biases between -100 and 100"
		}
	}
	return ""
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

func validServiceTier(value string) bool {
	switch value {
	case "", "auto", "default", "flex", "scale", "priority", "fast", "ultrafast":
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
