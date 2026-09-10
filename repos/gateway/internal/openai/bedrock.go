package openai

import (
	"encoding/json"
	"errors"
	"strings"
)

type BedrockConverseRequest struct {
	Messages        []BedrockMessage       `json:"messages"`
	System          []BedrockContentBlock  `json:"system,omitempty"`
	InferenceConfig BedrockInferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig      *BedrockToolConfig     `json:"toolConfig,omitempty"`
}

type BedrockMessage struct {
	Role    string                `json:"role"`
	Content []BedrockContentBlock `json:"content"`
}

type BedrockContentBlock struct {
	Text       *string            `json:"text,omitempty"`
	ToolUse    *BedrockToolUse    `json:"toolUse,omitempty"`
	ToolResult *BedrockToolResult `json:"toolResult,omitempty"`
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
	for _, block := range r.System {
		if block.Text == nil || strings.TrimSpace(*block.Text) == "" || block.ToolUse != nil || block.ToolResult != nil {
			return request, errors.New("system supports non-empty text blocks only")
		}
		request.Messages = append(request.Messages, Message{Role: "system", Content: *block.Text})
	}
	seenToolUses := make(map[string]bool)
	for _, message := range r.Messages {
		if (message.Role != "user" && message.Role != "assistant") || len(message.Content) == 0 {
			return request, errors.New("messages require user or assistant role and content")
		}
		textBlocks, toolUseBlocks, toolResultBlocks := 0, 0, 0
		var texts []string
		chat := Message{Role: message.Role}
		for _, block := range message.Content {
			fields := 0
			if block.Text != nil {
				fields++
				textBlocks++
				if *block.Text == "" {
					return request, errors.New("text blocks must be non-empty")
				}
				texts = append(texts, *block.Text)
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
			if message.Role != "user" || toolResultBlocks != 1 || textBlocks != 0 || toolUseBlocks != 0 || len(message.Content) != 1 {
				return request, errors.New("toolResult must be the only block in a user message")
			}
			result := message.Content[0].ToolResult
			if result.ID == "" || !seenToolUses[result.ID] || len(result.Content) != 1 || result.Content[0].Text == nil || *result.Content[0].Text == "" || result.Content[0].ToolUse != nil || result.Content[0].ToolResult != nil {
				return request, errors.New("invalid toolResult block")
			}
			request.Messages = append(request.Messages, Message{Role: "tool", ToolCallID: result.ID, Content: *result.Content[0].Text})
			continue
		}
		if len(texts) > 0 {
			chat.Content = strings.Join(texts, "")
		}
		if (message.Role == "user" && len(chat.ToolCalls) > 0) || (chat.Content == nil && len(chat.ToolCalls) == 0) {
			return request, errors.New("invalid message content")
		}
		request.Messages = append(request.Messages, chat)
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
