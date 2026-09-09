package openai

type ChatCompletionRequest struct {
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
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
	SearchRequests      int                 `json:"-"`
	PromptTokens        int                 `json:"prompt_tokens"`
	CompletionTokens    int                 `json:"completion_tokens"`
	TotalTokens         int                 `json:"total_tokens"`
	PromptTokensDetails *PromptTokenDetails `json:"prompt_tokens_details,omitempty"`
}

type PromptTokenDetails struct {
	CachedTokens        int `json:"cached_tokens,omitempty"`
	CacheWriteTokens    int `json:"cache_write_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
}

type ResponseRequest struct {
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model"`
	Input           any    `json:"input"`
	Instructions    string `json:"instructions,omitempty"`
	Stream          bool   `json:"stream,omitempty"`
	MaxOutputTokens *int   `json:"max_output_tokens,omitempty"`
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
	ID      string                  `json:"id,omitempty"`
	Type    string                  `json:"type"`
	Status  string                  `json:"status,omitempty"`
	Role    string                  `json:"role,omitempty"`
	Content []ResponseOutputContent `json:"content,omitempty"`
	Summary []ResponseOutputContent `json:"summary,omitempty"`
}

type ResponseOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type ResponseUsage struct {
	InputTokens        int                `json:"input_tokens,omitempty"`
	OutputTokens       int                `json:"output_tokens,omitempty"`
	TotalTokens        int                `json:"total_tokens,omitempty"`
	InputTokensDetails *InputTokenDetails `json:"input_tokens_details,omitempty"`
}

type InputTokenDetails struct {
	CachedTokens        int `json:"cached_tokens,omitempty"`
	CacheWriteTokens    int `json:"cache_write_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
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
