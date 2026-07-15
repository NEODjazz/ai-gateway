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

type ResponseRequest struct {
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model"`
	Input           any    `json:"input"`
	Instructions    string `json:"instructions,omitempty"`
	Stream          bool   `json:"stream,omitempty"`
	MaxOutputTokens *int   `json:"max_output_tokens,omitempty"`
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
