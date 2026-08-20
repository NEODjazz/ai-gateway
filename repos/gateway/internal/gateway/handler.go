package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type Handler struct {
	pipeline   modules.Pipeline
	provider   provider.Provider
	rateLimits RateLimitStore
	metrics    *Metrics
	ready      func(context.Context) error
}

func NewHandler(pipeline modules.Pipeline, llmProvider provider.Provider) Handler {
	return NewHandlerWithRateLimitStore(pipeline, llmProvider, NewMemoryRateLimitStore())

}

func NewHandlerWithRateLimitStore(pipeline modules.Pipeline, llmProvider provider.Provider, rateLimits RateLimitStore) Handler {
	return NewHandlerWithReadiness(pipeline, llmProvider, rateLimits, nil)
}

func NewHandlerWithReadiness(pipeline modules.Pipeline, llmProvider provider.Provider, rateLimits RateLimitStore, ready func(context.Context) error) Handler {
	if rateLimits == nil {
		rateLimits = NewMemoryRateLimitStore()
	}
	return Handler{pipeline: pipeline, provider: llmProvider, rateLimits: rateLimits, metrics: NewMetrics(), ready: ready}
}

func (h Handler) Health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) Ready(w http.ResponseWriter, r *http.Request) {
	if h.ready != nil {
		if err := h.ready(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "required storage is unavailable")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) Models(w http.ResponseWriter, r *http.Request) {
	reqCtx := modules.RequestContext{
		APIKey:    bearerToken(r.Header.Get("Authorization")),
		RequestID: requestID(r),
	}
	if err := h.pipeline.Run(r.Context(), &reqCtx); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return
		}
		writeError(w, http.StatusBadGateway, "module_failed", err.Error())
		return
	}
	reqCtx.APIKey = ""

	writeJSON(w, http.StatusOK, openai.ModelsResponse{
		Object: "list",
		Data:   filterModels(h.provider.Models(), reqCtx.AllowedModels),
	})
}

func (h Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	var request openai.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	stream := request.Stream
	reqCtx := modules.RequestContext{
		APIKey:    bearerToken(r.Header.Get("Authorization")),
		RequestID: requestID(r),
		Request:   request,
	}

	if err := h.pipeline.Run(r.Context(), &reqCtx); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return
		}
		writeError(w, http.StatusBadGateway, "module_failed", err.Error())
		return
	}
	reqCtx.APIKey = ""
	if !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateChatTokens(request)) {
		return
	}
	if stream {
		streamStarted := false
		writeStreamPayload := func(payload string) error {
			if !streamStarted {
				writeStreamHeaders(w)
				w.WriteHeader(http.StatusOK)
				streamStarted = true
			}
			return writeSSEPayload(w, payload)
		}
		if response, streamed, err := h.provider.StreamChatCompletions(r.Context(), reqCtx, writeStreamPayload); streamed {
			if err != nil {
				if streamStarted {
					_ = writeStreamPayload(errorStreamPayload(err))
					writeSSEDone(w)
					return
				}
				writeProviderFailure(w, err)
				return
			}
			_ = response
			writeSSEDone(w)
			return
		} else if err != nil {
			writeProviderFailure(w, err)
			return
		}
	}

	reqCtx.Request.Stream = false
	response, err := h.provider.ChatCompletions(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}

	if stream {
		writeChatCompletionStream(w, response)
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h Handler) Responses(w http.ResponseWriter, r *http.Request) {
	var request openai.ResponseRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	reqCtx := modules.RequestContext{
		APIKey:          bearerToken(r.Header.Get("Authorization")),
		RequestID:       requestID(r),
		ResponseRequest: &request,
		Request: openai.ChatCompletionRequest{
			Provider: request.Provider,
			Model:    request.Model,
			Messages: responseMessages(request),
		},
	}

	if err := h.pipeline.Run(r.Context(), &reqCtx); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return
		}
		writeError(w, http.StatusBadGateway, "module_failed", err.Error())
		return
	}
	reqCtx.APIKey = ""
	if !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateResponseTokens(request)) {
		return
	}
	if request.Stream {
		streamStarted := false
		writeStreamEvent := func(event string, payload string) error {
			if !streamStarted {
				writeStreamHeaders(w)
				w.WriteHeader(http.StatusOK)
				streamStarted = true
			}
			return writeSSEResponseEvent(w, event, payload)
		}
		if response, streamed, err := h.provider.StreamResponses(r.Context(), reqCtx, writeStreamEvent); streamed {
			if err != nil {
				if streamStarted {
					_ = writeStreamEvent("error", errorStreamPayload(err))
					writeSSEDone(w)
					return
				}
				writeProviderFailure(w, err)
				return
			}
			_ = response
			writeSSEDone(w)
			return
		} else if err != nil {
			writeProviderFailure(w, err)
			return
		}
	}

	reqCtx.ResponseRequest.Stream = false
	response, err := h.provider.Responses(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func writeProviderFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, modules.ErrBudgetExceeded) {
		writeError(w, http.StatusTooManyRequests, "budget_exceeded", "budget exceeded")
		return
	}
	if errors.Is(err, modules.ErrBillingConflict) {
		writeError(w, http.StatusConflict, "billing_conflict", "billing lifecycle conflict")
		return
	}
	writeError(w, http.StatusBadGateway, "provider_failed", err.Error())
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

func requestID(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); value != "" && len(value) <= 128 {
		return value
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UTC().UnixNano())
	}
	return fmt.Sprintf("%x", value[:])
}

func responseMessages(request openai.ResponseRequest) []openai.Message {
	var messages []openai.Message
	if request.Instructions != "" {
		messages = append(messages, openai.Message{Role: "system", Content: request.Instructions})
	}
	messages = append(messages, openai.Message{Role: "user", Content: responseInputText(request.Input)})
	return messages
}

func responseInputText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, responseInputText(item))
		}
		return strings.Join(parts, " ")
	case map[string]any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, responseInputText(item))
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
		"ts": time.Now().UTC().Format(time.RFC3339),
	})
}

func writeChatCompletionStream(w http.ResponseWriter, response openai.ChatCompletionResponse) {
	writeStreamHeaders(w)
	w.WriteHeader(http.StatusOK)

	for _, choice := range response.Choices {
		writeSSE(w, map[string]any{
			"id":      response.ID,
			"object":  "chat.completion.chunk",
			"model":   response.Model,
			"created": time.Now().UTC().Unix(),
			"choices": []map[string]any{
				{
					"index": choice.Index,
					"delta": map[string]any{
						"role":    choice.Message.Role,
						"content": openai.ContentText(choice.Message.Content),
					},
					"finish_reason": nil,
				},
			},
		})
		writeSSE(w, map[string]any{
			"id":      response.ID,
			"object":  "chat.completion.chunk",
			"model":   response.Model,
			"created": time.Now().UTC().Unix(),
			"choices": []map[string]any{
				{
					"index":         choice.Index,
					"delta":         map[string]any{},
					"finish_reason": choice.FinishReason,
				},
			},
		})
	}

	writeSSEDone(w)
}

func writeSSE(w http.ResponseWriter, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func writeSSEPayload(w http.ResponseWriter, payload string) error {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeSSEResponseEvent(w http.ResponseWriter, event string, payload string) error {
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	return writeSSEPayload(w, payload)
}

func writeSSEDone(w http.ResponseWriter) {
	_ = writeSSEPayload(w, "[DONE]")
}

func errorStreamPayload(err error) string {
	payload, marshalErr := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    "provider_failed",
			"message": err.Error(),
		},
	})
	if marshalErr != nil {
		return `{"error":{"code":"provider_failed","message":"stream failed"}}`
	}
	return string(payload)
}
