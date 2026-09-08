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
	h.serveChat(output, copyRequest, chat)
}

type messagesWriter struct {
	destination       http.ResponseWriter
	headers           http.Header
	status            int
	buffer            bytes.Buffer
	started, terminal bool
	model             string
	finishReason      string
	stopSequence      *string
	usage             openai.Usage
	tools             map[int]int
	toolArguments     [128]strings.Builder
	argumentBytes     int
	blocks            int
	textBlock         *int
	err               error
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
func messagesUsage(usage openai.Usage) map[string]int {
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
	return map[string]int{"input_tokens": input, "output_tokens": usage.CompletionTokens, "cache_read_input_tokens": read, "cache_creation_input_tokens": write}
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
	default:
		return "", errors.New("unsupported completion finish reason")
	}
}
func messagesContent(message openai.Message) ([]any, error) {
	content := []any{}
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
		content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input})
	}
	return content, nil
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
		if err := w.event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": w.finishReason, "stop_sequence": w.stopSequence}, "usage": messagesUsage(w.usage)}); err != nil {
			return err
		}
		w.terminal = true
		return w.event("message_stop", map[string]any{})
	}
	var chunk struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
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
	if !w.started {
		if chunk.Model != "" {
			w.model = chunk.Model
		}
		if err := w.event("message_start", map[string]any{"message": map[string]any{"id": chunk.ID, "type": "message", "role": "assistant", "model": w.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": messagesUsage(w.usage)}}); err != nil {
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
				if err := w.event("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": map[string]any{}}}); err != nil {
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
	err := json.Unmarshal(w.buffer.Bytes(), &response)
	if !validMessagesUsage(response.Usage) {
		err = errors.New("invalid message usage")
	}
	var content []any
	var reason string
	if err == nil && len(response.Choices) == 1 {
		content, err = messagesContent(response.Choices[0].Message)
		if err == nil {
			reason, err = messagesStop(response.Choices[0].FinishReason)
			if response.Choices[0].StopSequence != nil {
				reason = "stop_sequence"
			}
		}
	} else {
		err = errors.New("invalid message response")
	}
	if err != nil {
		writeJSON(w.destination, 502, map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": "provider response cannot be represented as Messages"}})
		return
	}
	writeJSON(w.destination, 200, map[string]any{"id": response.ID, "type": "message", "role": "assistant", "model": response.Model, "content": content, "stop_reason": reason, "stop_sequence": response.Choices[0].StopSequence, "usage": messagesUsage(response.Usage)})
}

func validMessagesUsage(usage openai.Usage) bool {
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 {
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
	w.usage = response.Usage
	if len(response.Choices) == 1 && response.Choices[0].StopSequence != nil {
		w.stopSequence = response.Choices[0].StopSequence
		w.finishReason = "stop_sequence"
	}
}
