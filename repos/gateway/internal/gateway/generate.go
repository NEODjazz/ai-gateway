package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

var generateRoutes = []RouteContract{
	{http.MethodPost, "/v1beta/models/{model}:generateContent"},
	{http.MethodPost, "/v1beta/models/{model}:countTokens"},
	{http.MethodPost, "/v1beta/models/{model}:streamGenerateContent"},
}

func (h Handler) GenerateContent(w http.ResponseWriter, r *http.Request) {
	output := &generateWriter{destination: w, headers: make(http.Header), status: http.StatusOK}
	defer output.finish()
	model, action, ok := generateModelAction(r.PathValue("modelAction"))
	if !ok {
		writeError(output, 404, "not_found", "unknown model operation")
		return
	}
	streaming := action == "streamGenerateContent"
	err := validateGenerateQuery(r, streaming)
	if err != nil {
		writeError(output, 400, "invalid_request", err.Error())
		return
	}
	key := bearerToken(r.Header.Get("Authorization"))
	if native := r.Header.Get("x-goog-api-key"); native != "" {
		if key != "" && key != native {
			writeError(output, 400, "invalid_request", "conflicting authentication headers")
			return
		}
		key = native
	}
	if action == "countTokens" {
		h.countGenerateTokens(output, r, model, key)
		return
	}
	var request generateRequest
	if !decodeInferenceRequest(output, r, &request) {
		return
	}
	chat, err := request.chat(model, streaming)
	if err != nil {
		writeError(output, 400, "invalid_request", err.Error())
		return
	}
	copyRequest := r.Clone(r.Context())
	copyRequest.Header.Set("Authorization", "Bearer "+key)
	output.model = model
	h.serveChatAs(output, copyRequest, chat, "generate_content")
}

func generateModelAction(value string) (string, string, bool) {
	separator := strings.LastIndexByte(value, ':')
	if separator <= 0 || separator == len(value)-1 {
		return "", "", false
	}
	model, action := value[:separator], value[separator+1:]
	if len(model) > 256 || (action != "generateContent" && action != "streamGenerateContent" && action != "countTokens") {
		return "", "", false
	}
	return model, action, true
}

func validateGenerateQuery(r *http.Request, stream bool) error {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return errors.New("invalid query parameters")
	}
	for key := range query {
		if key != "alt" {
			return errors.New("unsupported query parameter; send gateway credentials in headers")
		}
	}
	alt := query["alt"]
	if len(alt) > 1 {
		return errors.New("alt must occur at most once")
	}
	value := query.Get("alt")
	if stream && value != "sse" {
		return errors.New("streamGenerateContent requires alt=sse")
	}
	if !stream && value != "" && value != "json" {
		return errors.New("generateContent requires JSON output")
	}
	return nil
}

type generateWriter struct {
	destination        http.ResponseWriter
	headers            http.Header
	status             int
	buffer             bytes.Buffer
	started, terminal  bool
	id, model, reason  string
	serviceTier        string
	usage              openai.Usage
	tools              [128]*openai.ToolCall
	toolNames          [128]strings.Builder
	toolArguments      [128]strings.Builder
	toolBytes          int
	partSignatures     []openai.GeminiPartSignature
	codeExecutionParts []openai.GeminiCodeExecutionPart
	directResponse     *openai.ChatCompletionResponse
	err                error
}

func (w *generateWriter) Header() http.Header  { return w.headers }
func (w *generateWriter) WriteHeader(code int) { w.status = code }
func (w *generateWriter) Flush()               {}
func (w *generateWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if len(data) > (32<<20)-w.buffer.Len() {
		w.err = errors.New("response frame exceeds limit")
		return 0, w.err
	}
	w.buffer.Write(data)
	if w.headers.Get("Content-Type") == "text/event-stream" {
		for {
			end := bytes.Index(w.buffer.Bytes(), []byte("\n\n"))
			if end < 0 {
				break
			}
			frame := string(w.buffer.Next(end + 2))
			payload := strings.TrimSpace(strings.TrimPrefix(frame, "data:"))
			if err := w.chunk(payload); err != nil {
				w.err = err
				return 0, err
			}
		}
	}
	return len(data), nil
}
func (w *generateWriter) copyHeaders() {
	for key, values := range w.headers {
		w.destination.Header()[key] = append([]string(nil), values...)
	}
}
func (w *generateWriter) event(value any) error {
	if !w.started {
		w.copyHeaders()
		writeStreamHeaders(w.destination)
		w.destination.WriteHeader(200)
		w.started = true
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeSSEPayload(w.destination, string(payload))
}
func generateReason(reason string) (string, error) {
	switch reason {
	case "stop", "tool_calls":
		return "STOP", nil
	case "length":
		return "MAX_TOKENS", nil
	case "content_filter":
		return "SAFETY", nil
	default:
		return "", errors.New("unsupported finish reason")
	}
}
func generateUsage(usage openai.Usage, serviceTier string) (map[string]any, error) {
	thoughts, cached := 0, 0
	if usage.CompletionTokensDetails != nil {
		thoughts = usage.CompletionTokensDetails.ReasoningTokens
	}
	if usage.PromptTokensDetails != nil {
		cached = usage.PromptTokensDetails.CachedTokens
	}
	if usage.PromptTokens < 0 || usage.ProviderToolInputTokens < 0 || usage.ProviderToolInputTokens > usage.PromptTokens || usage.CompletionTokens < 0 || usage.TotalTokens < 0 || thoughts < 0 || thoughts > usage.CompletionTokens || cached < 0 || cached > usage.PromptTokens-usage.ProviderToolInputTokens || usage.PromptTokens > math.MaxInt-usage.CompletionTokens || usage.TotalTokens < usage.PromptTokens+usage.CompletionTokens {
		return nil, errors.New("invalid usage")
	}
	result := map[string]any{"promptTokenCount": usage.PromptTokens - usage.ProviderToolInputTokens, "candidatesTokenCount": usage.CompletionTokens - thoughts, "thoughtsTokenCount": thoughts, "cachedContentTokenCount": cached, "totalTokenCount": usage.TotalTokens}
	if usage.ProviderToolInputTokens > 0 {
		result["toolUsePromptTokenCount"] = usage.ProviderToolInputTokens
	}
	switch serviceTier {
	case "":
	case "auto", "default":
		result["serviceTier"] = "unspecified"
	case "standard", "standard_only":
		result["serviceTier"] = "standard"
	case "flex", "priority":
		result["serviceTier"] = serviceTier
	default:
		return nil, errors.New("invalid service tier")
	}
	return result, nil
}
func generateParts(message openai.Message) ([]any, error) {
	parts := []any{}
	if text := openai.ContentText(message.Content); text != "" {
		parts = append(parts, map[string]any{"text": text})
	}
	for _, call := range message.ToolCalls {
		var args map[string]any
		arguments := call.Function.Arguments
		if arguments == "" {
			arguments = "{}"
		}
		if call.Type != "function" || call.Function.Name == "" || json.Unmarshal([]byte(arguments), &args) != nil || args == nil {
			return nil, errors.New("invalid tool response")
		}
		part := map[string]any{"functionCall": map[string]any{"id": call.ID, "name": call.Function.Name, "args": args}}
		if call.ExtraContent != nil && call.ExtraContent.Google != nil && call.ExtraContent.Google.ThoughtSignature != "" {
			part["thoughtSignature"] = call.ExtraContent.Google.ThoughtSignature
		}
		parts = append(parts, part)
	}
	if err := openai.ValidateBedrockReasoningBlocks(message.Reasoning); err != nil {
		return nil, err
	}
	for _, reasoning := range message.Reasoning {
		if reasoning.Type != "thinking" || reasoning.Signature != "" && !validGenerateBase64(reasoning.Signature) {
			return nil, errors.New("invalid Gemini thought block")
		}
		part := map[string]any{"text": reasoning.Thinking, "thought": true}
		if reasoning.Signature != "" {
			part["thoughtSignature"] = reasoning.Signature
		}
		position := len(parts)
		if reasoning.Index != nil {
			position = min(*reasoning.Index, len(parts))
		}
		parts = append(parts, nil)
		copy(parts[position+1:], parts[position:])
		parts[position] = part
	}
	signatures, err := openai.GeminiPartSignatures(message.NativeContent)
	if err != nil {
		return nil, err
	}
	for _, signature := range signatures {
		position := min(signature.Index, len(parts))
		if position < len(parts) {
			if part, ok := parts[position].(map[string]any); ok {
				if _, textPart := part["text"]; textPart && part["thought"] != true {
					part["thoughtSignature"] = signature.Signature
					continue
				}
			}
		}
		part := map[string]any{"text": "", "thoughtSignature": signature.Signature}
		parts = append(parts, nil)
		copy(parts[position+1:], parts[position:])
		parts[position] = part
	}
	if err := openai.ValidateGeminiCodeExecutionParts(message.GeminiCodeExecutionParts); err != nil {
		return nil, err
	}
	for _, block := range message.GeminiCodeExecutionParts {
		var part map[string]any
		if block.Code != nil {
			part = map[string]any{"executableCode": block.Code}
		} else {
			part = map[string]any{"codeExecutionResult": block.Result}
		}
		position := min(block.Index, len(parts))
		parts = append(parts, nil)
		copy(parts[position+1:], parts[position:])
		parts[position] = part
	}
	return parts, nil
}
func generateEnvelope(id, model string, parts []any, reason string, usage map[string]any, grounding ...json.RawMessage) map[string]any {
	candidate := map[string]any{"index": 0}
	if len(parts) > 0 {
		candidate["content"] = map[string]any{"role": "model", "parts": parts}
	}
	if reason != "" {
		candidate["finishReason"] = reason
	}
	if len(grounding) > 0 && len(grounding[0]) > 0 {
		candidate["groundingMetadata"] = grounding[0]
	}
	result := map[string]any{"responseId": id, "modelVersion": model, "candidates": []any{candidate}}
	if usage != nil {
		result["usageMetadata"] = usage
	}
	return result
}
func (w *generateWriter) chunk(payload string) error {
	if w.terminal {
		return nil
	}
	if payload == "[DONE]" {
		if w.reason == "" {
			return errors.New("stream ended without finish reason")
		}
		message := openai.Message{Role: "assistant", GeminiCodeExecutionParts: append([]openai.GeminiCodeExecutionPart(nil), w.codeExecutionParts...)}
		for index, call := range w.tools {
			if call != nil {
				call.Function.Name = w.toolNames[index].String()
				call.Function.Arguments = w.toolArguments[index].String()
				message.ToolCalls = append(message.ToolCalls, *call)
			}
		}
		parts, err := generateParts(message)
		if err != nil {
			return err
		}
		for _, signature := range w.partSignatures {
			parts = append(parts, map[string]any{"text": "", "thoughtSignature": signature.Signature})
		}
		usage, err := generateUsage(w.usage, w.serviceTier)
		if err != nil {
			return err
		}
		if err := w.event(generateEnvelope(w.id, w.model, parts, w.reason, usage)); err != nil {
			return err
		}
		w.terminal = true
		return nil
	}
	var chunk struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Index     int             `json:"index"`
			Delta     openai.Message  `json:"delta"`
			Reason    string          `json:"finish_reason"`
			Grounding json.RawMessage `json:"gemini_grounding_metadata"`
		} `json:"choices"`
		Usage       *openai.Usage   `json:"usage"`
		ServiceTier string          `json:"service_tier"`
		Error       json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return err
	}
	if len(chunk.Error) > 0 {
		w.terminal = true
		return w.event(generateError(502, "inference stream failed"))
	}
	if chunk.ID != "" {
		w.id = chunk.ID
	}
	if chunk.Model != "" {
		w.model = chunk.Model
	}
	if chunk.Usage != nil {
		if _, err := generateUsage(*chunk.Usage, chunk.ServiceTier); err != nil {
			return err
		}
		w.usage = *chunk.Usage
	}
	if chunk.ServiceTier != "" {
		if w.serviceTier != "" && w.serviceTier != chunk.ServiceTier {
			return errors.New("service tier changed")
		}
		w.serviceTier = chunk.ServiceTier
	}
	if len(chunk.Choices) > 1 {
		return errors.New("multiple choices are unsupported")
	}
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			return errors.New("invalid choice index")
		}
		if choice.Reason != "" {
			reason, err := generateReason(choice.Reason)
			if err != nil {
				return err
			}
			w.reason = reason
		}
		if text := openai.ContentText(choice.Delta.Content); text != "" || len(choice.Grounding) > 0 {
			parts := []any(nil)
			if text != "" {
				parts = []any{map[string]any{"text": text}}
			}
			if err := w.event(generateEnvelope(w.id, w.model, parts, "", nil, choice.Grounding)); err != nil {
				return err
			}
		}
		for _, reasoning := range choice.Delta.Reasoning {
			if reasoning.Type != "thinking" || reasoning.Thinking == "" && reasoning.Signature == "" || reasoning.Signature != "" && !validGenerateBase64(reasoning.Signature) {
				return errors.New("invalid Gemini thought stream delta")
			}
			part := map[string]any{"text": reasoning.Thinking, "thought": true}
			if reasoning.Signature != "" {
				part["thoughtSignature"] = reasoning.Signature
			}
			if err := w.event(generateEnvelope(w.id, w.model, []any{part}, "", nil)); err != nil {
				return err
			}
		}
		for _, delta := range choice.Delta.ToolCalls {
			if delta.Type != "" && delta.Type != "function" {
				return errors.New("unsupported tool type")
			}
			if delta.Index == nil || *delta.Index < 0 || *delta.Index >= len(w.tools) {
				return errors.New("invalid tool index")
			}
			call := w.tools[*delta.Index]
			if call == nil {
				call = &openai.ToolCall{Type: "function"}
				w.tools[*delta.Index] = call
			}
			extra := len(delta.ID) + len(delta.Function.Name) + len(delta.Function.Arguments)
			if delta.ExtraContent != nil && delta.ExtraContent.Google != nil {
				extra += len(delta.ExtraContent.Google.ThoughtSignature)
			}
			if extra > (32<<20)-w.toolBytes {
				return errors.New("tool response exceeds limit")
			}
			w.toolBytes += extra
			if delta.ID != "" {
				if call.ID != "" && call.ID != delta.ID {
					return errors.New("tool ID changed")
				}
				call.ID = delta.ID
			}
			w.toolNames[*delta.Index].WriteString(delta.Function.Name)
			w.toolArguments[*delta.Index].WriteString(delta.Function.Arguments)
			if delta.ExtraContent != nil && delta.ExtraContent.Google != nil && delta.ExtraContent.Google.ThoughtSignature != "" {
				if call.ExtraContent != nil && call.ExtraContent.Google.ThoughtSignature != delta.ExtraContent.Google.ThoughtSignature {
					return errors.New("tool signature changed")
				}
				call.ExtraContent = delta.ExtraContent
			}
		}
	}
	return nil
}
func (w *generateWriter) chatStreamResult(response openai.ChatCompletionResponse) {
	if _, err := generateUsage(response.Usage, response.ServiceTier); err != nil {
		w.err = err
		return
	}
	w.usage = response.Usage
	w.serviceTier = response.ServiceTier
	if response.ID != "" {
		w.id = response.ID
	}
	if response.Model != "" {
		w.model = response.Model
	}
	if len(response.Choices) == 1 {
		signatures, err := openai.GeminiPartSignatures(response.Choices[0].Message.NativeContent)
		if err != nil {
			w.err = err
			return
		}
		w.partSignatures = signatures
		if err := openai.ValidateGeminiCodeExecutionParts(response.Choices[0].Message.GeminiCodeExecutionParts); err != nil {
			w.err = err
			return
		}
		w.codeExecutionParts = append([]openai.GeminiCodeExecutionPart(nil), response.Choices[0].Message.GeminiCodeExecutionParts...)
	}
}

func (w *generateWriter) chatResult(response openai.ChatCompletionResponse, stream bool) {
	if stream {
		if len(response.Choices) != 1 || response.Choices[0].Index != 0 {
			w.err = errors.New("invalid provider response")
			return
		}
		parts, err := generateParts(response.Choices[0].Message)
		if err != nil {
			w.err = err
			return
		}
		reason, err := generateReason(response.Choices[0].FinishReason)
		if err != nil {
			w.err = err
			return
		}
		usage, err := generateUsage(response.Usage, response.ServiceTier)
		if err != nil {
			w.err = err
			return
		}
		if err := w.event(generateEnvelope(response.ID, response.Model, parts, reason, usage, response.Choices[0].GeminiGroundingMetadata)); err != nil {
			w.err = err
			return
		}
		w.terminal = true
		return
	}
	copy := response
	w.directResponse = &copy
}
func generateError(code int, message string) map[string]any {
	status := "INTERNAL"
	switch code {
	case 400:
		status = "INVALID_ARGUMENT"
	case 401:
		status = "UNAUTHENTICATED"
	case 403, 451:
		status = "PERMISSION_DENIED"
	case 404:
		status = "NOT_FOUND"
	case 413, 429:
		status = "RESOURCE_EXHAUSTED"
	case 503:
		status = "UNAVAILABLE"
	case 408, 504:
		status = "DEADLINE_EXCEEDED"
	}
	return map[string]any{"error": map[string]any{"code": code, "status": status, "message": message}}
}
func (w *generateWriter) finish() {
	if w.started {
		if !w.terminal {
			_ = w.event(generateError(502, "inference stream did not complete"))
		}
		return
	}
	w.copyHeaders()
	if w.err != nil {
		w.status = 502
	}
	if w.status >= 400 {
		message := "request failed"
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if w.status < 500 && json.Unmarshal(w.buffer.Bytes(), &body) == nil && body.Error.Message != "" {
			message = body.Error.Message
		}
		writeJSON(w.destination, w.status, generateError(w.status, message))
		return
	}
	var response openai.ChatCompletionResponse
	var err error
	if w.directResponse != nil {
		response = *w.directResponse
	} else {
		err = json.Unmarshal(w.buffer.Bytes(), &response)
	}
	var parts []any
	var reason string
	var usage map[string]any
	if err == nil && len(response.Choices) == 1 && response.Choices[0].Index == 0 {
		parts, err = generateParts(response.Choices[0].Message)
		if err == nil {
			reason, err = generateReason(response.Choices[0].FinishReason)
		}
		if err == nil {
			usage, err = generateUsage(response.Usage, response.ServiceTier)
		}
	} else {
		err = errors.New("invalid provider response")
	}
	if err != nil {
		writeJSON(w.destination, 502, generateError(502, "provider response cannot be represented as GenerateContent"))
		return
	}
	writeJSON(w.destination, 200, generateEnvelope(response.ID, response.Model, parts, reason, usage, response.Choices[0].GeminiGroundingMetadata))
}
