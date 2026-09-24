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

func ollamaToolParameterError(param, detail string) error {
	return &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: param, Err: errors.New(detail)}
}

func validOllamaToolSchema(value any, root bool) bool {
	schema, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for field, item := range schema {
		switch field {
		case "type", "items", "required":
		case "$defs":
			if !root {
				return false
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
			if field == "anyOf" {
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
