package openai

type ChatCompletionRequest struct {
	Provider          string          `json:"provider,omitempty"`
	Model             string          `json:"model"`
	Messages          []Message       `json:"messages"`
	Tools             []Tool          `json:"tools,omitempty"`
	ToolChoice        any             `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    *ResponseFormat `json:"response_format,omitempty"`
	Stream            bool            `json:"stream,omitempty"`
	MaxTokens         *int            `json:"max_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Stop              any             `json:"stop,omitempty"`
	Seed              *int64          `json:"seed,omitempty"`
}

type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type Tool struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type FunctionDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
	Strict      *bool  `json:"strict,omitempty"`
}

type ToolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ResponseFormat struct {
	Type       string            `json:"type"`
	JSONSchema *JSONSchemaFormat `json:"json_schema,omitempty"`
}

type JSONSchemaFormat struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Schema      any    `json:"schema"`
	Strict      *bool  `json:"strict,omitempty"`
}

type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ResponseRequest struct {
	Provider          string         `json:"provider,omitempty"`
	Model             string         `json:"model"`
	Input             any            `json:"input"`
	Instructions      string         `json:"instructions,omitempty"`
	Tools             []ResponseTool `json:"tools,omitempty"`
	ToolChoice        any            `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool          `json:"parallel_tool_calls,omitempty"`
	Text              any            `json:"text,omitempty"`
	PreviousResponse  string         `json:"previous_response_id,omitempty"`
	Stream            bool           `json:"stream,omitempty"`
	MaxOutputTokens   *int           `json:"max_output_tokens,omitempty"`
	MaxTokens         *int           `json:"max_tokens,omitempty"`
	Temperature       *float64       `json:"temperature,omitempty"`
	TopP              *float64       `json:"top_p,omitempty"`
}

type ResponseTool struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
	Strict      *bool  `json:"strict,omitempty"`
}

type ResponseResponse struct {
	ID         string               `json:"id"`
	Object     string               `json:"object"`
	CreatedAt  int64                `json:"created_at,omitempty"`
	Status     string               `json:"status,omitempty"`
	Model      string               `json:"model"`
	Output     []ResponseOutputItem `json:"output,omitempty"`
	OutputText string               `json:"output_text,omitempty"`
	Usage      ResponseUsage        `json:"usage,omitempty"`
}

type ResponseOutputItem struct {
	ID        string                  `json:"id,omitempty"`
	Type      string                  `json:"type"`
	Status    string                  `json:"status,omitempty"`
	Role      string                  `json:"role,omitempty"`
	Name      string                  `json:"name,omitempty"`
	CallID    string                  `json:"call_id,omitempty"`
	Arguments string                  `json:"arguments,omitempty"`
	Content   []ResponseOutputContent `json:"content,omitempty"`
	Summary   []ResponseOutputContent `json:"summary,omitempty"`
}

type ResponseOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type ResponseUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

type ModelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func ContentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		text := ""
		for _, item := range typed {
			part := ContentText(item)
			if part == "" {
				continue
			}
			if text != "" {
				text += " "
			}
			text += part
		}
		return text
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return text
		}
		for _, key := range []string{"arguments", "output", "content"} {
			if part := ContentText(typed[key]); part != "" {
				return part
			}
		}
		if _, ok := typed["type"]; ok {
			return ""
		}
		text := ""
		for _, item := range typed {
			part := ContentText(item)
			if part == "" {
				continue
			}
			if text != "" {
				text += " "
			}
			text += part
		}
		return text
	default:
		return ""
	}
}
