package openai

import "encoding/json"

// DefaultOutputTokenReserve is used when a generation request has no output cap.
const DefaultOutputTokenReserve = 1024

// ChatOutputLimit normalizes the two mutually exclusive wire parameters.
func ChatOutputLimit(r ChatCompletionRequest) int {
	if r.MaxCompletionTokens != nil {
		return max(0, *r.MaxCompletionTokens)
	}
	if r.MaxTokens != nil {
		return max(0, *r.MaxTokens)
	}
	return 0
}

// ChatOutputReserve accounts for the maximum output of every requested choice.
func ChatOutputReserve(r ChatCompletionRequest) int {
	output := ChatOutputLimit(r)
	if output == 0 {
		output = DefaultOutputTokenReserve
	}
	choices := 1
	if r.N != nil && *r.N > 1 {
		choices = *r.N
	}
	if output > intMax()/choices {
		return intMax()
	}
	return output * choices
}

func ChatReserveTokens(r ChatCompletionRequest) int {
	return ReserveTokens(ChatInputTokens(r), ChatOutputReserve(r))
}

func ResponseOutputLimit(r ResponseRequest) int {
	if r.MaxOutputTokens != nil {
		return max(0, *r.MaxOutputTokens)
	}
	if r.MaxTokens != nil {
		return max(0, *r.MaxTokens)
	}
	return 0
}

func ResponseReserveTokens(r ResponseRequest) int {
	return ReserveTokens(ResponseInputTokens(r), ResponseOutputLimit(r))
}

// EstimateContextTokens estimates the serialized input, including roles, tool
// schemas and tool arguments. It is not a provider tokenizer. Encoded image
// bytes are replaced with a bounded image allowance rather than counted as text.
func EstimateContextTokens(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 1
	}
	var input any
	if json.Unmarshal(encoded, &input) != nil {
		return 1
	}
	images := 0
	var scrub func(any) any
	scrub = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			if x["type"] == "image_url" || x["type"] == "input_image" {
				images++
				return "image"
			}
			for k, child := range x {
				x[k] = scrub(child)
			}
		case []any:
			for i, child := range x {
				x[i] = scrub(child)
			}
		}
		return v
	}
	encoded, _ = json.Marshal(scrub(input))
	return max(1, (len(encoded)+3)/4+images*4096)
}

func ChatInputTokens(r ChatCompletionRequest) int {
	estimated := EstimateContextTokens(struct {
		Messages     []Message             `json:"messages"`
		Functions    []FunctionDefinition  `json:"functions,omitempty"`
		FunctionCall *LegacyFunctionChoice `json:"function_call,omitempty"`
		Tools        []Tool                `json:"tools,omitempty"`
		ToolChoice   any                   `json:"tool_choice,omitempty"`
		Format       *ResponseFormat       `json:"response_format,omitempty"`
		WebSearch    *ChatWebSearchOptions `json:"web_search_options,omitempty"`
		WebFetch     *ChatWebFetchOptions  `json:"web_fetch_options,omitempty"`
	}{r.Messages, r.Functions, r.FunctionCall, r.Tools, r.ToolChoice, r.ResponseFormat, r.WebSearchOptions, r.WebFetchOptions})
	if r.NativeInputTokens > intMax()-estimated {
		return intMax()
	}
	return estimated + max(0, r.NativeInputTokens)
}

func ResponseInputTokens(r ResponseRequest) int {
	estimated := EstimateContextTokens(struct {
		Input        any            `json:"input"`
		Instructions string         `json:"instructions,omitempty"`
		Tools        []ResponseTool `json:"tools,omitempty"`
		ToolChoice   any            `json:"tool_choice,omitempty"`
		Text         any            `json:"text,omitempty"`
	}{r.Input, r.Instructions, r.Tools, r.ToolChoice, r.Text})
	if r.NativeInputTokens > intMax()-estimated {
		return intMax()
	}
	return estimated + max(0, r.NativeInputTokens)
}

func ResponseCompactInputTokens(r ResponseCompactRequest) int {
	return EstimateContextTokens(struct {
		Input        any    `json:"input"`
		Instructions string `json:"instructions,omitempty"`
	}{r.Input, r.Instructions})
}

func CompletionInputTokens(r CompletionRequest) int {
	info, err := InspectCompletionPrompt(r.Prompt)
	if err != nil {
		return 1
	}
	if info.Kind == CompletionPromptTokens || info.Kind == CompletionPromptTokenArrays {
		return max(1, info.TokenCount)
	}
	total := 0
	for _, prompt := range info.Texts {
		count := EstimateContextTokens(prompt)
		if total > intMax()-count {
			return intMax()
		}
		total += count
	}
	return max(1, total)
}

func CompletionReserveTokens(r CompletionRequest) int {
	output := 16
	if r.MaxTokens != nil {
		output = max(0, *r.MaxTokens)
	}
	prompts := CompletionPromptCount(r.Prompt)
	candidatesPerPrompt := 1
	if r.N != nil {
		candidatesPerPrompt = max(candidatesPerPrompt, *r.N)
	}
	if r.BestOf != nil {
		candidatesPerPrompt = max(candidatesPerPrompt, *r.BestOf)
	}
	if candidatesPerPrompt > intMax()/prompts {
		return intMax()
	}
	candidates := prompts * candidatesPerPrompt
	limit := intMax()
	if candidates > 0 && output > limit/candidates {
		return limit
	}
	generated := output * candidates
	input := CompletionInputTokens(r)
	if input > limit-generated {
		return limit
	}
	return input + generated
}

// ReserveTokens saturates instead of overflowing for an untrusted output cap.
func ReserveTokens(input, output int) int {
	if output <= 0 {
		output = DefaultOutputTokenReserve
	}
	limit := int(^uint(0) >> 1)
	if input > limit-output {
		return limit
	}
	return input + output
}
