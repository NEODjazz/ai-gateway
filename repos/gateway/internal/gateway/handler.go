package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type Handler struct {
	pipeline      modules.Pipeline
	provider      provider.Provider
	rateLimits    RateLimitStore
	metrics       *Metrics
	ready         func(context.Context) error
	management    ManagementClient
	directory     IdentityDirectoryClient
	organizations OrganizationDirectoryClient
	dlp           modules.Module
	av            modules.Module
	guardrails    *GuardrailMonitor
	cacheConfig   CacheRuntimeConfig
	logging       *LoggingRegistry
	agents        *AgentRegistry
	mcp           *MCPRegistry
	access        *AccessRegistry
	budgets       BudgetManagementClient
	usage         UsageManagementClient
	requestLogs   RequestLogClient
	models        *modelcatalog.Registry
	audit         AuditClient
	apiDocs       apiDocsConfig
	adminUI       bool
	adminState    *AdminStateRuntime
}

func (h Handler) WithAdminState(runtime *AdminStateRuntime) Handler {
	h.adminState = runtime
	return h
}

// WithAPIDocs enables the embedded API documentation. Interactive requests are
// controlled separately so operators can expose read-only documentation.
func (h Handler) WithAPIDocs(tryItOutEnabled bool) Handler {
	h.apiDocs = apiDocsConfig{enabled: true, tryItOutEnabled: tryItOutEnabled}
	return h
}

// WithAdminUI enables the embedded management console. All data APIs remain
// protected by the normal admin bearer authentication and RBAC pipeline.
func (h Handler) WithAdminUI() Handler {
	h.adminUI = true
	return h
}

func NewHandler(pipeline modules.Pipeline, llmProvider provider.Provider) Handler {
	return NewHandlerWithRateLimitStore(pipeline, llmProvider, NewMemoryRateLimitStore())

}

func NewHandlerWithRateLimitStore(pipeline modules.Pipeline, llmProvider provider.Provider, rateLimits RateLimitStore) Handler {
	return NewHandlerWithReadiness(pipeline, llmProvider, rateLimits, nil)
}

func NewHandlerWithReadiness(pipeline modules.Pipeline, llmProvider provider.Provider, rateLimits RateLimitStore, ready func(context.Context) error) Handler {
	return NewHandlerWithMetrics(pipeline, llmProvider, rateLimits, ready, NewMetrics())
}

func NewHandlerWithMetrics(pipeline modules.Pipeline, llmProvider provider.Provider, rateLimits RateLimitStore, ready func(context.Context) error, metrics *Metrics) Handler {
	if rateLimits == nil {
		rateLimits = NewMemoryRateLimitStore()
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return Handler{pipeline: pipeline, provider: llmProvider, rateLimits: rateLimits, metrics: metrics, ready: ready}
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
		SessionID: sessionID(r),
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

	models := filterModels(h.provider.Models(), reqCtx.AllowedModels)
	if h.access != nil {
		filtered := models[:0]
		for _, model := range models {
			if allowed, _ := h.access.TagModelAllowed(reqCtx.Tags, model.ID); allowed {
				filtered = append(filtered, model)
			}
		}
		models = filtered
	}
	writeJSON(w, http.StatusOK, openai.ModelsResponse{Object: "list", Data: models})
}

func (h Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	var request openai.ChatCompletionRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if request.MaxTokens != nil && request.MaxCompletionTokens != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "max_tokens and max_completion_tokens are mutually exclusive")
		return
	}

	stream := request.Stream
	reqCtx := modules.RequestContext{
		APIKey:    bearerToken(r.Header.Get("Authorization")),
		RequestID: requestID(r),
		SessionID: sessionID(r),
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
	if _, err := openai.ChatImageAttachments(reqCtx.Request.Messages); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_image", err.Error())
		return
	}
	toolIdentifiers, validTools := chatToolIdentifiers(request.Tools)
	if !h.authorizeTools(w, reqCtx, toolIdentifiers, validTools) {
		return
	}
	if !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateChatTokens(request)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
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
	if !decodeInferenceRequest(w, r, &request) {
		return
	}

	reqCtx := modules.RequestContext{
		APIKey:          bearerToken(r.Header.Get("Authorization")),
		RequestID:       requestID(r),
		SessionID:       sessionID(r),
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
	if _, err := openai.ResponseImageAttachments(reqCtx.ResponseRequest.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_image", err.Error())
		return
	}
	toolIdentifiers, validTools := responseToolIdentifiers(request.Tools)
	if !h.authorizeTools(w, reqCtx, toolIdentifiers, validTools) {
		return
	}
	if !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateResponseTokens(request)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
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

func (h Handler) Embeddings(w http.ResponseWriter, r *http.Request) {
	var request openai.EmbeddingRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.Model) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	if _, ok := openai.EmbeddingInputStrings(request.Input); !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "input must be a non-empty string or array of non-empty strings; token arrays are not supported")
		return
	}
	if request.EncodingFormat != "" && request.EncodingFormat != "float" {
		writeError(w, http.StatusBadRequest, "invalid_request", "only encoding_format=float is supported")
		return
	}
	if request.Dimensions != nil && *request.Dimensions <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "dimensions must be positive")
		return
	}

	reqCtx := modules.RequestContext{
		APIKey:           bearerToken(r.Header.Get("Authorization")),
		RequestID:        requestID(r),
		SessionID:        sessionID(r),
		EmbeddingRequest: &request,
		Request: openai.ChatCompletionRequest{
			Provider: request.Provider,
			Model:    request.Model,
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
	if !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateEmbeddingTokens(request)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	embeddingProvider, ok := h.provider.(provider.EmbeddingProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "embeddings are not supported by the configured provider")
		return
	}
	response, err := embeddingProvider.Embeddings(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) Rerank(w http.ResponseWriter, r *http.Request) {
	var request openai.RerankRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if message := validateRerankRequest(request); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: requestID(r), SessionID: sessionID(r), RerankRequest: &request,
		Request: openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model},
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
	if !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateRerankTokens(request)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	rerankProvider, ok := h.provider.(provider.RerankProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "rerank is not supported by the configured provider")
		return
	}
	response, err := rerankProvider.Rerank(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func validateRerankRequest(request openai.RerankRequest) string {
	if strings.TrimSpace(request.Model) == "" {
		return "model is required"
	}
	if strings.TrimSpace(request.Query) == "" {
		return "query is required"
	}
	if len(request.Documents) == 0 || len(request.Documents) > 1000 {
		return "documents must contain between 1 and 1000 items"
	}
	if request.TopN != nil && (*request.TopN <= 0 || *request.TopN > len(request.Documents)) {
		return "top_n must be between 1 and the number of documents"
	}
	if request.MaxChunksPerDoc != nil && *request.MaxChunksPerDoc <= 0 {
		return "max_chunks_per_doc must be positive"
	}
	if request.MaxTokensPerDoc != nil && *request.MaxTokensPerDoc <= 0 {
		return "max_tokens_per_doc must be positive"
	}
	if len(request.RankFields) > 32 {
		return "rank_fields must not contain more than 32 fields"
	}
	seen := map[string]bool{}
	for _, field := range request.RankFields {
		field = strings.TrimSpace(field)
		if field == "" || seen[field] {
			return "rank_fields must contain unique non-empty fields"
		}
		seen[field] = true
	}
	text, ok := openai.RerankDocumentText(request)
	if !ok {
		return "documents must be non-empty strings or objects containing text in rank_fields (default: text)"
	}
	if len(text) > 4<<20 {
		return "query and document text exceed the rerank limit"
	}
	return ""
}

func decodeInferenceRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, openai.MaxInferenceBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the inference limit")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain exactly one JSON value")
		return false
	}
	return true
}

func writeProviderFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, modules.ErrContentRejected) {
		writeError(w, http.StatusUnavailableForLegalReasons, "content_rejected", "content rejected")
		return
	}
	if errors.Is(err, modules.ErrGuardrailUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "guardrail_unavailable", "required content policy service is unavailable")
		return
	}
	if errors.Is(err, modules.ErrBudgetExceeded) {
		writeError(w, http.StatusTooManyRequests, "budget_exceeded", "budget exceeded")
		return
	}
	if errors.Is(err, modules.ErrBillingConflict) {
		writeError(w, http.StatusConflict, "billing_conflict", "billing lifecycle conflict")
		return
	}
	var admissionErr *provider.AdmissionError
	if errors.As(err, &admissionErr) {
		seconds := int((admissionErr.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests, "provider_busy", "provider capacity is temporarily exhausted")
		return
	}
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		switch providerErr.Class {
		case provider.FailureClientRequest:
			code := providerErr.UpstreamCode
			if code == "" {
				code = "provider_invalid_request"
			}
			message := "provider rejected the request"
			if providerErr.Param != "" {
				message = "provider rejected parameter " + providerErr.Param
			}
			writeProviderParameterError(w, providerErr.StatusCode, code, message, providerErr.Param)
			return
		case provider.FailureContextLength:
			code := providerErr.UpstreamCode
			if code == "" {
				code = "context_length_exceeded"
			}
			writeProviderParameterError(w, http.StatusBadRequest, code, "request exceeds the model context window", providerErr.Param)
			return
		case provider.FailureContentPolicy:
			writeError(w, http.StatusUnavailableForLegalReasons, "provider_content_policy", "upstream provider rejected the request under its content policy")
			return
		}
	}
	writeError(w, http.StatusBadGateway, "provider_failed", err.Error())
}

func writeProviderParameterError(w http.ResponseWriter, status int, code, message, param string) {
	if status < 400 || status >= 500 {
		status = http.StatusBadRequest
	}
	detail := map[string]any{"code": code, "message": message}
	if param != "" {
		detail["param"] = param
	}
	writeJSON(w, status, map[string]any{"error": detail, "ts": time.Now().UTC().Format(time.RFC3339)})
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

func sessionID(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("X-Session-ID"))
	if value != "" && len(value) <= 128 {
		return value
	}
	return ""
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
