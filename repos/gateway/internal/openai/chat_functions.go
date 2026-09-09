package openai

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxChatFunctionArgumentsChars = 1 << 20

var chatFunctionName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidateLegacyFunctionRequest(request ChatCompletionRequest) error {
	hasLegacyHistory, hasToolHistory := false, false
	for _, message := range request.Messages {
		hasLegacyHistory = hasLegacyHistory || message.FunctionCall != nil || message.Role == "function"
		hasToolHistory = hasToolHistory || len(message.ToolCalls) > 0 || message.ToolCallID != "" || message.Role == "tool"
	}
	legacy := len(request.Functions) > 0 || request.FunctionCall != nil || hasLegacyHistory
	modern := len(request.Tools) > 0 || request.ToolChoice != nil || request.ParallelToolCalls != nil || hasToolHistory
	if legacy && modern {
		return errors.New("legacy functions and modern tools are mutually exclusive")
	}
	if len(request.Functions) > 128 {
		return errors.New("functions must contain at most 128 definitions")
	}
	names := make(map[string]struct{}, len(request.Functions))
	for _, function := range request.Functions {
		if !chatFunctionName.MatchString(function.Name) {
			return errors.New("function names must contain 1 to 64 letters, digits, underscores, or hyphens")
		}
		if utf8.RuneCountInString(function.Description) > 4096 {
			return errors.New("function descriptions must contain at most 4096 characters")
		}
		if function.Parameters != nil {
			if _, ok := function.Parameters.(map[string]any); !ok {
				return errors.New("function parameters must be a JSON object")
			}
		}
		if _, exists := names[function.Name]; exists {
			return errors.New("function names must be unique")
		}
		names[function.Name] = struct{}{}
	}
	if choice := request.FunctionCall; choice != nil {
		if choice.Name != "" {
			if choice.Mode != "" {
				return errors.New("function_call must select either a mode or a name")
			}
			if _, exists := names[choice.Name]; !exists {
				return errors.New("function_call name must identify a declared function")
			}
		} else {
			switch choice.Mode {
			case "none":
			case "auto":
				if len(request.Functions) == 0 {
					return errors.New("function_call=auto requires functions")
				}
			default:
				return errors.New("function_call must be none, auto, or a named function")
			}
		}
	}
	for _, message := range request.Messages {
		if message.FunctionCall != nil {
			if message.Role != "assistant" || len(message.ToolCalls) > 0 || message.ToolCallID != "" || !validCompleteFunctionCall(*message.FunctionCall) {
				return errors.New("messages.function_call requires a complete assistant function call")
			}
		}
		if message.Role == "function" {
			if !chatFunctionName.MatchString(message.Name) || message.FunctionCall != nil || len(message.ToolCalls) > 0 || message.ToolCallID != "" {
				return errors.New("function messages require a valid name and cannot contain tool calls")
			}
		}
	}
	return nil
}

func ValidateLegacyFunctionResponse(call *FunctionCall) error {
	if call == nil {
		return nil
	}
	if !validCompleteFunctionCall(*call) {
		return errors.New("invalid legacy function call")
	}
	return nil
}

func validCompleteFunctionCall(call FunctionCall) bool {
	return chatFunctionName.MatchString(call.Name) && utf8.RuneCountInString(call.Arguments) <= MaxChatFunctionArgumentsChars && strings.TrimSpace(call.Arguments) != ""
}

func ChatHasLegacyFunctionHistory(request ChatCompletionRequest) bool {
	for _, message := range request.Messages {
		if message.FunctionCall != nil || message.Role == "function" {
			return true
		}
	}
	return false
}

func ChatRequiresFunctionCapability(request ChatCompletionRequest) bool {
	if len(request.Functions) > 0 || ChatHasLegacyFunctionHistory(request) {
		return true
	}
	return request.FunctionCall != nil && (request.FunctionCall.Name != "" || request.FunctionCall.Mode == "auto")
}
