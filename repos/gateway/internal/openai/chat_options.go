package openai

import "strconv"

// ChatGenerationOptions contains optional controls shared with compatible wire
// requests. Pointer fields preserve explicitly supplied false and zero values.
type ChatGenerationOptions struct {
	ReasoningEffort  string         `json:"reasoning_effort,omitempty"`
	Logprobs         *bool          `json:"logprobs,omitempty"`
	TopLogprobs      *int           `json:"top_logprobs,omitempty"`
	FrequencyPenalty *float64       `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64       `json:"presence_penalty,omitempty"`
	LogitBias        map[string]int `json:"logit_bias,omitempty"`
}

func (o ChatGenerationOptions) Validate() string {
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
