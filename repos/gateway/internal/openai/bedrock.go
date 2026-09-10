package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

type BedrockConverseRequest struct {
	Messages        []BedrockMessage       `json:"messages"`
	System          []BedrockContentBlock  `json:"system,omitempty"`
	InferenceConfig BedrockInferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig      *BedrockToolConfig     `json:"toolConfig,omitempty"`
	ServiceTier     *BedrockServiceTier    `json:"serviceTier,omitempty"`
}

type BedrockServiceTier struct {
	Type string `json:"type"`
}

type BedrockMessage struct {
	Role    string                `json:"role"`
	Content []BedrockContentBlock `json:"content"`
}

type BedrockContentBlock struct {
	Text       *string            `json:"text,omitempty"`
	Image      *BedrockImage      `json:"image,omitempty"`
	Document   *BedrockDocument   `json:"document,omitempty"`
	ToolUse    *BedrockToolUse    `json:"toolUse,omitempty"`
	ToolResult *BedrockToolResult `json:"toolResult,omitempty"`
}

type BedrockDocument struct {
	Format string                `json:"format,omitempty"`
	Name   string                `json:"name"`
	Source BedrockDocumentSource `json:"source"`
}

type BedrockDocumentSource struct {
	Bytes string `json:"bytes"`
}

const (
	maxBedrockDocuments    = 5
	maxBedrockDocumentSize = 4718592
)

type BedrockImage struct {
	Format string             `json:"format"`
	Source BedrockImageSource `json:"source"`
}

type BedrockImageSource struct {
	Bytes string `json:"bytes"`
}

type BedrockToolUse struct {
	ID    string `json:"toolUseId"`
	Name  string `json:"name"`
	Input any    `json:"input"`
}

type BedrockToolResult struct {
	ID      string                `json:"toolUseId"`
	Content []BedrockContentBlock `json:"content"`
}

type BedrockInferenceConfig struct {
	MaxTokens     *int     `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

type BedrockToolConfig struct {
	Tools []BedrockTool `json:"tools"`
}

type BedrockTool struct {
	Spec BedrockToolSpec `json:"toolSpec"`
}

type BedrockToolSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema BedrockToolInputSchema `json:"inputSchema"`
}

type BedrockToolInputSchema struct {
	JSON any `json:"json"`
}

func (r BedrockConverseRequest) ChatRequest(model, provider string) (ChatCompletionRequest, error) {
	request := ChatCompletionRequest{
		Provider: provider, Model: model, MaxCompletionTokens: r.InferenceConfig.MaxTokens,
		Temperature: r.InferenceConfig.Temperature, TopP: r.InferenceConfig.TopP,
	}
	if len(r.InferenceConfig.StopSequences) > 0 {
		request.Stop = r.InferenceConfig.StopSequences
	}
	if strings.TrimSpace(model) == "" || len(r.Messages) == 0 {
		return request, errors.New("model and messages are required")
	}
	if r.ServiceTier != nil {
		switch r.ServiceTier.Type {
		case "default", "flex", "priority":
			request.ServiceTier = r.ServiceTier.Type
		default:
			return request, errors.New("serviceTier.type must be default, flex, or priority")
		}
	}
	for _, block := range r.System {
		if block.Text == nil || strings.TrimSpace(*block.Text) == "" || block.Image != nil || block.Document != nil || block.ToolUse != nil || block.ToolResult != nil {
			return request, errors.New("system supports non-empty text blocks only")
		}
		request.Messages = append(request.Messages, Message{Role: "system", Content: *block.Text})
	}
	seenToolUses := make(map[string]bool)
	for _, message := range r.Messages {
		if (message.Role != "user" && message.Role != "assistant") || len(message.Content) == 0 {
			return request, errors.New("messages require user or assistant role and content")
		}
		textBlocks, imageBlocks, documentBlocks, toolUseBlocks, toolResultBlocks := 0, 0, 0, 0, 0
		var texts []string
		chat := Message{Role: message.Role}
		var content []any
		for _, block := range message.Content {
			fields := 0
			if block.Text != nil {
				fields++
				textBlocks++
				if *block.Text == "" {
					return request, errors.New("text blocks must be non-empty")
				}
				texts = append(texts, *block.Text)
				content = append(content, map[string]any{"type": "text", "text": *block.Text})
			}
			if block.Image != nil {
				fields++
				imageBlocks++
				if message.Role != "user" {
					return request, errors.New("images are accepted in user messages only")
				}
				mediaType := bedrockImageMediaType(block.Image.Format)
				dataURL := "data:" + mediaType + ";base64," + block.Image.Source.Bytes
				if mediaType == "" {
					return request, errors.New("unsupported image format")
				}
				if _, err := ParseDataImageURL(dataURL); err != nil {
					return request, err
				}
				content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL}})
			}
			if block.Document != nil {
				fields++
				documentBlocks++
				if message.Role != "user" {
					return request, errors.New("documents are accepted in user messages only")
				}
				decoded, err := validateBedrockDocument(*block.Document)
				if err != nil {
					return request, err
				}
				request.NativeInputTokens = ReserveTokens(request.NativeInputTokens, len(decoded))
				raw, err := json.Marshal(BedrockContentBlock{Document: block.Document})
				if err != nil {
					return request, errors.New("document block is not valid JSON")
				}
				content = append(content, map[string]any{"type": "bedrock_document", "index": len(chat.NativeContent)})
				chat.NativeContent = append(chat.NativeContent, raw)
			}
			if block.ToolUse != nil {
				fields++
				toolUseBlocks++
				tool := block.ToolUse
				if message.Role != "assistant" || tool.ID == "" || tool.Name == "" || tool.Input == nil || seenToolUses[tool.ID] {
					return request, errors.New("invalid assistant toolUse block")
				}
				arguments, err := json.Marshal(tool.Input)
				if err != nil {
					return request, errors.New("toolUse input is not valid JSON")
				}
				seenToolUses[tool.ID] = true
				chat.ToolCalls = append(chat.ToolCalls, ToolCall{ID: tool.ID, Type: "function", Function: FunctionCall{Name: tool.Name, Arguments: string(arguments)}})
			}
			if block.ToolResult != nil {
				fields++
				toolResultBlocks++
			}
			if fields != 1 {
				return request, errors.New("content blocks must contain exactly one supported field")
			}
		}
		if toolResultBlocks > 0 {
			if message.Role != "user" || toolResultBlocks != 1 || textBlocks != 0 || imageBlocks != 0 || documentBlocks != 0 || toolUseBlocks != 0 || len(message.Content) != 1 {
				return request, errors.New("toolResult must be the only block in a user message")
			}
			result := message.Content[0].ToolResult
			if result.ID == "" || !seenToolUses[result.ID] || len(result.Content) != 1 || result.Content[0].Text == nil || *result.Content[0].Text == "" || result.Content[0].Image != nil || result.Content[0].Document != nil || result.Content[0].ToolUse != nil || result.Content[0].ToolResult != nil {
				return request, errors.New("invalid toolResult block")
			}
			request.Messages = append(request.Messages, Message{Role: "tool", ToolCallID: result.ID, Content: *result.Content[0].Text})
			continue
		}
		if documentBlocks > 0 && (documentBlocks > maxBedrockDocuments || textBlocks == 0) {
			return request, errors.New("documents require accompanying text and at most five documents per message")
		}
		if imageBlocks > 0 || documentBlocks > 0 {
			chat.Content = content
		} else if len(texts) > 0 {
			chat.Content = strings.Join(texts, "")
		}
		if (message.Role == "user" && len(chat.ToolCalls) > 0) || (chat.Content == nil && len(chat.ToolCalls) == 0) {
			return request, errors.New("invalid message content")
		}
		request.Messages = append(request.Messages, chat)
	}
	if _, err := ChatImageAttachments(request.Messages); err != nil {
		return request, err
	}
	if r.ToolConfig != nil {
		if len(r.ToolConfig.Tools) == 0 {
			return request, errors.New("toolConfig.tools must not be empty")
		}
		seen := make(map[string]bool)
		for _, tool := range r.ToolConfig.Tools {
			spec := tool.Spec
			if spec.Name == "" || spec.InputSchema.JSON == nil || seen[spec.Name] {
				return request, errors.New("invalid tool specification")
			}
			seen[spec.Name] = true
			request.Tools = append(request.Tools, Tool{Type: "function", Function: FunctionDefinition{Name: spec.Name, Description: spec.Description, Parameters: spec.InputSchema.JSON}})
		}
	}
	return request, nil
}

func validateBedrockDocument(document BedrockDocument) ([]byte, error) {
	if !validBedrockDocumentName(document.Name) {
		return nil, errors.New("document name must contain 1 to 200 letters, digits, single spaces, hyphens, parentheses, or brackets")
	}
	mediaTypes := map[string]string{"pdf": "application/pdf", "csv": "text/csv", "doc": "application/msword", "docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "xls": "application/vnd.ms-excel", "xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "html": "text/html", "txt": "text/plain", "md": "text/markdown"}
	if mediaTypes[document.Format] == "" || document.Source.Bytes == "" || base64.StdEncoding.DecodedLen(len(document.Source.Bytes)) > maxBedrockDocumentSize {
		return nil, errors.New("document format or size is invalid")
	}
	decoded, err := base64.StdEncoding.DecodeString(document.Source.Bytes)
	if err != nil || len(decoded) == 0 || len(decoded) > maxBedrockDocumentSize || !validBedrockDocumentSignature(document.Format, decoded) {
		return nil, errors.New("document bytes are invalid for the declared format")
	}
	return decoded, nil
}

func validBedrockDocumentName(name string) bool {
	if len(name) == 0 || len(name) > 200 || strings.TrimSpace(name) != name || strings.Contains(name, "  ") {
		return false
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune(" -()[]", character) {
			continue
		}
		return false
	}
	return true
}

func validBedrockDocumentSignature(format string, data []byte) bool {
	switch format {
	case "pdf":
		return len(data) >= 5 && string(data[:5]) == "%PDF-"
	case "doc", "xls":
		return len(data) >= 8 && string(data[:8]) == "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"
	case "docx", "xlsx":
		return len(data) >= 4 && string(data[:4]) == "PK\x03\x04"
	default:
		return true
	}
}

func BedrockDocumentAttachments(messages []Message) ([]ImageAttachment, error) {
	var attachments []ImageAttachment
	mediaTypes := map[string]string{"pdf": "application/pdf", "csv": "text/csv", "doc": "application/msword", "docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "xls": "application/vnd.ms-excel", "xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "html": "text/html", "txt": "text/plain", "md": "text/markdown"}
	for _, message := range messages {
		messageDocuments := 0
		for _, raw := range message.NativeContent {
			var block BedrockContentBlock
			if json.Unmarshal(raw, &block) != nil || block.Document == nil {
				continue
			}
			if _, err := validateBedrockDocument(*block.Document); err != nil {
				return nil, err
			}
			messageDocuments++
			attachments = append(attachments, ImageAttachment{MediaType: mediaTypes[block.Document.Format], Data: block.Document.Source.Bytes})
		}
		if messageDocuments > 0 && (message.Role != "user" || messageDocuments > maxBedrockDocuments || strings.TrimSpace(ContentText(message.Content)) == "") {
			return nil, errors.New("documents require a user message with accompanying text and at most five documents")
		}
	}
	return attachments, nil
}

func BedrockDocumentText(messages []Message) string {
	var texts []string
	for _, message := range messages {
		for _, raw := range message.NativeContent {
			var block BedrockContentBlock
			if json.Unmarshal(raw, &block) != nil || block.Document == nil || !strings.Contains(" csv html txt md ", " "+block.Document.Format+" ") {
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(block.Document.Source.Bytes)
			if err == nil {
				texts = append(texts, string(decoded))
			}
		}
	}
	return strings.Join(texts, "\n")
}

func bedrockImageMediaType(format string) string {
	switch format {
	case "jpeg", "png", "gif", "webp":
		return "image/" + format
	default:
		return ""
	}
}

type BedrockConverseResponse struct {
	Output     BedrockConverseOutput `json:"output"`
	StopReason string                `json:"stopReason"`
	Usage      BedrockUsage          `json:"usage"`
}

type BedrockConverseOutput struct {
	Message BedrockMessage `json:"message"`
}

type BedrockUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

func BedrockFromChat(response ChatCompletionResponse) (BedrockConverseResponse, error) {
	result := BedrockConverseResponse{Usage: BedrockUsage{InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens, TotalTokens: response.Usage.TotalTokens}}
	result.Output.Message.Role = "assistant"
	if response.Usage.PromptTokens < 0 || response.Usage.CompletionTokens < 0 || response.Usage.TotalTokens != response.Usage.PromptTokens+response.Usage.CompletionTokens {
		return result, errors.New("invalid chat usage for Converse response")
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Role != "assistant" {
		return result, errors.New("chat response cannot be represented as Converse")
	}
	choice := response.Choices[0]
	if text := ContentText(choice.Message.Content); text != "" {
		value := text
		result.Output.Message.Content = append(result.Output.Message.Content, BedrockContentBlock{Text: &value})
	}
	for _, call := range choice.Message.ToolCalls {
		var input any
		if json.Unmarshal([]byte(call.Function.Arguments), &input) != nil {
			input = call.Function.Arguments
		}
		result.Output.Message.Content = append(result.Output.Message.Content, BedrockContentBlock{ToolUse: &BedrockToolUse{ID: call.ID, Name: call.Function.Name, Input: input}})
	}
	stopReasons := map[string]string{"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use", "content_filter": "content_filtered"}
	stopReason, found := stopReasons[choice.FinishReason]
	if !found || len(result.Output.Message.Content) == 0 {
		return result, errors.New("chat response cannot be represented as Converse")
	}
	result.StopReason = stopReason
	return result, nil
}
