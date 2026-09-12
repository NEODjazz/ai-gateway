package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

func (h Handler) Messages(w http.ResponseWriter, r *http.Request) {
	output := &messagesWriter{destination: w, headers: make(http.Header), status: http.StatusOK, tools: map[int]int{}}
	defer output.finish()
	if version := r.Header.Get("anthropic-version"); version != "2023-06-01" {
		writeError(output, 400, "invalid_request", "anthropic-version must be 2023-06-01")
		return
	}
	if r.Header.Get("anthropic-beta") != "" {
		writeError(output, 400, "invalid_request", "anthropic-beta is not supported")
		return
	}
	var request messagesRequest
	if !decodeInferenceRequest(output, r, &request) {
		return
	}
	chat, err := request.chat()
	if err != nil {
		writeError(output, 400, "invalid_request", err.Error())
		return
	}
	copyRequest := r.Clone(r.Context())
	if key := r.Header.Get("x-api-key"); key != "" {
		if auth := bearerToken(r.Header.Get("Authorization")); auth != "" && auth != key {
			writeError(output, 400, "invalid_request", "conflicting authentication headers")
			return
		}
		copyRequest.Header.Set("Authorization", "Bearer "+key)
	}
	output.model = chat.Model
	h.serveChatAs(output, copyRequest, chat, "messages")
}

type messagesWriter struct {
	destination       http.ResponseWriter
	headers           http.Header
	status            int
	buffer            bytes.Buffer
	started, terminal bool
	model             string
	serviceTier       string
	finishReason      string
	stopSequence      *string
	usage             openai.Usage
	tools             map[int]int
	reasoning         map[int]int
	reasoningTypes    map[int]string
	toolArguments     [128]strings.Builder
	argumentBytes     int
	blocks            int
	textBlock         *int
	err               error
	directResponse    *openai.ChatCompletionResponse
	contextManagement json.RawMessage
}

func (w *messagesWriter) Header() http.Header    { return w.headers }
func (w *messagesWriter) WriteHeader(status int) { w.status = status }
func (w *messagesWriter) Flush()                 {}
func (w *messagesWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.buffer.Len()+len(data) > 32<<20 {
		w.err = errors.New("message response exceeds limit")
		return 0, w.err
	}
	w.buffer.Write(data)
	if w.headers.Get("Content-Type") == "text/event-stream" {
		for {
			raw := w.buffer.Bytes()
			end := bytes.Index(raw, []byte("\n\n"))
			if end < 0 {
				break
			}
			event := string(w.buffer.Next(end + 2))
			payload := strings.TrimSpace(strings.TrimPrefix(event, "data:"))
			if err := w.chunk(payload); err != nil {
				w.err = err
				return 0, err
			}
		}
	}
	return len(data), nil
}
func (w *messagesWriter) copyHeaders() {
	for key, values := range w.headers {
		w.destination.Header()[key] = append([]string(nil), values...)
	}
	w.destination.Header().Set("request-id", w.destination.Header().Get("X-Request-ID"))
}
func (w *messagesWriter) event(kind string, data map[string]any) error {
	if !w.started {
		w.copyHeaders()
		writeStreamHeaders(w.destination)
		w.destination.WriteHeader(http.StatusOK)
		w.started = true
	}
	data["type"] = kind
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return writeSSEResponseEvent(w.destination, kind, string(payload))
}
func messagesUsage(usage openai.Usage, serviceTier ...string) map[string]any {
	read, write := 0, 0
	if details := usage.PromptTokensDetails; details != nil {
		read = details.CachedTokens
		write = details.CacheWriteTokens
		if write == 0 {
			write = details.CacheCreationTokens
		}
	}
	input := usage.PromptTokens - read - write
	if input < 0 {
		input = 0
	}
	result := map[string]any{"input_tokens": input, "output_tokens": usage.CompletionTokens, "cache_read_input_tokens": read, "cache_creation_input_tokens": write}
	if len(serviceTier) > 0 && serviceTier[0] != "" {
		result["service_tier"] = serviceTier[0]
	}
	if usage.InferenceGeo != "" {
		result["inference_geo"] = usage.InferenceGeo
	}
	if details := usage.CompletionTokensDetails; details != nil {
		result["output_tokens_details"] = map[string]int{"thinking_tokens": details.ReasoningTokens}
	}
	return result
}

func validMessagesServiceTier(value string) bool {
	return value == "" || value == "standard" || value == "priority" || value == "batch"
}
func messagesStop(reason string) (string, error) {
	switch reason {
	case "stop":
		return "end_turn", nil
	case "length":
		return "max_tokens", nil
	case "tool_calls":
		return "tool_use", nil
	case "content_filter":
		return "refusal", nil
	case "pause_turn":
		return "pause_turn", nil
	default:
		return "", errors.New("unsupported completion finish reason")
	}
}
func messagesContent(message openai.Message) ([]any, error) {
	if len(message.NativeContent) > 0 {
		if len(message.NativeContent) > 128 {
			return nil, errors.New("too many native content blocks")
		}
		content := make([]any, 0, len(message.NativeContent))
		total := 0
		for _, raw := range message.NativeContent {
			if len(raw) > 4<<20 || total > (32<<20)-len(raw) {
				return nil, errors.New("native content exceeds limit")
			}
			total += len(raw)
			var block map[string]any
			if err := json.Unmarshal(raw, &block); err != nil || block == nil {
				return nil, errors.New("invalid native content block")
			}
			if err := validateNativeMessageBlock(block); err != nil {
				return nil, err
			}
			content = append(content, block)
		}
		return content, nil
	}
	content := []any{}
	if err := openai.ValidateReasoningBlocks(message.Reasoning); err != nil {
		return nil, err
	}
	for _, block := range message.Reasoning {
		if block.Type == "thinking" {
			content = append(content, map[string]any{"type": block.Type, "thinking": block.Thinking, "signature": block.Signature})
		} else {
			content = append(content, map[string]any{"type": block.Type, "data": block.Data})
		}
	}
	if text := openai.ContentText(message.Content); text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, call := range message.ToolCalls {
		if call.ExtraContent != nil || call.ID == "" || call.Function.Name == "" || call.Type != "function" {
			return nil, errors.New("tool metadata cannot be represented in Messages")
		}
		var input map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil || input == nil {
			return nil, errors.New("invalid tool arguments")
		}
		block := map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input}
		if call.ToolsetName != "" {
			members := clientToolsetMembers(call.ToolsetName)
			if members == nil || !members[call.Function.Name] {
				return nil, errors.New("invalid client toolset identity")
			}
			block["toolset_name"] = call.ToolsetName
		}
		content = append(content, block)
	}
	return content, nil
}

func validateNativeMessageBlock(block map[string]any) error {
	typeName, _ := block["type"].(string)
	stringField := func(name string) bool {
		value, ok := block[name].(string)
		return ok && value != ""
	}
	switch typeName {
	case "text":
		if _, ok := block["text"].(string); !ok {
			return errors.New("native text block requires text")
		}
		if citations, found := block["citations"]; found {
			if _, ok := citations.([]any); !ok {
				return errors.New("native text citations must be an array")
			}
		}
	case "thinking":
		if !stringField("thinking") || !stringField("signature") {
			return errors.New("native thinking block requires thinking and signature")
		}
	case "redacted_thinking":
		if !stringField("data") {
			return errors.New("native redacted thinking block requires data")
		}
	case "tool_use", "server_tool_use":
		if !stringField("id") || !stringField("name") || block["input"] == nil {
			return errors.New("native tool block requires id, name and input")
		}
		if _, ok := block["input"].(map[string]any); !ok {
			return errors.New("native tool input must be an object")
		}
		if toolset, found := block["toolset_name"]; found {
			name, _ := block["name"].(string)
			toolsetName, _ := toolset.(string)
			members := clientToolsetMembers(toolsetName)
			if members == nil || !members[name] || typeName != "tool_use" {
				return errors.New("invalid native client toolset identity")
			}
		}
	case "web_search_tool_result", "web_fetch_tool_result", "code_execution_tool_result", "bash_code_execution_tool_result", "text_editor_code_execution_tool_result":
		if !stringField("tool_use_id") || block["content"] == nil {
			return errors.New("native tool result requires tool_use_id and content")
		}
	default:
		return errors.New("unsupported native content block")
	}
	return nil
}

func (w *messagesWriter) chatResult(response openai.ChatCompletionResponse, stream bool) {
	if !stream {
		w.directResponse = &response
		return
	}
	if err := w.streamResult(response); err != nil {
		w.err = err
	}
}

func (w *messagesWriter) streamResult(response openai.ChatCompletionResponse) error {
	if !validMessagesUsage(response.Usage) || !validMessagesServiceTier(response.ServiceTier) || len(response.Choices) != 1 {
		return errors.New("invalid message response")
	}
	content, err := messagesContent(response.Choices[0].Message)
	if err != nil {
		return err
	}
	reason, err := messagesStop(response.Choices[0].FinishReason)
	if err != nil {
		return err
	}
	if response.Choices[0].StopSequence != nil {
		reason = "stop_sequence"
	}
	startUsage := messagesUsage(response.Usage, response.ServiceTier)
	startUsage["output_tokens"] = 0
	message := map[string]any{"id": response.ID, "type": "message", "role": "assistant", "model": response.Model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": startUsage}
	if container, err := messagesNativeContainer(response.NativeContainer); err != nil {
		return err
	} else if container != nil {
		message["container"] = container
	}
	if err := w.event("message_start", map[string]any{"message": message}); err != nil {
		return err
	}
	for index, value := range content {
		block, ok := value.(map[string]any)
		if !ok {
			return errors.New("invalid native content block")
		}
		start := cloneMessageBlock(block)
		typeName, _ := block["type"].(string)
		switch typeName {
		case "text":
			text, _ := block["text"].(string)
			delete(start, "citations")
			start["text"] = ""
			if err := w.event("content_block_start", map[string]any{"index": index, "content_block": start}); err != nil {
				return err
			}
			if text != "" {
				if err := w.event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "text_delta", "text": text}}); err != nil {
					return err
				}
			}
			if citations, ok := block["citations"].([]any); ok {
				for _, citation := range citations {
					if err := w.event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "citations_delta", "citation": citation}}); err != nil {
						return err
					}
				}
			}
		case "thinking":
			thinking, _ := block["thinking"].(string)
			signature, _ := block["signature"].(string)
			start["thinking"] = ""
			delete(start, "signature")
			if err := w.event("content_block_start", map[string]any{"index": index, "content_block": start}); err != nil {
				return err
			}
			if thinking != "" {
				if err := w.event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "thinking_delta", "thinking": thinking}}); err != nil {
					return err
				}
			}
			if signature != "" {
				if err := w.event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "signature_delta", "signature": signature}}); err != nil {
					return err
				}
			}
		case "tool_use":
			input := block["input"]
			start["input"] = map[string]any{}
			if err := w.event("content_block_start", map[string]any{"index": index, "content_block": start}); err != nil {
				return err
			}
			encoded, marshalErr := json.Marshal(input)
			if marshalErr != nil {
				return marshalErr
			}
			if err := w.event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(encoded)}}); err != nil {
				return err
			}
		default:
			if err := w.event("content_block_start", map[string]any{"index": index, "content_block": start}); err != nil {
				return err
			}
		}
		if err := w.event("content_block_stop", map[string]any{"index": index}); err != nil {
			return err
		}
	}
	delta := map[string]any{"delta": map[string]any{"stop_reason": reason, "stop_sequence": response.Choices[0].StopSequence}, "usage": messagesUsage(response.Usage, response.ServiceTier)}
	if len(response.NativeContextManagement) > 0 {
		contextManagement, err := messagesNativeContextManagement(response.NativeContextManagement)
		if err != nil {
			return err
		}
		delta["context_management"] = contextManagement
	}
	if err := w.event("message_delta", delta); err != nil {
		return err
	}
	w.terminal = true
	return w.event("message_stop", map[string]any{})
}

func cloneMessageBlock(block map[string]any) map[string]any {
	result := make(map[string]any, len(block))
	for key, value := range block {
		result[key] = value
	}
	return result
}
func (w *messagesWriter) chunk(payload string) error {
	if w.terminal {
		return nil
	}
	if payload == "[DONE]" {
		if w.finishReason == "" {
			return errors.New("Messages stream ended without a finish reason")
		}
		for callIndex := range w.tools {
			if arguments := w.toolArguments[callIndex].String(); arguments != "" {
				var input map[string]any
				if err := json.Unmarshal([]byte(arguments), &input); err != nil || input == nil {
					return errors.New("invalid completed tool arguments")
				}
			}
		}
		for index := 0; index < w.blocks; index++ {
			if err := w.event("content_block_stop", map[string]any{"index": index}); err != nil {
				return err
			}
		}
		delta := map[string]any{"delta": map[string]any{"stop_reason": w.finishReason, "stop_sequence": w.stopSequence}, "usage": messagesUsage(w.usage, w.serviceTier)}
		if len(w.contextManagement) > 0 {
			contextManagement, err := messagesNativeContextManagement(w.contextManagement)
			if err != nil {
				return err
			}
			delta["context_management"] = contextManagement
		}
		if err := w.event("message_delta", delta); err != nil {
			return err
		}
		w.terminal = true
		return w.event("message_stop", map[string]any{})
	}
	var chunk struct {
		ID          string `json:"id"`
		Model       string `json:"model"`
		ServiceTier string `json:"service_tier"`
		Choices     []struct {
			Index        int            `json:"index"`
			Delta        openai.Message `json:"delta"`
			Finish       string         `json:"finish_reason"`
			StopSequence *string        `json:"stop_sequence"`
		} `json:"choices"`
		Usage *openai.Usage   `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return err
	}
	if len(chunk.Error) > 0 {
		w.terminal = true
		return w.event("error", map[string]any{"error": map[string]any{"type": "api_error", "message": "inference stream failed"}})
	}
	if chunk.Usage != nil {
		if !validMessagesUsage(*chunk.Usage) {
			return errors.New("invalid message usage")
		}
		w.usage = *chunk.Usage
	}
	if chunk.ServiceTier != "" {
		if !validMessagesServiceTier(chunk.ServiceTier) || (w.serviceTier != "" && w.serviceTier != chunk.ServiceTier) {
			return errors.New("invalid or inconsistent message service tier")
		}
		w.serviceTier = chunk.ServiceTier
	}
	if !w.started {
		if chunk.Model != "" {
			w.model = chunk.Model
		}
		if err := w.event("message_start", map[string]any{"message": map[string]any{"id": chunk.ID, "type": "message", "role": "assistant", "model": w.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": messagesUsage(w.usage, w.serviceTier)}}); err != nil {
			return err
		}
	}
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			return errors.New("Messages requires one completion choice")
		}
		if choice.Finish != "" {
			reason, err := messagesStop(choice.Finish)
			if err != nil {
				return err
			}
			w.finishReason = reason
		}
		if choice.StopSequence != nil {
			w.stopSequence = choice.StopSequence
			w.finishReason = "stop_sequence"
		}
		if text := openai.ContentText(choice.Delta.Content); text != "" {
			if w.textBlock == nil {
				index := w.blocks
				w.blocks++
				w.textBlock = &index
				if err := w.event("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "text", "text": ""}}); err != nil {
					return err
				}
			}
			if err := w.event("content_block_delta", map[string]any{"index": *w.textBlock, "delta": map[string]any{"type": "text_delta", "text": text}}); err != nil {
				return err
			}
		}
		for _, call := range choice.Delta.ToolCalls {
			if call.Index == nil || *call.Index < 0 || *call.Index >= 128 || call.ExtraContent != nil {
				return errors.New("invalid or unsupported tool delta")
			}
			index, found := w.tools[*call.Index]
			if !found {
				if call.ID == "" || call.Function.Name == "" {
					return errors.New("tool delta must start with id and name")
				}
				index = w.blocks
				w.blocks++
				w.tools[*call.Index] = index
				block := map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": map[string]any{}}
				if call.ToolsetName != "" {
					members := clientToolsetMembers(call.ToolsetName)
					if members == nil || !members[call.Function.Name] {
						return errors.New("invalid client toolset identity")
					}
					block["toolset_name"] = call.ToolsetName
				}
				if err := w.event("content_block_start", map[string]any{"index": index, "content_block": block}); err != nil {
					return err
				}
			} else if call.Function.Name != "" || call.ID != "" {
				return errors.New("tool identity must be sent in the first delta")
			}
			if call.Function.Arguments != "" {
				if len(call.Function.Arguments) > (32<<20)-w.argumentBytes {
					return errors.New("tool arguments exceed limit")
				}
				w.argumentBytes += len(call.Function.Arguments)
				w.toolArguments[*call.Index].WriteString(call.Function.Arguments)
				if err := w.event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": call.Function.Arguments}}); err != nil {
					return err
				}
			}
		}
		for _, block := range choice.Delta.Reasoning {
			if block.Index == nil || *block.Index < 0 || *block.Index >= 128 || (block.Type != "thinking" && block.Type != "redacted_thinking") {
				return errors.New("invalid reasoning delta")
			}
			if w.reasoning == nil {
				w.reasoning = map[int]int{}
				w.reasoningTypes = map[int]string{}
			}
			index, found := w.reasoning[*block.Index]
			if !found {
				index = w.blocks
				w.blocks++
				w.reasoning[*block.Index] = index
				w.reasoningTypes[*block.Index] = block.Type
				content := map[string]any{"type": block.Type}
				if block.Type == "thinking" {
					content["thinking"] = ""
				} else {
					if block.Data == "" {
						return errors.New("redacted reasoning block requires data")
					}
					content["data"] = block.Data
				}
				if err := w.event("content_block_start", map[string]any{"index": index, "content_block": content}); err != nil {
					return err
				}
			} else if w.reasoningTypes[*block.Index] != block.Type || block.Data != "" {
				return errors.New("inconsistent reasoning delta")
			}
			if block.Thinking != "" {
				if err := w.event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "thinking_delta", "thinking": block.Thinking}}); err != nil {
					return err
				}
			}
			if block.Signature != "" {
				if err := w.event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "signature_delta", "signature": block.Signature}}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (w *messagesWriter) finish() {
	if w.started {
		if !w.terminal {
			_ = w.event("error", map[string]any{"error": map[string]any{"type": "api_error", "message": "inference stream did not complete"}})
		}
		return
	}
	w.copyHeaders()
	if w.err != nil {
		w.status = http.StatusBadGateway
	}
	if w.status >= 400 {
		kind := "api_error"
		switch w.status {
		case 400:
			kind = "invalid_request_error"
		case 401:
			kind = "authentication_error"
		case 403, 451:
			kind = "permission_error"
		case 404:
			kind = "not_found_error"
		case 413:
			kind = "request_too_large"
		case 429:
			kind = "rate_limit_error"
		case 503:
			kind = "overloaded_error"
		}
		message := "request failed"
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if w.status < 500 && json.Unmarshal(w.buffer.Bytes(), &body) == nil && body.Error.Message != "" {
			message = body.Error.Message
		}
		writeJSON(w.destination, w.status, map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
		return
	}
	var response openai.ChatCompletionResponse
	var err error
	if w.directResponse != nil {
		response = *w.directResponse
	} else {
		err = json.Unmarshal(w.buffer.Bytes(), &response)
	}
	var payload map[string]any
	if err == nil {
		payload, err = messagesResponsePayload(response)
	}
	if err != nil {
		writeJSON(w.destination, 502, map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": "provider response cannot be represented as Messages"}})
		return
	}
	writeJSON(w.destination, 200, payload)
}

func messagesResponsePayload(response openai.ChatCompletionResponse) (map[string]any, error) {
	if !validMessagesUsage(response.Usage) || !validMessagesServiceTier(response.ServiceTier) || len(response.Choices) != 1 {
		return nil, errors.New("invalid message response")
	}
	content, err := messagesContent(response.Choices[0].Message)
	if err != nil {
		return nil, err
	}
	reason, err := messagesStop(response.Choices[0].FinishReason)
	if err != nil {
		return nil, err
	}
	if response.Choices[0].StopSequence != nil {
		reason = "stop_sequence"
	}
	payload := map[string]any{"id": response.ID, "type": "message", "role": "assistant", "model": response.Model, "content": content, "stop_reason": reason, "stop_sequence": response.Choices[0].StopSequence, "usage": messagesUsage(response.Usage, response.ServiceTier)}
	if container, err := messagesNativeContainer(response.NativeContainer); err != nil {
		return nil, err
	} else if container != nil {
		payload["container"] = container
	}
	if len(response.NativeContextManagement) > 0 {
		contextManagement, err := messagesNativeContextManagement(response.NativeContextManagement)
		if err != nil {
			return nil, err
		}
		payload["context_management"] = contextManagement
	}
	return payload, nil
}

func messagesNativeContainer(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("invalid native container")
	}
	var container struct {
		ID        string                           `json:"id"`
		ExpiresAt string                           `json:"expires_at,omitempty"`
		Skills    []openai.AnthropicSkillReference `json:"skills,omitempty"`
	}
	if decodeMessagesValue(raw, &container) != nil || !validSkillID(container.ID) || len(container.ID) > 128 || len(container.Skills) > 20 {
		return nil, errors.New("invalid native container")
	}
	for _, skill := range container.Skills {
		if (skill.Type != "anthropic" && skill.Type != "custom") || !validSkillID(skill.SkillID) || len(skill.SkillID) > 64 || !validSkillID(skill.Version) || len(skill.Version) > 64 {
			return nil, errors.New("invalid native container")
		}
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil {
		return nil, errors.New("invalid native container")
	}
	return result, nil
}

func validMessagesUsage(usage openai.Usage) bool {
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 {
		return false
	}
	if details := usage.CompletionTokensDetails; details != nil && (details.ReasoningTokens < 0 || details.ReasoningTokens > usage.CompletionTokens) {
		return false
	}
	if details := usage.PromptTokensDetails; details != nil {
		write := details.CacheWriteTokens
		if write == 0 {
			write = details.CacheCreationTokens
		}
		return details.CachedTokens >= 0 && write >= 0 && details.CachedTokens <= usage.PromptTokens && write <= usage.PromptTokens-details.CachedTokens
	}
	return true
}

// Native adapters can report final usage only in their accumulated response.
func (w *messagesWriter) chatStreamResult(response openai.ChatCompletionResponse) {
	if !validMessagesUsage(response.Usage) {
		w.err = errors.New("invalid message usage")
		return
	}
	if _, err := messagesNativeContextManagement(response.NativeContextManagement); err != nil {
		w.err = err
		return
	}
	w.usage = response.Usage
	w.contextManagement = append(w.contextManagement[:0], response.NativeContextManagement...)
	if len(response.Choices) == 1 && response.Choices[0].StopSequence != nil {
		w.stopSequence = response.Choices[0].StopSequence
		w.finishReason = "stop_sequence"
	}
}
