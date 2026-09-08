package openai

// Validate checks provider-independent Responses generation options.
func (r ResponseRequest) Validate() string {
	if r.TopLogprobs != nil && (*r.TopLogprobs < 0 || *r.TopLogprobs > 20) {
		return "top_logprobs must be between 0 and 20"
	}
	if r.Truncation != nil && *r.Truncation != "auto" && *r.Truncation != "disabled" {
		return "truncation must be auto or disabled"
	}
	return ""
}
