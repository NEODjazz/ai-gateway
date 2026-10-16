package provider

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"ai-gateway-gateway/internal/openai"
)

// The OpenAI middleware discards native options. Configured context windows
// therefore use native chat while preserving the public Responses contract.
func (p Ollama) contextResponseChat(request openai.ResponseRequest) (openai.ChatCompletionRequest, error) {
	chat := openai.ChatCompletionRequest{Model: request.Model, MaxCompletionTokens: responseOutputTokenLimit(request), Temperature: request.Temperature, TopP: request.TopP}
	if request.Reasoning != nil && request.Reasoning.Effort != nil {
		chat.ReasoningEffort = *request.Reasoning.Effort
	}
	if request.Instructions != "" {
		chat.Messages = append(chat.Messages, openai.Message{Role: "system", Content: request.Instructions})
	}
	if text, ok := request.Input.(string); ok {
		chat.Messages = append(chat.Messages, openai.Message{Role: "user", Content: text})
	} else {
		encoded, err := json.Marshal(request.Input)
		if err != nil {
			return chat, err
		}
		var items []map[string]any
		if err := json.Unmarshal(encoded, &items); err != nil {
			return chat, ollamaContextInputError("input")
		}
		for _, item := range items {
			kind, _ := item["type"].(string)
			switch kind {
			case "", "message":
				role, _ := item["role"].(string)
				if role == "developer" {
					role = "system"
				}
				if role != "user" && role != "assistant" && role != "system" {
					return chat, ollamaContextInputError("input.role")
				}
				content := item["content"]
				if parts, ok := content.([]any); ok {
					copy := make([]any, 0, len(parts))
					for _, part := range parts {
						obj, ok := part.(map[string]any)
						if !ok {
							return chat, ollamaContextInputError("input.content")
						}
						switch obj["type"] {
						case "input_text", "output_text", "text":
							copy = append(copy, map[string]any{"type": "text", "text": obj["text"]})
						default:
							return chat, ollamaContextInputError("input.content.type")
						}
					}
					content = copy
				}
				chat.Messages = append(chat.Messages, openai.Message{Role: role, Content: content})
			case "input_text", "text":
				text, ok := item["text"].(string)
				if !ok {
					return chat, ollamaContextInputError("input.text")
				}
				chat.Messages = append(chat.Messages, openai.Message{Role: "user", Content: text})
			case "function_call":
				name, _ := item["name"].(string)
				id, _ := item["call_id"].(string)
				args, _ := item["arguments"].(string)
				chat.Messages = append(chat.Messages, openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: id, Type: "function", Function: openai.FunctionCall{Name: name, Arguments: args}}}})
			case "function_call_output":
				id, _ := item["call_id"].(string)
				chat.Messages = append(chat.Messages, openai.Message{Role: "tool", ToolCallID: id, Content: item["output"]})
			case "reasoning":
				if encrypted, ok := item["encrypted_content"].(string); ok && encrypted != "" {
					return chat, ollamaContextInputError("input.encrypted_content")
				}
				content := openai.ContentText(item["content"])
				if content == "" {
					content = openai.ContentText(item["summary"])
				}
				if content != "" {
					chat.Messages = append(chat.Messages, openai.Message{Role: "assistant", ReasoningContent: content})
				}
			default:
				return chat, ollamaContextInputError("input.type")
			}
		}
	}
	for _, tool := range ollamaResponseTools(request) {
		chat.Tools = append(chat.Tools, openai.Tool{Type: "function", Function: openai.FunctionDefinition{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters, Strict: tool.Strict}})
	}
	if request.Text != nil {
		text, err := normalizeOllamaResponseText(request.Text, p.supportsStructuredOutput(request.Model))
		if err != nil {
			return chat, err
		}
		encoded, err := json.Marshal(text)
		if err != nil {
			return chat, err
		}
		var wire struct {
			Format struct {
				Type   string
				Name   string
				Schema any
			}
		}
		if err := json.Unmarshal(encoded, &wire); err != nil {
			return chat, err
		}
		if wire.Format.Type != "" {
			chat.ResponseFormat = &openai.ResponseFormat{Type: wire.Format.Type}
			if wire.Format.Type == "json_schema" {
				chat.ResponseFormat.JSONSchema = &openai.JSONSchemaFormat{Name: wire.Format.Name, Schema: wire.Format.Schema}
			}
		}
	}
	return chat, nil
}

func ollamaContextInputError(param string) error {
	return &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: 400, UpstreamCode: "unsupported_parameter", Param: param, Err: fmt.Errorf("parameter %s cannot be represented by the native Ollama context adapter", param)}
}

func (p Ollama) contextResponses(ctx context.Context, request openai.ResponseRequest, stream bool, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	chat, err := p.contextResponseChat(request)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	id := "resp_" + rand.Text()
	response := openai.ResponseResponse{ID: id, Object: "response", CreatedAt: time.Now().Unix(), Model: request.Model, Status: "in_progress", Output: []openai.ResponseOutputItem{}}
	indices := map[string]int{}
	sequence := 0
	emit := func(kind string, body map[string]any) error {
		if write == nil {
			return nil
		}
		body["type"] = kind
		body["sequence_number"] = sequence
		sequence++
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		return write(kind, string(data))
	}
	created := false
	create := func() error {
		if created {
			return nil
		}
		created = true
		return emit("response.created", map[string]any{"response": response})
	}
	appendDelta := func(kind, delta string) error {
		if delta == "" {
			return nil
		}
		if err := create(); err != nil {
			return err
		}
		index, found := indices[kind]
		if !found {
			index = len(response.Output)
			indices[kind] = index
			item := openai.ResponseOutputItem{ID: fmt.Sprintf("item_%s_%d", id, index), Type: kind, Status: "in_progress"}
			part := openai.ResponseOutputContent{Type: "reasoning_text"}
			if kind == "message" {
				item.Role = "assistant"
				part.Type = "output_text"
			}
			response.Output = append(response.Output, item)
			if err := emit("response.output_item.added", map[string]any{"output_index": index, "item": item}); err != nil {
				return err
			}
			response.Output[index].Content = []openai.ResponseOutputContent{part}
			if kind == "message" {
				if err := emit("response.content_part.added", map[string]any{"output_index": index, "item_id": item.ID, "content_index": 0, "part": part}); err != nil {
					return err
				}
			}
		}
		response.Output[index].Content[0].Text += delta
		event := "response.reasoning_text.delta"
		if kind == "message" {
			event = "response.output_text.delta"
		}
		return emit(event, map[string]any{"output_index": index, "item_id": response.Output[index].ID, "content_index": 0, "delta": delta})
	}
	var completed openai.ChatCompletionResponse
	if stream {
		completed, err = p.StreamChatCompletions(ctx, chat, func(payload string) error {
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content   string `json:"content"`
						Reasoning string `json:"reasoning_content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				return err
			}
			for _, c := range chunk.Choices {
				if err := appendDelta("reasoning", c.Delta.Reasoning); err != nil {
					return err
				}
				if err := appendDelta("message", c.Delta.Content); err != nil {
					return err
				}
			}
			return nil
		})
	} else {
		completed, err = p.ChatCompletions(ctx, chat)
	}
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	if len(completed.Choices) != 1 {
		return openai.ResponseResponse{}, fmt.Errorf("Ollama native response has no single choice")
	}
	choice := completed.Choices[0]
	if !stream {
		if err := appendDelta("reasoning", choice.Message.ReasoningContent); err != nil {
			return response, err
		}
		if err := appendDelta("message", openai.ContentText(choice.Message.Content)); err != nil {
			return response, err
		}
	}
	if err := create(); err != nil {
		return response, err
	}
	for index := range response.Output {
		item := &response.Output[index]
		item.Status = "completed"
		event := "response.reasoning_text.done"
		if item.Type == "message" {
			event = "response.output_text.done"
		}
		if err := emit(event, map[string]any{"output_index": index, "item_id": item.ID, "content_index": 0, "text": item.Content[0].Text}); err != nil {
			return response, err
		}
		if item.Type == "message" {
			if err := emit("response.content_part.done", map[string]any{"output_index": index, "item_id": item.ID, "content_index": 0, "part": item.Content[0]}); err != nil {
				return response, err
			}
		}
		if err := emit("response.output_item.done", map[string]any{"output_index": index, "item": item}); err != nil {
			return response, err
		}
	}
	for _, call := range choice.Message.ToolCalls {
		index := len(response.Output)
		item := openai.ResponseOutputItem{ID: "fc_" + call.ID, Type: "function_call", CallID: call.ID, Name: call.Function.Name, Status: "in_progress"}
		if err := emit("response.output_item.added", map[string]any{"output_index": index, "item": item}); err != nil {
			return response, err
		}
		if err := emit("response.function_call_arguments.delta", map[string]any{"output_index": index, "item_id": item.ID, "delta": call.Function.Arguments}); err != nil {
			return response, err
		}
		item.Arguments = call.Function.Arguments
		item.Status = "completed"
		response.Output = append(response.Output, item)
		if err := emit("response.function_call_arguments.done", map[string]any{"output_index": index, "item_id": item.ID, "arguments": item.Arguments}); err != nil {
			return response, err
		}
		if err := emit("response.output_item.done", map[string]any{"output_index": index, "item": item}); err != nil {
			return response, err
		}
	}
	response.Status = "completed"
	terminal := "response.completed"
	if choice.FinishReason == "length" {
		response.Status = "incomplete"
		terminal = "response.incomplete"
		response.IncompleteDetails = &openai.ResponseIncompleteDetails{Reason: "max_output_tokens"}
	}
	response.CompletedAt = time.Now().Unix()
	response.OutputText = openai.ContentText(choice.Message.Content)
	response.Usage = openai.ResponseUsage{InputTokens: completed.Usage.PromptTokens, OutputTokens: completed.Usage.CompletionTokens, TotalTokens: completed.Usage.TotalTokens, OutputTokensDetails: completed.Usage.CompletionTokensDetails}
	if completed.Usage.PromptTokensDetails != nil {
		response.Usage.InputTokensDetails = &openai.InputTokenDetails{CachedTokens: completed.Usage.PromptTokensDetails.CachedTokens}
	}
	response.InputTokensReported = true
	response.OutputTokensReported = true
	response.TotalTokensReported = true
	if err := emit(terminal, map[string]any{"response": response}); err != nil {
		return response, err
	}
	return response, nil
}
