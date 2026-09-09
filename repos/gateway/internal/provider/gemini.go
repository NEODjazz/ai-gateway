package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

// Gemini uses the native GenerateContent API with API-key authentication.
// Cloud workload identity is a separate authentication contract.
type Gemini struct {
	baseURL        string
	apiKey         string
	upstreamStream bool
	client         *http.Client
}

func NewGemini(baseURL, apiKey string, stream bool) Gemini {
	client := newProviderHTTPClient(180 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return Gemini{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, upstreamStream: stream, client: client}
}

func (Gemini) SupportsVision() bool { return true }

func (Gemini) SupportsResponses() bool { return false }

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
}
type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}
type geminiFunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}
type geminiFunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}
type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}
type geminiFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parametersJsonSchema,omitempty"`
}
type geminiTool struct {
	Functions []geminiFunction `json:"functionDeclarations"`
}
type geminiGeneration struct {
	MaxOutputTokens    *int     `json:"maxOutputTokens,omitempty"`
	Temperature        *float64 `json:"temperature,omitempty"`
	TopP               *float64 `json:"topP,omitempty"`
	Seed               *int64   `json:"seed,omitempty"`
	Stop               []string `json:"stopSequences,omitempty"`
	ResponseMIMEType   string   `json:"responseMimeType,omitempty"`
	ResponseJSONSchema any      `json:"responseJsonSchema,omitempty"`
}
type geminiRequest struct {
	Contents   []geminiContent  `json:"contents"`
	System     *geminiContent   `json:"systemInstruction,omitempty"`
	Tools      []geminiTool     `json:"tools,omitempty"`
	ToolConfig map[string]any   `json:"toolConfig,omitempty"`
	Generation geminiGeneration `json:"generationConfig"`
}
type geminiResponse struct {
	ID         string `json:"responseId"`
	Model      string `json:"modelVersion"`
	Candidates []struct {
		Index        int           `json:"index"`
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	Usage          *geminiUsage `json:"usageMetadata"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error *struct {
		Code int `json:"code"`
	} `json:"error"`
}
type geminiUsage struct {
	Prompt     int `json:"promptTokenCount"`
	Cached     int `json:"cachedContentTokenCount"`
	Candidates int `json:"candidatesTokenCount"`
	Thoughts   int `json:"thoughtsTokenCount"`
	Total      int `json:"totalTokenCount"`
}

func geminiInvalid(param string) error {
	return &Error{Class: FailureClientRequest, Provider: "gemini", StatusCode: 400, UpstreamCode: "unsupported_parameter", Param: param, Err: fmt.Errorf("unsupported or invalid %s for Gemini adapter", param)}
}

func (Gemini) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	_, err := geminiChatRequest(request)
	return err
}
func (Gemini) ValidateResponseParameters(openai.ResponseRequest) error {
	return geminiInvalid("responses")
}
func (g Gemini) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, geminiInvalid("responses")
}

func geminiChatRequest(request openai.ChatCompletionRequest) (geminiRequest, error) {
	result := geminiRequest{}
	if err := validateChatMessagePrefix("gemini", request.Messages, false); err != nil {
		return result, err
	}
	if err := rejectLegacyFunctionCalling("gemini", request); err != nil {
		return result, err
	}
	if err := validateChatPromptCacheBreakpoints("gemini", request, false); err != nil {
		return result, err
	}
	if err := rejectGenerationOptions("gemini", request.ChatGenerationOptions); err != nil {
		return result, err
	}
	if err := rejectChatMessageRefusals("gemini", request.Messages); err != nil {
		return result, err
	}
	if err := rejectChatMessageAudio("gemini", request.Messages); err != nil {
		return result, err
	}
	if request.ParallelToolCalls != nil {
		return result, geminiInvalid("parallel_tool_calls")
	}
	if request.MaxTokens != nil && request.MaxCompletionTokens != nil {
		return result, geminiInvalid("max_tokens")
	}
	maxTokens := request.MaxCompletionTokens
	if maxTokens == nil {
		maxTokens = request.MaxTokens
	}
	if maxTokens != nil && (*maxTokens <= 0 || *maxTokens > math.MaxInt32) {
		return result, geminiInvalid("max_completion_tokens")
	}
	if request.Seed != nil && (*request.Seed < math.MinInt32 || *request.Seed > math.MaxInt32) {
		return result, geminiInvalid("seed")
	}
	stop, valid := openai.StopSequences(request.Stop)
	if !valid {
		return result, geminiInvalid("stop")
	}
	result.Generation = geminiGeneration{MaxOutputTokens: maxTokens, Temperature: request.Temperature, TopP: request.TopP, Seed: request.Seed, Stop: stop}
	if request.ResponseFormat != nil {
		switch request.ResponseFormat.Type {
		case "text":
		case "json_object":
			result.Generation.ResponseMIMEType = "application/json"
		case "json_schema":
			if request.ResponseFormat.JSONSchema == nil || request.ResponseFormat.JSONSchema.Schema == nil {
				return result, geminiInvalid("response_format")
			}
			result.Generation.ResponseMIMEType = "application/json"
			result.Generation.ResponseJSONSchema = request.ResponseFormat.JSONSchema.Schema
		default:
			return result, geminiInvalid("response_format")
		}
	}
	if _, err := openai.ChatImageAttachments(request.Messages); err != nil {
		return result, err
	}
	toolNames := make(map[string]string)
	for _, message := range request.Messages {
		if (message.Role != "assistant" && len(message.ToolCalls) > 0) || (message.Role != "tool" && message.ToolCallID != "") {
			return result, geminiInvalid("messages.tool_calls")
		}
		if message.Name != "" {
			return result, geminiInvalid("messages.name")
		}
		content := geminiContent{Role: "user"}
		var parts []geminiPart
		var err error
		if message.Role != "tool" {
			parts, err = geminiMessageParts(message.Content)
		}
		if err != nil {
			return result, err
		}
		content.Parts = parts
		switch message.Role {
		case "system", "developer":
			if result.System == nil {
				result.System = &geminiContent{}
			}
			result.System.Parts = append(result.System.Parts, parts...)
			continue
		case "user":
		case "assistant":
			content.Role = "model"
			for _, call := range message.ToolCalls {
				if call.Type != "function" || call.Function.Name == "" {
					return result, geminiInvalid("messages.tool_calls")
				}
				var args map[string]any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
					return result, geminiInvalid("messages.tool_calls.arguments")
				}
				part := geminiPart{FunctionCall: &geminiFunctionCall{ID: call.ID, Name: call.Function.Name, Args: args}}
				if call.ExtraContent != nil && call.ExtraContent.Google != nil {
					part.ThoughtSignature = call.ExtraContent.Google.ThoughtSignature
				}
				content.Parts = append(content.Parts, part)
				toolNames[call.ID] = call.Function.Name
			}
		case "tool":
			name, ok := toolNames[message.ToolCallID]
			if !ok || message.ToolCallID == "" {
				return result, geminiInvalid("messages.tool_call_id")
			}
			content.Parts = []geminiPart{{FunctionResponse: &geminiFunctionResponse{ID: message.ToolCallID, Name: name, Response: geminiToolResponse(message.Content)}}}
		default:
			return result, geminiInvalid("messages.role")
		}
		if len(content.Parts) == 0 {
			return result, geminiInvalid("messages.content")
		}
		if message.Role == "tool" && len(result.Contents) > 0 {
			last := &result.Contents[len(result.Contents)-1]
			if last.Role == "user" && len(last.Parts) > 0 && last.Parts[0].FunctionResponse != nil {
				last.Parts = append(last.Parts, content.Parts...)
				continue
			}
		}
		result.Contents = append(result.Contents, content)
	}
	if len(result.Contents) == 0 {
		return result, geminiInvalid("messages")
	}
	if len(request.Tools) > 0 {
		tool := geminiTool{}
		for _, definition := range request.Tools {
			if definition.Type != "function" || definition.Function.Name == "" {
				return result, geminiInvalid("tools")
			}
			if definition.Function.Strict != nil && *definition.Function.Strict {
				return result, geminiInvalid("tools.function.strict")
			}
			tool.Functions = append(tool.Functions, geminiFunction{Name: definition.Function.Name, Description: definition.Function.Description, Parameters: definition.Function.Parameters})
		}
		result.Tools = []geminiTool{tool}
	}
	if request.ToolChoice != nil {
		config := map[string]any{}
		switch choice := request.ToolChoice.(type) {
		case string:
			switch choice {
			case "auto":
				config["mode"] = "AUTO"
			case "none":
				config["mode"] = "NONE"
			case "required":
				config["mode"] = "ANY"
			default:
				return result, geminiInvalid("tool_choice")
			}
		case map[string]any:
			function, ok := choice["function"].(map[string]any)
			name, _ := function["name"].(string)
			if !ok || choice["type"] != "function" || name == "" {
				return result, geminiInvalid("tool_choice")
			}
			config["mode"] = "ANY"
			config["allowedFunctionNames"] = []string{name}
		default:
			return result, geminiInvalid("tool_choice")
		}
		result.ToolConfig = map[string]any{"functionCallingConfig": config}
	}
	return result, nil
}

func geminiMessageParts(value any) ([]geminiPart, error) {
	switch value := value.(type) {
	case nil:
		return nil, nil
	case string:
		if value == "" {
			return nil, nil
		}
		return []geminiPart{{Text: value}}, nil
	case []any:
		parts := make([]geminiPart, 0, len(value))
		for _, raw := range value {
			part, ok := raw.(map[string]any)
			if !ok {
				return nil, geminiInvalid("messages.content")
			}
			switch part["type"] {
			case "text":
				text, ok := part["text"].(string)
				if !ok {
					return nil, geminiInvalid("messages.content.text")
				}
				parts = append(parts, geminiPart{Text: text})
			case "image_url":
				image, _ := part["image_url"].(map[string]any)
				data, _ := image["url"].(string)
				attachment, err := openai.ParseDataImageURL(data)
				if err != nil {
					return nil, err
				}
				parts = append(parts, geminiPart{InlineData: &geminiInlineData{MIMEType: attachment.MediaType, Data: attachment.Data}})
			default:
				return nil, geminiInvalid("messages.content.type")
			}
		}
		return parts, nil
	default:
		return nil, geminiInvalid("messages.content")
	}
}

func geminiToolResponse(value any) map[string]any {
	if object, ok := value.(map[string]any); ok && object != nil {
		return object
	}
	if text, ok := value.(string); ok {
		var object map[string]any
		if json.Unmarshal([]byte(text), &object) == nil && object != nil {
			return object
		}
	}
	return map[string]any{"result": value}
}

func geminiBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(baseURL, "/v1beta") && !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1beta"
	}
	return baseURL
}
func (g Gemini) generate(ctx context.Context, request openai.ChatCompletionRequest, stream bool) (*http.Response, error) {
	body, err := geminiChatRequest(request)
	if err != nil {
		return nil, err
	}
	model := strings.TrimPrefix(request.Model, "models/")
	if model == "" || strings.ContainsAny(model, "/\\?#%") || model == "." || model == ".." {
		return nil, geminiInvalid("model")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	method := "generateContent"
	if stream {
		method = "streamGenerateContent"
	}
	base, err := url.Parse(g.baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("invalid Gemini base URL")
	}
	endpoint := geminiBaseURL(g.baseURL) + "/models/" + url.PathEscape(model) + ":" + method
	if stream {
		endpoint += "?alt=sse"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.apiKey)
	response, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := responseStatusError("gemini", response)
		_ = response.Body.Close()
		return nil, failure
	}
	return response, nil
}

func (g Gemini) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	response, err := g.generate(ctx, request, false)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if len(payload) > 32<<20 {
		return openai.ChatCompletionResponse{}, errors.New("Gemini response exceeds limit")
	}
	var body geminiResponse
	if err = json.Unmarshal(payload, &body); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	result, err := geminiToChat(body, request.Model)
	if err == nil && len(result.Choices) == 0 {
		err = errors.New("Gemini response produced no candidates")
	}
	return result, err
}

func geminiToChat(body geminiResponse, model string) (openai.ChatCompletionResponse, error) {
	if body.Error != nil {
		code := body.Error.Code
		if code < 400 || code > 599 {
			code = 502
		}
		return openai.ChatCompletionResponse{}, statusError("gemini", code)
	}
	if body.PromptFeedback.BlockReason != "" {
		return openai.ChatCompletionResponse{}, &Error{Class: FailureContentPolicy, Provider: "gemini", StatusCode: 400, UpstreamCode: "content_policy_violation", Err: errors.New("upstream content policy rejected prompt")}
	}
	if body.Model != "" {
		model = body.Model
	}
	result := openai.ChatCompletionResponse{ID: body.ID, Object: "chat.completion", Model: model}
	if result.ID == "" {
		result.ID = "chatcmpl-" + rand.Text()
	}
	if body.Usage != nil {
		u := body.Usage
		if u.Prompt < 0 || u.Candidates < 0 || u.Thoughts < 0 || u.Cached < 0 || u.Cached > u.Prompt || u.Candidates > math.MaxInt-u.Thoughts || u.Total < 0 {
			return result, errors.New("invalid Gemini usage")
		}
		if u.Prompt > math.MaxInt-(u.Candidates+u.Thoughts) || u.Total < u.Prompt+u.Candidates+u.Thoughts {
			return result, errors.New("inconsistent Gemini usage")
		}
		result.Usage = openai.Usage{PromptTokens: u.Prompt, CompletionTokens: u.Candidates + u.Thoughts, TotalTokens: u.Total}
		if u.Thoughts > 0 {
			result.Usage.CompletionTokensDetails = &openai.CompletionTokenDetails{ReasoningTokens: u.Thoughts}
		}
		if u.Cached > 0 {
			result.Usage.PromptTokensDetails = &openai.PromptTokenDetails{CachedTokens: u.Cached}
		}
	}
	if len(body.Candidates) > maxChatStreamChoices {
		return result, errors.New("too many Gemini candidates")
	}
	seen := map[int]bool{}
	for _, candidate := range body.Candidates {
		if seen[candidate.Index] {
			return result, errors.New("duplicate Gemini candidate index")
		}
		seen[candidate.Index] = true
		if candidate.Index < 0 || candidate.Index >= maxChatStreamChoices {
			return result, errors.New("invalid Gemini candidate index")
		}
		choice := openai.Choice{Index: candidate.Index, Message: openai.Message{Role: "assistant"}}
		var text strings.Builder
		for _, part := range candidate.Content.Parts {
			if part.Thought {
				continue
			}
			if part.InlineData != nil || part.FunctionResponse != nil {
				return result, errors.New("unsupported Gemini output modality")
			}
			text.WriteString(part.Text)
			if part.FunctionCall != nil {
				if part.FunctionCall.Name == "" {
					return result, errors.New("invalid Gemini function call")
				}
				if len(choice.Message.ToolCalls) >= maxChatStreamToolCalls {
					return result, errors.New("too many Gemini tool calls")
				}
				arguments := part.FunctionCall.Args
				if arguments == nil {
					arguments = map[string]any{}
				}
				args, err := json.Marshal(arguments)
				if err != nil {
					return result, err
				}
				id := part.FunctionCall.ID
				if id == "" {
					id = "call_" + rand.Text()
				}
				call := openai.ToolCall{ID: id, Type: "function", Function: openai.FunctionCall{Name: part.FunctionCall.Name, Arguments: string(args)}}
				if part.ThoughtSignature != "" {
					call.ExtraContent = &openai.ToolCallExtraContent{Google: &openai.GoogleToolCallContent{ThoughtSignature: part.ThoughtSignature}}
				}
				choice.Message.ToolCalls = append(choice.Message.ToolCalls, call)
			}
		}
		choice.Message.Content = text.String()
		switch candidate.FinishReason {
		case "":
		case "STOP":
			choice.FinishReason = "stop"
		case "MAX_TOKENS":
			choice.FinishReason = "length"
		case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
			choice.FinishReason = "content_filter"
		default:
			return result, errors.New("Gemini generation failed")
		}
		if len(choice.Message.ToolCalls) > 0 && choice.FinishReason == "stop" {
			choice.FinishReason = "tool_calls"
		}
		result.Choices = append(result.Choices, choice)
	}
	return result, nil
}

func (g Gemini) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := g.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if !g.upstreamStream {
		return openai.ChatCompletionResponse{}, ErrStreamingUnsupported
	}
	response, err := g.generate(ctx, request, true)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer response.Body.Close()
	result := openai.ChatCompletionResponse{ID: "chatcmpl-" + rand.Text(), Object: "chat.completion", Model: request.Model}
	consumed := 0
	err = scanSSEData(response.Body, func(payload string) error {
		consumed += len(payload)
		if consumed > 64<<20 {
			return errors.New("Gemini stream exceeds limit")
		}
		var body geminiResponse
		if err := json.Unmarshal([]byte(payload), &body); err != nil {
			return err
		}
		if body.ID != "" {
			result.ID = body.ID
		}
		body.ID = result.ID
		chunk, err := geminiToChat(body, result.Model)
		if err != nil {
			return err
		}
		result.Model = chunk.Model
		if body.Usage != nil {
			result.Usage = chunk.Usage
		}
		choices := make([]map[string]any, 0, len(chunk.Choices))
		for _, choice := range chunk.Choices {
			for len(result.Choices) <= choice.Index {
				result.Choices = append(result.Choices, openai.Choice{Index: len(result.Choices), Message: openai.Message{Role: "assistant"}})
			}
			current := &result.Choices[choice.Index]
			current.Message.Content = openai.ContentText(current.Message.Content) + openai.ContentText(choice.Message.Content)
			for i := range choice.Message.ToolCalls {
				index := len(current.Message.ToolCalls)
				if index >= maxChatStreamToolCalls {
					return errors.New("too many Gemini tool calls")
				}
				choice.Message.ToolCalls[i].Index = &index
				current.Message.ToolCalls = append(current.Message.ToolCalls, choice.Message.ToolCalls[i])
			}
			if choice.FinishReason == "stop" && len(current.Message.ToolCalls) > 0 {
				choice.FinishReason = "tool_calls"
			}
			if choice.FinishReason != "" {
				current.FinishReason = choice.FinishReason
			}
			var finish any
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			choices = append(choices, map[string]any{"index": choice.Index, "delta": choice.Message, "finish_reason": finish})
		}
		event := map[string]any{"id": result.ID, "object": "chat.completion.chunk", "model": result.Model, "choices": choices}
		if body.Usage != nil {
			event["usage"] = chunk.Usage
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		return write(string(encoded))
	})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if len(result.Choices) == 0 {
		return openai.ChatCompletionResponse{}, errors.New("Gemini stream produced no candidates")
	}
	for _, choice := range result.Choices {
		if choice.FinishReason == "" {
			return openai.ChatCompletionResponse{}, errors.New("Gemini stream ended before completion")
		}
	}
	return result, nil
}
