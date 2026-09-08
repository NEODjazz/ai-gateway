package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

type generateRequest struct {
	Contents []generateContent `json:"contents"`
	System   *generateContent  `json:"systemInstruction,omitempty"`
	Tools    []struct {
		Functions []generateFunction `json:"functionDeclarations"`
	} `json:"tools,omitempty"`
	ToolConfig *struct {
		FunctionCalling struct {
			Mode  string   `json:"mode"`
			Names []string `json:"allowedFunctionNames,omitempty"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig,omitempty"`
	Generation struct {
		MaxOutputTokens *int           `json:"maxOutputTokens,omitempty"`
		Temperature     *float64       `json:"temperature,omitempty"`
		TopP            *float64       `json:"topP,omitempty"`
		Seed            *int64         `json:"seed,omitempty"`
		Stop            []string       `json:"stopSequences,omitempty"`
		CandidateCount  *int           `json:"candidateCount,omitempty"`
		MIMEType        string         `json:"responseMimeType,omitempty"`
		JSONSchema      map[string]any `json:"responseJsonSchema,omitempty"`
		Schema          map[string]any `json:"responseSchema,omitempty"`
	} `json:"generationConfig,omitempty"`
}
type generateContent struct {
	Role  string         `json:"role,omitempty"`
	Parts []generatePart `json:"parts"`
}
type generateFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	JSONSchema  map[string]any `json:"parametersJsonSchema,omitempty"`
}
type generatePart struct {
	Text       *string `json:"text,omitempty"`
	InlineData *struct {
		MIMEType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData,omitempty"`
	Call *struct {
		ID   string         `json:"id,omitempty"`
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	} `json:"functionCall,omitempty"`
	Result *struct {
		ID       string         `json:"id,omitempty"`
		Name     string         `json:"name"`
		Response map[string]any `json:"response"`
	} `json:"functionResponse,omitempty"`
	Signature string `json:"thoughtSignature,omitempty"`
}

func (r generateRequest) chat(model string, stream bool) (openai.ChatCompletionRequest, error) {
	result := openai.ChatCompletionRequest{Model: model, Stream: stream, MaxCompletionTokens: r.Generation.MaxOutputTokens, Temperature: r.Generation.Temperature, TopP: r.Generation.TopP, Seed: r.Generation.Seed}
	fail := func(field string) (openai.ChatCompletionRequest, error) {
		return result, fmt.Errorf("invalid or unsupported %s", field)
	}
	if strings.TrimSpace(model) == "" || len(r.Contents) == 0 || len(r.Contents) > 10000 {
		return fail("contents")
	}
	if n := r.Generation.CandidateCount; n != nil && *n != 1 {
		return fail("candidateCount")
	}
	if n := r.Generation.MaxOutputTokens; n != nil && (*n <= 0 || *n > math.MaxInt32) {
		return fail("maxOutputTokens")
	}
	if n := r.Generation.Seed; n != nil && (*n < math.MinInt32 || *n > math.MaxInt32) {
		return fail("seed")
	}
	if n := r.Generation.Temperature; n != nil && (*n < 0 || *n > 2) {
		return fail("temperature")
	}
	if n := r.Generation.TopP; n != nil && (*n < 0 || *n > 1) {
		return fail("topP")
	}
	if _, valid := openai.StopSequences(r.Generation.Stop); !valid {
		return fail("stopSequences")
	}
	if len(r.Generation.Stop) > 0 {
		result.Stop = r.Generation.Stop
	}
	schema := r.Generation.JSONSchema
	if r.Generation.Schema != nil {
		if schema != nil {
			return fail("responseSchema")
		}
		var err error
		schema, err = generateSchema(r.Generation.Schema)
		if err != nil {
			return result, err
		}
	}
	switch r.Generation.MIMEType {
	case "", "text/plain":
		if schema != nil {
			return fail("responseMimeType")
		}
	case "application/json":
		result.ResponseFormat = &openai.ResponseFormat{Type: "json_object"}
		if schema != nil {
			result.ResponseFormat = &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "response", Schema: schema}}
		}
	default:
		return fail("responseMimeType")
	}
	if r.System != nil {
		if r.System.Role != "" && r.System.Role != "system" {
			return fail("systemInstruction.role")
		}
		parts := []any{}
		for _, part := range r.System.Parts {
			if part.Text == nil || part.InlineData != nil || part.Call != nil || part.Result != nil || part.Signature != "" {
				return fail("systemInstruction.parts")
			}
			parts = append(parts, map[string]any{"type": "text", "text": *part.Text})
		}
		if len(parts) == 0 {
			return fail("systemInstruction.parts")
		}
		result.Messages = append(result.Messages, openai.Message{Role: "system", Content: parts})
	}
	pending := []openai.ToolCall{}
	seen := map[string]bool{}
	callIndex := 0
	for _, content := range r.Contents {
		role := content.Role
		if role == "" {
			role = "user"
		}
		if role != "user" && role != "model" {
			return fail("contents.role")
		}
		if role == "model" {
			role = "assistant"
		}
		if len(content.Parts) == 0 {
			return fail("contents.parts")
		}
		message := openai.Message{Role: role}
		parts := []any{}
		flush := func() {
			if len(parts) > 0 || len(message.ToolCalls) > 0 {
				message.Content = parts
				result.Messages = append(result.Messages, message)
				message = openai.Message{Role: role}
				parts = []any{}
			}
		}
		for _, part := range content.Parts {
			members := 0
			for _, present := range []bool{part.Text != nil, part.InlineData != nil, part.Call != nil, part.Result != nil} {
				if present {
					members++
				}
			}
			if members != 1 || (part.Signature != "" && part.Call == nil) {
				return fail("contents.parts")
			}
			switch {
			case part.Text != nil:
				if len(message.ToolCalls) > 0 {
					return fail("text after functionCall")
				}
				parts = append(parts, map[string]any{"type": "text", "text": *part.Text})
			case part.InlineData != nil:
				if role != "user" {
					return fail("inlineData role")
				}
				data := "data:" + part.InlineData.MIMEType + ";base64," + part.InlineData.Data
				if _, err := openai.ParseDataImageURL(data); err != nil {
					return result, err
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": data}})
			case part.Call != nil:
				callIndex++
				call := part.Call
				if role != "assistant" || call.Name == "" {
					return fail("functionCall")
				}
				id := call.ID
				if id == "" {
					id = fmt.Sprintf("native_call_%d", callIndex)
					for seen[id] {
						callIndex++
						id = fmt.Sprintf("native_call_%d", callIndex)
					}
				}
				if seen[id] {
					return fail("functionCall.id")
				}
				seen[id] = true
				arguments := call.Args
				if arguments == nil {
					arguments = map[string]any{}
				}
				args, err := json.Marshal(arguments)
				if err != nil {
					return result, err
				}
				converted := openai.ToolCall{ID: id, Type: "function", Function: openai.FunctionCall{Name: call.Name, Arguments: string(args)}}
				if part.Signature != "" {
					converted.ExtraContent = &openai.ToolCallExtraContent{Google: &openai.GoogleToolCallContent{ThoughtSignature: part.Signature}}
				}
				message.ToolCalls = append(message.ToolCalls, converted)
				pending = append(pending, converted)
			case part.Result != nil:
				response := part.Result
				if role != "user" || response.Name == "" || response.Response == nil {
					return fail("functionResponse")
				}
				found := -1
				for index, call := range pending {
					if call.Function.Name == response.Name && (response.ID == "" || call.ID == response.ID) {
						if found >= 0 && response.ID == "" {
							return fail("ambiguous functionResponse reference")
						}
						found = index
					}
				}
				if found < 0 {
					return fail("functionResponse reference")
				}
				encoded, err := json.Marshal(response.Response)
				if err != nil {
					return result, err
				}
				flush()
				result.Messages = append(result.Messages, openai.Message{Role: "tool", ToolCallID: pending[found].ID, Content: string(encoded)})
				pending = append(pending[:found], pending[found+1:]...)
			}
		}
		flush()
	}
	for _, tool := range r.Tools {
		if len(tool.Functions) == 0 {
			return fail("functionDeclarations")
		}
		for _, function := range tool.Functions {
			if function.Name == "" {
				return fail("functionDeclarations.name")
			}
			schema := function.JSONSchema
			if function.Parameters != nil {
				if schema != nil {
					return fail("functionDeclarations.parameters")
				}
				var err error
				schema, err = generateSchema(function.Parameters)
				if err != nil {
					return result, err
				}
			}
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			result.Tools = append(result.Tools, openai.Tool{Type: "function", Function: openai.FunctionDefinition{Name: function.Name, Description: function.Description, Parameters: schema}})
		}
	}
	if len(result.Tools) > 128 {
		return fail("functionDeclarations")
	}
	if r.ToolConfig != nil {
		config := r.ToolConfig.FunctionCalling
		switch config.Mode {
		case "AUTO":
			result.ToolChoice = "auto"
		case "NONE":
			result.ToolChoice = "none"
		case "ANY":
			result.ToolChoice = "required"
		default:
			return fail("functionCallingConfig.mode")
		}
		if len(config.Names) > 0 {
			if config.Mode != "ANY" || len(config.Names) != 1 || config.Names[0] == "" {
				return fail("allowedFunctionNames")
			}
			result.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": config.Names[0]}}
		}
	}
	if _, err := openai.ChatImageAttachments(result.Messages); err != nil {
		return result, err
	}
	return result, nil
}

// Convert the supported native Schema subset to JSON Schema. Unknown fields
// fail rather than silently weakening a caller's schema constraints.
func generateSchema(native map[string]any) (map[string]any, error) {
	result := map[string]any{}
	for key, value := range native {
		switch key {
		case "type":
			name, ok := value.(string)
			if !ok {
				return nil, errors.New("invalid schema type")
			}
			name = strings.ToLower(name)
			switch name {
			case "object", "array", "string", "number", "integer", "boolean", "null":
			default:
				return nil, errors.New("unsupported schema type")
			}
			result[key] = name
		case "properties":
			properties, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("invalid schema properties")
			}
			converted := map[string]any{}
			for name, property := range properties {
				schema, ok := property.(map[string]any)
				if !ok {
					return nil, errors.New("invalid property schema")
				}
				item, err := generateSchema(schema)
				if err != nil {
					return nil, err
				}
				converted[name] = item
			}
			result[key] = converted
		case "items":
			schema, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("invalid items schema")
			}
			item, err := generateSchema(schema)
			if err != nil {
				return nil, err
			}
			result[key] = item
		case "description", "format", "enum", "required", "minimum", "maximum":
			result[key] = value
		case "nullable":
			if value != false {
				return nil, errors.New("nullable native schemas require responseJsonSchema/parametersJsonSchema")
			}
		default:
			return nil, fmt.Errorf("unsupported native schema field %q; use JSON Schema", key)
		}
	}
	return result, nil
}
