package openai

import (
	"math"
	"unicode/utf8"
)

// Validate checks provider-independent Responses generation options.
func (r ResponseRequest) Validate() string {
	if message := ValidateMetadata(r.Metadata); message != "" {
		return message
	}
	if utf8.RuneCountInString(r.SafetyIdentifier) > 64 {
		return "safety_identifier must contain at most 64 characters"
	}
	if !validServiceTier(r.ServiceTier) {
		return "unsupported service_tier value"
	}
	if _, _, valid := responseTextVerbosity(r.Text); !valid {
		return "text.verbosity must be low, medium, or high"
	}
	if r.MaxOutputTokens != nil && r.MaxTokens != nil {
		return "max_output_tokens and max_tokens are mutually exclusive"
	}
	if r.MaxOutputTokens != nil && *r.MaxOutputTokens <= 0 {
		return "max_output_tokens must be positive"
	}
	if r.MaxTokens != nil && *r.MaxTokens <= 0 {
		return "max_tokens must be positive"
	}
	if r.TopLogprobs != nil && (*r.TopLogprobs < 0 || *r.TopLogprobs > 20) {
		return "top_logprobs must be between 0 and 20"
	}
	if r.Truncation != nil && *r.Truncation != "auto" && *r.Truncation != "disabled" {
		return "truncation must be auto or disabled"
	}
	for _, penalty := range []*float64{r.FrequencyPenalty, r.PresencePenalty} {
		if penalty != nil && (math.IsNaN(*penalty) || math.IsInf(*penalty, 0) || *penalty < -2 || *penalty > 2) {
			return "frequency_penalty and presence_penalty must be between -2 and 2"
		}
	}
	if r.MaxToolCalls != nil && (*r.MaxToolCalls < 1 || *r.MaxToolCalls > 1000) {
		return "max_tool_calls must be between 1 and 1000"
	}
	return ""
}

// ValidateMetadata checks the shared metadata limits used by inference contracts.
func ValidateMetadata(metadata map[string]string) string {
	if len(metadata) > 16 {
		return "metadata must contain at most 16 entries"
	}
	for key, value := range metadata {
		if utf8.RuneCountInString(key) > 64 || utf8.RuneCountInString(value) > 512 {
			return "metadata keys must be at most 64 characters and values at most 512 characters"
		}
	}
	return ""
}
