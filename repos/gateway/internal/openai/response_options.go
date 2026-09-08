package openai

// Validate checks provider-independent Responses generation options.
func (r ResponseRequest) Validate() string {
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
	return ""
}
