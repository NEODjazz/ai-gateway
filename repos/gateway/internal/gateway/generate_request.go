package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

type generateRequest struct {
	ServiceTier string                       `json:"serviceTier,omitempty"`
	Store       *bool                        `json:"store,omitempty"`
	Contents    []generateContent            `json:"contents"`
	System      *generateContent             `json:"systemInstruction,omitempty"`
	Safety      []openai.GeminiSafetySetting `json:"safetySettings,omitempty"`
	Tools       []struct {
		Functions     []generateFunction `json:"functionDeclarations,omitempty"`
		GoogleSearch  *struct{}          `json:"googleSearch,omitempty"`
		CodeExecution *struct{}          `json:"codeExecution,omitempty"`
		URLContext    *struct{}          `json:"urlContext,omitempty"`
	} `json:"tools,omitempty"`
	ToolConfig *struct {
		FunctionCalling struct {
			Mode  string   `json:"mode"`
			Names []string `json:"allowedFunctionNames,omitempty"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig,omitempty"`
	Generation struct {
		MaxOutputTokens  *int           `json:"maxOutputTokens,omitempty"`
		Temperature      *float64       `json:"temperature,omitempty"`
		TopP             *float64       `json:"topP,omitempty"`
		TopK             *int           `json:"topK,omitempty"`
		Seed             *int64         `json:"seed,omitempty"`
		Stop             []string       `json:"stopSequences,omitempty"`
		CandidateCount   *int           `json:"candidateCount,omitempty"`
		PresencePenalty  *float64       `json:"presencePenalty,omitempty"`
		FrequencyPenalty *float64       `json:"frequencyPenalty,omitempty"`
		ResponseLogprobs *bool          `json:"responseLogprobs,omitempty"`
		Logprobs         *int           `json:"logprobs,omitempty"`
		Modalities       []string       `json:"responseModalities,omitempty"`
		MIMEType         string         `json:"responseMimeType,omitempty"`
		JSONSchema       map[string]any `json:"responseJsonSchema,omitempty"`
		Schema           map[string]any `json:"responseSchema,omitempty"`
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
	FileData *struct {
		MIMEType string `json:"mimeType"`
		FileURI  string `json:"fileUri"`
	} `json:"fileData,omitempty"`
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
	ExecutableCode      *openai.GeminiExecutableCode      `json:"executableCode,omitempty"`
	CodeExecutionResult *openai.GeminiCodeExecutionResult `json:"codeExecutionResult,omitempty"`
	Signature           string                            `json:"thoughtSignature,omitempty"`
	Thought             bool                              `json:"thought,omitempty"`
}

func (r generateRequest) chat(model string, stream bool) (openai.ChatCompletionRequest, error) {
	result := openai.ChatCompletionRequest{Model: model, Stream: stream, MaxCompletionTokens: r.Generation.MaxOutputTokens, Temperature: r.Generation.Temperature, TopP: r.Generation.TopP, Seed: r.Generation.Seed}
	if stream {
		result.StreamOptions = &openai.ChatStreamOptions{IncludeUsage: true}
	}
	fail := func(field string) (openai.ChatCompletionRequest, error) {
		return result, fmt.Errorf("invalid or unsupported %s", field)
	}
	switch r.ServiceTier {
	case "":
	case "unspecified":
		result.ServiceTier = "auto"
	case "standard":
		result.ServiceTier = "standard_only"
	case "flex", "priority":
		result.ServiceTier = r.ServiceTier
	default:
		return fail("serviceTier")
	}
	result.Store = r.Store
	if strings.TrimSpace(model) == "" || len(r.Contents) == 0 || len(r.Contents) > 10000 {
		return fail("contents")
	}
	if err := openai.ValidateGeminiSafetySettings(r.Safety); err != nil {
		return fail("safetySettings")
	}
	if len(r.Safety) > 0 {
		result.GeminiSafetySettings = append([]openai.GeminiSafetySetting(nil), r.Safety...)
		result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(r.Safety))
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
	if n := r.Generation.TopK; n != nil && (*n < 1 || *n > 1000000) {
		return fail("topK")
	}
	for _, penalty := range []struct {
		name  string
		value *float64
	}{
		{name: "presencePenalty", value: r.Generation.PresencePenalty},
		{name: "frequencyPenalty", value: r.Generation.FrequencyPenalty},
	} {
		if penalty.value != nil && (math.IsNaN(*penalty.value) || math.IsInf(*penalty.value, 0) || *penalty.value < -2 || *penalty.value > 2) {
			return fail(penalty.name)
		}
	}
	if n := r.Generation.Logprobs; n != nil && (*n < 0 || *n > 20 || r.Generation.ResponseLogprobs == nil || !*r.Generation.ResponseLogprobs) {
		return fail("logprobs")
	}
	if r.Generation.Modalities != nil {
		if len(r.Generation.Modalities) > 1 || len(r.Generation.Modalities) == 1 && r.Generation.Modalities[0] != "TEXT" {
			return fail("responseModalities")
		}
	}
	result.TopK = r.Generation.TopK
	result.PresencePenalty = r.Generation.PresencePenalty
	result.FrequencyPenalty = r.Generation.FrequencyPenalty
	result.Logprobs = r.Generation.ResponseLogprobs
	result.TopLogprobs = r.Generation.Logprobs
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
			if part.Text == nil || part.InlineData != nil || part.FileData != nil || part.Call != nil || part.Result != nil || part.Signature != "" {
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
			if len(parts) > 0 || len(message.ToolCalls) > 0 || len(message.Reasoning) > 0 || len(message.GeminiCodeExecutionParts) > 0 {
				message.Content = parts
				result.Messages = append(result.Messages, message)
				message = openai.Message{Role: role}
				parts = []any{}
			}
		}
		for partIndex, part := range content.Parts {
			members := 0
			for _, present := range []bool{part.Text != nil, part.InlineData != nil, part.FileData != nil, part.Call != nil, part.Result != nil, part.ExecutableCode != nil, part.CodeExecutionResult != nil} {
				if present {
					members++
				}
			}
			if members != 1 || (part.Thought && part.Text == nil) || part.Signature != "" && part.Text == nil && part.Call == nil || part.Text != nil && part.Signature != "" && !validGenerateBase64(part.Signature) {
				return fail("contents.parts")
			}
			switch {
			case part.Text != nil:
				if part.Thought {
					if role != "assistant" || *part.Text == "" {
						return fail("thought")
					}
					index := partIndex
					message.Reasoning = append(message.Reasoning, openai.ReasoningBlock{Index: &index, Type: "thinking", Thinking: *part.Text, Signature: part.Signature})
					continue
				}
				if len(message.ToolCalls) > 0 {
					return fail("text after functionCall")
				}
				parts = append(parts, map[string]any{"type": "text", "text": *part.Text})
				if part.Signature != "" {
					if role != "assistant" {
						return fail("thoughtSignature role")
					}
					var err error
					message.NativeContent, err = openai.AddGeminiPartSignature(message.NativeContent, partIndex, part.Signature)
					if err != nil {
						return result, err
					}
					result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(part.Signature))
				}
			case part.InlineData != nil:
				if role != "user" {
					return fail("inlineData role")
				}
				data := "data:" + part.InlineData.MIMEType + ";base64," + part.InlineData.Data
				if _, err := openai.ParseDataImageURL(data); err == nil {
					parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": data}})
					break
				}
				if part.InlineData.MIMEType == "application/pdf" {
					file := map[string]any{"type": "input_file", "file_data": data, "filename": "input.pdf"}
					if _, err := openai.ResponseFileAttachments([]any{file}); err != nil {
						return result, err
					}
					parts = append(parts, file)
					break
				}
				videoFormat := ""
				switch part.InlineData.MIMEType {
				case "video/mp4":
					videoFormat = "mp4"
				case "video/webm":
					videoFormat = "webm"
				}
				if videoFormat != "" {
					video := map[string]any{"type": "input_video", "input_video": map[string]any{"data": part.InlineData.Data, "format": videoFormat}}
					if _, err := openai.ChatVideoAttachments([]openai.Message{{Role: "user", Content: []any{video}}}); err != nil {
						return result, err
					}
					parts = append(parts, video)
					break
				}
				format, filename := generateAudioFormat(part.InlineData.MIMEType)
				if format == "" {
					return fail("inlineData.mimeType")
				}
				if err := openai.ValidateAudioAttachment(openai.AudioAttachment{Filename: filename, MediaType: part.InlineData.MIMEType, Data: part.InlineData.Data}); err != nil {
					return result, err
				}
				parts = append(parts, map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": part.InlineData.Data, "format": format}})
			case part.FileData != nil:
				if role != "user" || !validFileToken(part.FileData.FileURI, 128) || !strings.HasPrefix(part.FileData.FileURI, "file_") {
					return fail("fileData")
				}
				referenceType := generateFileReferenceType(part.FileData.MIMEType)
				if referenceType == "" {
					return fail("fileData.mimeType")
				}
				parts = append(parts, map[string]any{"type": referenceType, "file_id": part.FileData.FileURI, "media_type": part.FileData.MIMEType})
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
			case part.ExecutableCode != nil:
				if role != "assistant" {
					return fail("executableCode role")
				}
				block := openai.GeminiCodeExecutionPart{Index: partIndex, Code: part.ExecutableCode}
				if err := openai.ValidateGeminiCodeExecutionParts([]openai.GeminiCodeExecutionPart{block}); err != nil {
					return result, err
				}
				message.GeminiCodeExecutionParts = append(message.GeminiCodeExecutionParts, block)
				result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(part.ExecutableCode))
			case part.CodeExecutionResult != nil:
				if role != "assistant" {
					return fail("codeExecutionResult role")
				}
				block := openai.GeminiCodeExecutionPart{Index: partIndex, Result: part.CodeExecutionResult}
				if err := openai.ValidateGeminiCodeExecutionParts([]openai.GeminiCodeExecutionPart{block}); err != nil {
					return result, err
				}
				message.GeminiCodeExecutionParts = append(message.GeminiCodeExecutionParts, block)
				result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(part.CodeExecutionResult))
			}
		}
		flush()
	}
	if _, err := openai.ChatFileAttachments(result.Messages); err != nil {
		return result, err
	}
	if _, err := openai.ChatVideoAttachments(result.Messages); err != nil {
		return result, err
	}
	for _, tool := range r.Tools {
		members := 0
		for _, present := range []bool{len(tool.Functions) > 0, tool.GoogleSearch != nil, tool.CodeExecution != nil, tool.URLContext != nil} {
			if present {
				members++
			}
		}
		if members != 1 {
			return fail("tools")
		}
		if tool.GoogleSearch != nil {
			if result.WebSearchOptions != nil {
				return fail("googleSearch")
			}
			result.WebSearchOptions = &openai.ChatWebSearchOptions{}
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(tool))
			continue
		}
		if tool.CodeExecution != nil {
			if result.GeminiCodeExecution {
				return fail("codeExecution")
			}
			result.GeminiCodeExecution = true
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(tool))
			continue
		}
		if tool.URLContext != nil {
			if result.GeminiURLContext {
				return fail("urlContext")
			}
			result.GeminiURLContext = true
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(tool))
			continue
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
		if len(result.Tools) == 0 {
			return fail("toolConfig")
		}
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
	if _, err := openai.ChatAudioAttachments(result.Messages); err != nil {
		return result, err
	}
	return result, nil
}

func generateFileReferenceType(mediaType string) string {
	if supportedA2AImageType(mediaType) {
		return "input_file_image_reference"
	}
	if supportedA2AVideoType(mediaType) {
		return "input_file_video_reference"
	}
	switch mediaType {
	case "application/pdf", "text/plain":
		return "input_file_reference"
	case "audio/wav", "audio/mp3", "audio/mpeg", "audio/aiff", "audio/aac", "audio/ogg", "audio/opus", "audio/flac", "audio/m4a", "audio/webm", "audio/mp4":
		return "input_file_audio_reference"
	default:
		return ""
	}
}

func validGenerateBase64(value string) bool {
	_, err := base64.StdEncoding.DecodeString(value)
	return err == nil
}
