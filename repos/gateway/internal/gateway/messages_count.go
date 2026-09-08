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
	req := modules.RequestContext{APIKey: key, RequestID: executionID(output), SessionID: sessionID(r), Request: request}
	if err := h.pipeline.RunTokenCount(r.Context(), &req); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(output, 401, "unauthorized", "invalid api key")
		} else {
			writeProviderFailure(output, err)
		}
		return
	}
	req.APIKey = ""
	if !h.prepareAccessGroups(output, &req) {
		return
	}
	if _, err := openai.ChatImageAttachments(req.Request.Messages); err != nil {
		writeError(output, 400, "invalid_image", err.Error())
		return
	}
	tools, valid := chatToolIdentifiers(request.Tools)
	if !h.authorizeTools(output, req, tools, valid) || !h.authorizeAccess(output, r.Context(), req, request.Model, openai.ChatInputTokens(request)) {
		return
	}
	if !h.prepareModelFallbacks(output, r.Context(), &req, request.Model) {
		return
	}
	counter, ok := h.provider.(provider.TokenCountProvider)
	if !ok {
		writeError(output, 400, "unsupported_parameter", "count_tokens is not supported")
		return
	}
	result, err := counter.CountTokens(r.Context(), req)
	if err != nil {
		writeProviderFailure(output, err)
		return
	}
	output.copyHeaders()
	writeJSON(w, 200, map[string]int{"input_tokens": result.InputTokens})
	completed = true
}
