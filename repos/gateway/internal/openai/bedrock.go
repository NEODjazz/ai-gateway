package openai

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

type BedrockConverseRequest struct {
	Messages                          []BedrockMessage          `json:"messages"`
	System                            []BedrockContentBlock     `json:"system,omitempty"`
	InferenceConfig                   BedrockInferenceConfig    `json:"inferenceConfig,omitempty"`
	ToolConfig                        *BedrockToolConfig        `json:"toolConfig,omitempty"`
	ServiceTier                       *BedrockServiceTier       `json:"serviceTier,omitempty"`
	PerformanceConfig                 *BedrockPerformanceConfig `json:"performanceConfig,omitempty"`
	OutputConfig                      *BedrockOutputConfig      `json:"outputConfig,omitempty"`
	GuardrailConfig                   *BedrockGuardrailConfig   `json:"guardrailConfig,omitempty"`
	AdditionalModelRequestFields      json.RawMessage           `json:"additionalModelRequestFields,omitempty"`
	AdditionalModelResponseFieldPaths []string                  `json:"additionalModelResponseFieldPaths,omitempty"`
	RequestMetadata                   map[string]string         `json:"requestMetadata,omitempty"`
}

type BedrockOutputConfig struct {
	TextFormat BedrockOutputFormat `json:"textFormat"`
}

type BedrockGuardrailConfig struct {
	GuardrailIdentifier string `json:"guardrailIdentifier"`
	GuardrailVersion    string `json:"guardrailVersion"`
	Trace               string `json:"trace,omitempty"`
}

type BedrockOutputFormat struct {
	Type      string                       `json:"type"`
	Structure BedrockOutputFormatStructure `json:"structure"`
}

type BedrockOutputFormatStructure struct {
	JSONSchema *BedrockJSONSchemaDefinition `json:"jsonSchema,omitempty"`
}

type BedrockJSONSchemaDefinition struct {
	Schema      string `json:"schema"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type BedrockServiceTier struct {
	Type string `json:"type"`
}

type BedrockPerformanceConfig struct {
	Latency string `json:"latency"`
}

type BedrockMessage struct {
	Role    string                `json:"role"`
	Content []BedrockContentBlock `json:"content"`
}

type BedrockContentBlock struct {
	Text             *string                  `json:"text,omitempty"`
	Image            *BedrockImage            `json:"image,omitempty"`
	Document         *BedrockDocument         `json:"document,omitempty"`
	ToolUse          *BedrockToolUse          `json:"toolUse,omitempty"`
	ToolResult       *BedrockToolResult       `json:"toolResult,omitempty"`
	CitationsContent *BedrockCitationsContent `json:"citationsContent,omitempty"`
	ReasoningContent *BedrockReasoningContent `json:"reasoningContent,omitempty"`
}

type BedrockReasoningContent struct {
	ReasoningText   *BedrockReasoningText `json:"reasoningText,omitempty"`
	RedactedContent string                `json:"redactedContent,omitempty"`
}

type BedrockReasoningText struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

type BedrockCitationsContent struct {
	Content   []BedrockCitationText `json:"content"`
	Citations []BedrockCitation     `json:"citations"`
}

type BedrockCitationText struct {
	Text *string `json:"text,omitempty"`
}

type BedrockCitation struct {
	Title         string                         `json:"title,omitempty"`
	Source        string                         `json:"source,omitempty"`
	SourceContent []BedrockCitationSourceContent `json:"sourceContent,omitempty"`
	Location      BedrockCitationLocation        `json:"location"`
}

type BedrockCitationSourceContent struct {
	Text *string `json:"text,omitempty"`
}

type BedrockCitationLocation struct {
	DocumentChar         *BedrockDocumentLocation     `json:"documentChar,omitempty"`
	DocumentChunk        *BedrockDocumentLocation     `json:"documentChunk,omitempty"`
	DocumentPage         *BedrockDocumentLocation     `json:"documentPage,omitempty"`
	SearchResultLocation *BedrockSearchResultLocation `json:"searchResultLocation,omitempty"`
	Web                  *BedrockWebLocation          `json:"web,omitempty"`
}

type BedrockDocumentLocation struct {
	DocumentIndex int `json:"documentIndex"`
	Start         int `json:"start"`
	End           int `json:"end"`
}

type BedrockSearchResultLocation struct {
	SearchResultIndex int `json:"searchResultIndex"`
	Start             int `json:"start"`
	End               int `json:"end"`
}

type BedrockWebLocation struct {
	Domain string `json:"domain,omitempty"`
	URL    string `json:"url"`
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
	Tools      []BedrockTool      `json:"tools"`
	ToolChoice *BedrockToolChoice `json:"toolChoice,omitempty"`
}

type BedrockToolChoice struct {
	Auto *struct{}                  `json:"auto,omitempty"`
	Any  *struct{}                  `json:"any,omitempty"`
	Tool *BedrockSpecificToolChoice `json:"tool,omitempty"`
}

type BedrockSpecificToolChoice struct {
	Name string `json:"name"`
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
	if err := ValidateBedrockResponseFieldPaths(r.AdditionalModelResponseFieldPaths); err != nil {
		return request, err
	}
	if err := ValidateBedrockAdditionalModelRequestFields(r.AdditionalModelRequestFields); err != nil {
		return request, err
	}
	if err := ValidateBedrockGuardrailConfig(r.GuardrailConfig); err != nil {
		return request, err
	}
	if err := ValidateBedrockRequestMetadata(r.RequestMetadata); err != nil {
		return request, err
	}
	request.BedrockAdditionalModelResponseFieldPaths = append([]string(nil), r.AdditionalModelResponseFieldPaths...)
	request.BedrockAdditionalModelRequestFields = append(json.RawMessage(nil), r.AdditionalModelRequestFields...)
	if r.GuardrailConfig != nil {
		config := *r.GuardrailConfig
		request.BedrockGuardrailConfig = &config
	}
	if len(r.AdditionalModelRequestFields) > 0 {
		request.NativeInputTokens = ReserveTokens(request.NativeInputTokens, EstimateContextTokens(r.AdditionalModelRequestFields))
	}
	if len(r.RequestMetadata) > 0 {
		request.BedrockRequestMetadata = make(map[string]string, len(r.RequestMetadata))
		for key, value := range r.RequestMetadata {
			request.BedrockRequestMetadata[key] = value
		}
	}
	if r.OutputConfig != nil {
		format := r.OutputConfig.TextFormat
		definition := format.Structure.JSONSchema
		if format.Type != "json_schema" || definition == nil || definition.Schema == "" || len(definition.Schema) > 1<<20 || utf8.RuneCountInString(definition.Name) > 256 || utf8.RuneCountInString(definition.Description) > 8192 {
			return request, errors.New("outputConfig.textFormat requires a bounded json_schema structure")
		}
		var schema any
		if json.Unmarshal([]byte(definition.Schema), &schema) != nil {
			return request, errors.New("outputConfig.textFormat.schema must contain valid JSON")
		}
		if _, ok := schema.(map[string]any); !ok {
			return request, errors.New("outputConfig.textFormat.schema must contain a JSON object")
		}
		request.ResponseFormat = &ResponseFormat{Type: "json_schema", JSONSchema: &JSONSchemaFormat{Name: definition.Name, Description: definition.Description, Schema: schema}}
	}
	if r.ServiceTier != nil {
		switch r.ServiceTier.Type {
		case "default", "flex", "priority":
			request.ServiceTier = r.ServiceTier.Type
		case "reserved":
			request.BedrockServiceTier = r.ServiceTier.Type
		default:
			return request, errors.New("serviceTier.type must be default, flex, priority, or reserved")
		}
	}
	if r.PerformanceConfig != nil {
		switch r.PerformanceConfig.Latency {
		case "standard", "optimized":
			request.BedrockPerformanceLatency = r.PerformanceConfig.Latency
		default:
			return request, errors.New("performanceConfig.latency must be standard or optimized")
		}
	}
	for _, block := range r.System {
		if block.Text == nil || strings.TrimSpace(*block.Text) == "" || block.Image != nil || block.Document != nil || block.ToolUse != nil || block.ToolResult != nil || block.CitationsContent != nil || block.ReasoningContent != nil {
			return request, errors.New("system supports non-empty text blocks only")
		}
		request.Messages = append(request.Messages, Message{Role: "system", Content: *block.Text})
	}
	seenToolUses := make(map[string]bool)
	for _, message := range r.Messages {
		if (message.Role != "user" && message.Role != "assistant") || len(message.Content) == 0 || len(message.Content) > 128 {
			return request, errors.New("messages require user or assistant role and content")
		}
		textBlocks, imageBlocks, documentBlocks, toolUseBlocks, toolResultBlocks, reasoningBlocks := 0, 0, 0, 0, 0, 0
		var texts []string
		chat := Message{Role: message.Role}
		var content []any
		for blockIndex, block := range message.Content {
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
			if block.ReasoningContent != nil {
				fields++
				reasoningBlocks++
				if message.Role != "assistant" {
					return request, errors.New("reasoningContent is accepted in assistant messages only")
				}
				reasoning := block.ReasoningContent
				members := 0
				index := blockIndex
				converted := ReasoningBlock{Index: &index}
				if reasoning.ReasoningText != nil {
					members++
					converted.Type = "thinking"
					converted.Thinking = reasoning.ReasoningText.Text
					converted.Signature = reasoning.ReasoningText.Signature
				}
				if reasoning.RedactedContent != "" {
					members++
					if _, err := base64.StdEncoding.DecodeString(reasoning.RedactedContent); err != nil {
						return request, errors.New("reasoningContent.redactedContent must be valid base64")
					}
					converted.Type = "redacted_thinking"
					converted.Data = reasoning.RedactedContent
				}
				if members != 1 {
					return request, errors.New("reasoningContent must contain exactly one union member")
				}
				chat.Reasoning = append(chat.Reasoning, converted)
			}
			if fields != 1 {
				return request, errors.New("content blocks must contain exactly one supported field")
			}
		}
		if toolResultBlocks > 0 {
			if message.Role != "user" || toolResultBlocks != 1 || textBlocks != 0 || imageBlocks != 0 || documentBlocks != 0 || toolUseBlocks != 0 || reasoningBlocks != 0 || len(message.Content) != 1 {
				return request, errors.New("toolResult must be the only block in a user message")
			}
			result := message.Content[0].ToolResult
			if result.ID == "" || !seenToolUses[result.ID] || len(result.Content) != 1 || result.Content[0].Text == nil || *result.Content[0].Text == "" || result.Content[0].Image != nil || result.Content[0].Document != nil || result.Content[0].ToolUse != nil || result.Content[0].ToolResult != nil || result.Content[0].CitationsContent != nil || result.Content[0].ReasoningContent != nil {
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
		if err := ValidateBedrockReasoningBlocks(chat.Reasoning); err != nil {
			return request, err
		}
		if (message.Role == "user" && len(chat.ToolCalls) > 0) || (chat.Content == nil && len(chat.ToolCalls) == 0 && len(chat.Reasoning) == 0) {
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
			if !ValidBedrockToolName(spec.Name) || spec.InputSchema.JSON == nil || seen[spec.Name] {
				return request, errors.New("invalid tool specification")
			}
			seen[spec.Name] = true
			request.Tools = append(request.Tools, Tool{Type: "function", Function: FunctionDefinition{Name: spec.Name, Description: spec.Description, Parameters: spec.InputSchema.JSON}})
		}
		if choice := r.ToolConfig.ToolChoice; choice != nil {
			members := 0
			if choice.Auto != nil {
				members++
				request.ToolChoice = "auto"
			}
			if choice.Any != nil {
				members++
				request.ToolChoice = "required"
			}
			if choice.Tool != nil {
				members++
				if !seen[choice.Tool.Name] {
					return request, errors.New("toolChoice.tool must reference a configured tool")
				}
				request.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": choice.Tool.Name}}
			}
			if members != 1 {
				return request, errors.New("toolChoice must contain exactly one union member")
			}
		}
	}
	return request, nil
}

const MaxBedrockAdditionalModelRequestFieldsBytes = 64 << 10

var (
	bedrockGuardrailIDPattern      = regexp.MustCompile(`^(?:[a-z0-9]+|arn:aws(?:-[^:]+)?:bedrock:[a-z0-9-]{1,20}:[0-9]{12}:guardrail/[a-z0-9]+)$`)
	bedrockGuardrailVersionPattern = regexp.MustCompile(`^(?:[1-9][0-9]{0,7}|DRAFT)$`)
)

func ValidateBedrockGuardrailConfig(config *BedrockGuardrailConfig) error {
	if config == nil {
		return nil
	}
	if len(config.GuardrailIdentifier) > 2048 || !bedrockGuardrailIDPattern.MatchString(config.GuardrailIdentifier) {
		return errors.New("guardrailConfig contains an invalid guardrailIdentifier")
	}
	if !bedrockGuardrailVersionPattern.MatchString(config.GuardrailVersion) {
		return errors.New("guardrailConfig contains an invalid guardrailVersion")
	}
	if config.Trace != "" && config.Trace != "enabled" && config.Trace != "disabled" && config.Trace != "enabled_full" {
		return errors.New("guardrailConfig contains an invalid trace value")
	}
	return nil
}

func ValidateBedrockAdditionalModelRequestFields(fields json.RawMessage) error {
	if len(fields) == 0 {
		return nil
	}
	if len(fields) > MaxBedrockAdditionalModelRequestFieldsBytes {
		return errors.New("additionalModelRequestFields exceeds its size limit")
	}
	if !json.Valid(fields) || bytes.Equal(bytes.TrimSpace(fields), []byte("null")) {
		return errors.New("additionalModelRequestFields must contain a non-null JSON value")
	}
	return nil
}

func ValidateBedrockRequestMetadata(metadata map[string]string) error {
	if len(metadata) > 16 {
		return errors.New("requestMetadata must contain at most 16 entries")
	}
	for key, value := range metadata {
		if !validBedrockRequestMetadataText(key, false) {
			return errors.New("requestMetadata contains an invalid key")
		}
		if !validBedrockRequestMetadataText(value, true) {
			return errors.New("requestMetadata contains an invalid value")
		}
	}
	return nil
}

func validBedrockRequestMetadataText(value string, emptyAllowed bool) bool {
	if (!emptyAllowed && value == "") || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune(" \t\n\r\f:_@$#=/+,-.", char) {
			continue
		}
		return false
	}
	return true
}

func ValidBedrockToolName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func ValidateBedrockResponseFieldPaths(paths []string) error {
	if len(paths) > 10 {
		return errors.New("additionalModelResponseFieldPaths supports at most ten paths")
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if len(path) == 0 || !utf8.ValidString(path) || utf8.RuneCountInString(path) > 256 || path[0] != '/' || seen[path] {
			return errors.New("additionalModelResponseFieldPaths contains an invalid JSON Pointer")
		}
		seen[path] = true
		for index := 0; index < len(path); index++ {
			if path[index] == '~' && (index+1 >= len(path) || path[index+1] != '0' && path[index+1] != '1') {
				return errors.New("additionalModelResponseFieldPaths contains an invalid JSON Pointer")
			}
			if path[index] == '~' {
				index++
			}
		}
	}
	return nil
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
	Output                        BedrockConverseOutput `json:"output"`
	StopReason                    string                `json:"stopReason"`
	Usage                         BedrockUsage          `json:"usage"`
	AdditionalModelResponseFields json.RawMessage       `json:"additionalModelResponseFields,omitempty"`
	Trace                         json.RawMessage       `json:"trace,omitempty"`
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
	result.AdditionalModelResponseFields = bedrockAdditionalResponseFields(choice.Message.NativeContent)
	result.Trace = bedrockTrace(choice.Message.NativeContent)
	text := ContentText(choice.Message.Content)
	if err := ValidateChatAnnotations(choice.Message.Annotations); err != nil {
		return result, err
	}
	if text != "" && len(choice.Message.Annotations) > 0 {
		citations, err := bedrockCitationsFromAnnotations(text, choice.Message.Annotations)
		if err != nil {
			return result, err
		}
		result.Output.Message.Content = append(result.Output.Message.Content, BedrockContentBlock{CitationsContent: citations})
	} else if text != "" {
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
	if err := ValidateBedrockReasoningBlocks(choice.Message.Reasoning); err != nil {
		return result, err
	}
	for _, reasoning := range choice.Message.Reasoning {
		block := BedrockContentBlock{ReasoningContent: &BedrockReasoningContent{}}
		switch reasoning.Type {
		case "thinking":
			block.ReasoningContent.ReasoningText = &BedrockReasoningText{Text: reasoning.Thinking, Signature: reasoning.Signature}
		case "redacted_thinking":
			if _, err := base64.StdEncoding.DecodeString(reasoning.Data); err != nil {
				return result, errors.New("invalid redacted reasoning data")
			}
			block.ReasoningContent.RedactedContent = reasoning.Data
		}
		position := len(result.Output.Message.Content)
		if reasoning.Index != nil {
			position = *reasoning.Index
			if position > len(result.Output.Message.Content) {
				position = len(result.Output.Message.Content)
			}
		}
		result.Output.Message.Content = append(result.Output.Message.Content, BedrockContentBlock{})
		copy(result.Output.Message.Content[position+1:], result.Output.Message.Content[position:])
		result.Output.Message.Content[position] = block
	}
	if len(result.Output.Message.Content) > 128 {
		return result, errors.New("chat response contains more than 128 Converse content blocks")
	}
	stopReason, found := bedrockNativeStopReason(choice.Message.NativeContent)
	if !found {
		stopReasons := map[string]string{"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use", "content_filter": "content_filtered"}
		stopReason, found = stopReasons[choice.FinishReason]
	}
	if !found || len(result.Output.Message.Content) == 0 {
		return result, errors.New("chat response cannot be represented as Converse")
	}
	result.StopReason = stopReason
	return result, nil
}

func bedrockTrace(content []json.RawMessage) json.RawMessage {
	for _, raw := range content {
		var marker map[string]json.RawMessage
		if json.Unmarshal(raw, &marker) != nil || len(marker) != 2 {
			continue
		}
		var markerType string
		if json.Unmarshal(marker["type"], &markerType) == nil && markerType == "bedrock_guardrail_trace" && len(marker["trace"]) > 0 && string(marker["trace"]) != "null" {
			return append(json.RawMessage(nil), marker["trace"]...)
		}
	}
	return nil
}

func bedrockAdditionalResponseFields(content []json.RawMessage) json.RawMessage {
	for _, raw := range content {
		var marker map[string]json.RawMessage
		if json.Unmarshal(raw, &marker) != nil || len(marker) != 2 {
			continue
		}
		var markerType string
		if json.Unmarshal(marker["type"], &markerType) == nil && markerType == "bedrock_additional_model_response_fields" && len(marker["fields"]) > 0 && string(marker["fields"]) != "null" {
			return append(json.RawMessage(nil), marker["fields"]...)
		}
	}
	return nil
}

func bedrockNativeStopReason(content []json.RawMessage) (string, bool) {
	for _, raw := range content {
		var marker map[string]any
		if json.Unmarshal(raw, &marker) != nil || len(marker) != 2 || marker["type"] != "bedrock_stop_reason" {
			continue
		}
		reason, _ := marker["reason"].(string)
		if reason == "guardrail_intervened" || reason == "malformed_model_output" || reason == "malformed_tool_use" || reason == "model_context_window_exceeded" {
			return reason, true
		}
	}
	return "", false
}

func bedrockCitationsFromAnnotations(text string, annotations []ChatAnnotation) (*BedrockCitationsContent, error) {
	value := text
	result := &BedrockCitationsContent{Content: []BedrockCitationText{{Text: &value}}}
	for _, annotation := range annotations {
		citation := BedrockCitation{}
		switch annotation.Type {
		case "url_citation":
			if annotation.URLCitation == nil {
				return nil, errors.New("invalid URL citation")
			}
			citation.Title = annotation.URLCitation.Title
			citation.Location.Web = &BedrockWebLocation{URL: annotation.URLCitation.URL}
		case "source_citation":
			if annotation.SourceCitation == nil {
				return nil, errors.New("invalid source citation")
			}
			source := annotation.SourceCitation
			citation.Title, citation.Source = source.Title, source.Source
			for _, content := range source.SourceContent {
				content := content
				citation.SourceContent = append(citation.SourceContent, BedrockCitationSourceContent{Text: &content})
			}
			switch source.LocationType {
			case "document_char", "document_chunk", "document_page":
				location := &BedrockDocumentLocation{DocumentIndex: *source.DocumentIndex, Start: source.LocationStart, End: source.LocationEnd}
				switch source.LocationType {
				case "document_char":
					citation.Location.DocumentChar = location
				case "document_chunk":
					citation.Location.DocumentChunk = location
				case "document_page":
					citation.Location.DocumentPage = location
				}
			case "search_result":
				citation.Location.SearchResultLocation = &BedrockSearchResultLocation{SearchResultIndex: *source.SearchResultIndex, Start: source.LocationStart, End: source.LocationEnd}
			default:
				return nil, errors.New("unsupported source citation location")
			}
		default:
			return nil, errors.New("unsupported chat annotation")
		}
		result.Citations = append(result.Citations, citation)
	}
	return result, nil
}
