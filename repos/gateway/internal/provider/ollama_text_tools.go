package provider

import (
	"encoding/json"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

func ollamaTextToolCallsEnabled(model string) bool {
	return model == "llama3.2" || strings.HasPrefix(model, "llama3.2:")
}

// The llama3.2 tool template asks for {"name": ..., "parameters": ...}, but
// Ollama can return that object as plain content instead of a native tool call.
// Convert only a complete, unambiguous call to a declared function.
func promoteOllamaTextToolCall(message *openai.Message, tools []openai.Tool) {
	if message == nil || len(message.ToolCalls) != 0 || len(tools) == 0 {
		return
	}
	content := strings.TrimSpace(openai.ContentText(message.Content))
	if len(content) == 0 || len(content) > openai.MaxChatFunctionArgumentsChars+1024 {
		return
	}
	var call map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &call) != nil || len(call) != 2 {
		return
	}
	var name string
	if json.Unmarshal(call["name"], &name) != nil || name == "" {
		return
	}
	declared := false
	for _, tool := range tools {
		if tool.Type == "function" && tool.Function.Name == name {
			declared = true
			break
		}
	}
	if !declared {
		return
	}
	var arguments map[string]any
	if json.Unmarshal(call["parameters"], &arguments) != nil || arguments == nil || len(call["parameters"]) > openai.MaxChatFunctionArgumentsChars {
		return
	}
	message.Content = ""
	message.ToolCalls = []openai.ToolCall{{Function: openai.FunctionCall{Name: name, Arguments: string(call["parameters"])}}}
}
