package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"ai-gateway-gateway/internal/openai"
)

func validateOllamaChatTools(tools []openai.Tool) error {
	for index, tool := range tools {
		if tool.Type != "function" || tool.Function.Name == "" || tool.Function.Strict != nil || tool.Function.DeferLoading {
			return ollamaToolParameterError("tools", fmt.Sprintf("tool %d has unsupported function controls", index))
		}
		if tool.Function.Parameters == nil {
			continue
		}
		encoded, err := json.Marshal(tool.Function.Parameters)
		if err != nil {
			return ollamaToolParameterError("tools.function.parameters", "tool parameters must be JSON")
		}
		var schema any
		if json.Unmarshal(encoded, &schema) != nil || !validOllamaToolSchema(schema, true) {
			return ollamaToolParameterError("tools.function.parameters", "tool parameters contain unsupported schema fields")
		}
	}
	return nil
}

func validateOllamaResponseTools(tools []openai.ResponseTool) error {
	for index, tool := range tools {
		if tool.Type != "function" {
			continue
		}
		if tool.Strict != nil {
			return ollamaToolParameterError("tools.strict", fmt.Sprintf("tool %d has unsupported strict control", index))
		}
		if tool.Parameters == nil {
			continue
		}
		encoded, err := json.Marshal(tool.Parameters)
		if err != nil {
			return ollamaToolParameterError("tools.parameters", "tool parameters must be JSON")
		}
		var schema any
		if json.Unmarshal(encoded, &schema) != nil || !validOllamaToolSchema(schema, true) {
			return ollamaToolParameterError("tools.parameters", "tool parameters contain unsupported schema fields")
		}
	}
	return nil
}

func ollamaToolParameterError(param, detail string) error {
	return &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: param, Err: errors.New(detail)}
}

func validOllamaToolSchema(value any, root bool) bool {
	schema, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if root && schema["type"] != "object" {
		return false
	}
	for field, item := range schema {
		switch field {
		case "type":
			if !validOllamaToolType(item) {
				return false
			}
		case "items":
			if !validOllamaToolSchema(item, false) {
				return false
			}
		case "required":
			values, ok := item.([]any)
			if !ok {
				return false
			}
			for _, value := range values {
				if name, ok := value.(string); !ok || name == "" {
					return false
				}
			}
		case "$defs":
			if !root {
				return false
			}
			definitions, ok := item.(map[string]any)
			if !ok {
				return false
			}
			for name, definition := range definitions {
				if name == "" || !validOllamaToolSchema(definition, false) {
					return false
				}
			}
		case "properties":
			properties, ok := item.(map[string]any)
			if !ok {
				return false
			}
			for _, property := range properties {
				if !validOllamaToolSchema(property, false) {
					return false
				}
			}
		case "anyOf", "description", "enum":
			if root {
				return false
			}
			switch field {
			case "description":
				if _, ok := item.(string); !ok {
					return false
				}
			case "enum":
				values, ok := item.([]any)
				if !ok || len(values) == 0 {
					return false
				}
			case "anyOf":
				variants, ok := item.([]any)
				if !ok || len(variants) == 0 {
					return false
				}
				for _, variant := range variants {
					if !validOllamaToolSchema(variant, false) {
						return false
					}
				}
			}
		default:
			return false
		}
	}
	return true
}

func validOllamaToolType(value any) bool {
	valid := func(value string) bool {
		switch value {
		case "array", "boolean", "integer", "null", "number", "object", "string":
			return true
		}
		return false
	}
	if value, ok := value.(string); ok {
		return valid(value)
	}
	values, ok := value.([]any)
	if !ok || len(values) == 0 {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		name, ok := value.(string)
		if !ok || !valid(name) || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}
