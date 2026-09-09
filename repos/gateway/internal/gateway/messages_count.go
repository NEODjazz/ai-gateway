package gateway

import (
	"encoding/json"
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type messagesCountRequest struct {
	Model      string              `json:"model"`
	Messages   []messagesInput     `json:"messages"`
	System     json.RawMessage     `json:"system,omitempty"`
	Tools      []messagesTool      `json:"tools,omitempty"`
	ToolChoice *messagesToolChoice `json:"tool_choice,omitempty"`
}

func (h Handler) CountMessageTokens(w http.ResponseWriter, r *http.Request) {
	output := &messagesWriter{destination: w, headers: make(http.Header), status: http.StatusOK}
	completed := false
	defer func() {
		if !completed {
			output.finish()
		}
	}()
	if r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("anthropic-beta") != "" {
		writeError(output, 400, "invalid_request", "anthropic-version must be 2023-06-01; beta headers are unsupported")
		return
	}
	var native messagesCountRequest
	if !decodeInferenceRequest(output, r, &native) {
		return
	}
	request, err := (messagesRequest{Model: native.Model, MaxTokens: 1, Messages: native.Messages, System: native.System, Tools: native.Tools, ToolChoice: native.ToolChoice}).chatContext(true)
	if err != nil {
		writeError(output, 400, "invalid_request", err.Error())
		return
	}
	request.MaxTokens = nil
	key := bearerToken(r.Header.Get("Authorization"))
	if nativeKey := r.Header.Get("x-api-key"); nativeKey != "" {
		if key != "" && key != nativeKey {
			writeError(output, 400, "invalid_request", "conflicting authentication headers")
			return
		}
		key = nativeKey
	}
	count, ok := h.countContextTokens(output, r, request, key)
	if !ok {
		return
	}
	output.copyHeaders()
	writeJSON(w, 200, map[string]int{"input_tokens": count})
	completed = true
}

// countContextTokens applies the same admission and policy checks to each native
// counting protocol, without opening a generation billing lifecycle.
func (h Handler) countContextTokens(w http.ResponseWriter, r *http.Request, request openai.ChatCompletionRequest, key string) (int, bool) {
	req := modules.RequestContext{APIKey: key, RequestID: executionID(w), SessionID: sessionID(r), Request: request}
	if err := h.pipeline.RunTokenCount(r.Context(), &req); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, 401, "unauthorized", "invalid api key")
		} else {
			writeProviderFailure(w, err)
		}
		return 0, false
	}
	req.APIKey = ""
	if !h.prepareAccessGroups(w, &req) {
		return 0, false
	}
	if _, err := openai.ChatImageAttachments(req.Request.Messages); err != nil {
		writeError(w, 400, "invalid_image", err.Error())
		return 0, false
	}
	request = req.Request
	tools, valid := chatToolIdentifiers(request.Tools, nil)
	if !h.authorizeTools(w, req, tools, valid) || !h.authorizeAccess(w, r.Context(), req, request.Model, openai.ChatInputTokens(request)) {
		return 0, false
	}
	if !h.prepareModelFallbacks(w, r.Context(), &req, request.Model) {
		return 0, false
	}
	counter, ok := h.provider.(provider.TokenCountProvider)
	if !ok {
		writeError(w, 400, "unsupported_parameter", "count_tokens is not supported")
		return 0, false
	}
	result, err := counter.CountTokens(r.Context(), req)
	if err != nil {
		writeProviderFailure(w, err)
		return 0, false
	}
	if result.InputTokens < 0 {
		writeError(w, 502, "provider_error", "invalid provider token count")
		return 0, false
	}
	return result.InputTokens, true
}
