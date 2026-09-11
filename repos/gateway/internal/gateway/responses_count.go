package gateway

import (
	"errors"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func (h Handler) CountResponseInputTokens(w http.ResponseWriter, r *http.Request) {
	var request openai.ResponseInputTokenCountRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	responseRequest := request.ResponseRequest()
	if strings.TrimSpace(responseRequest.Model) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	if responseRequest.Input == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "input is required")
		return
	}
	if message := responseRequest.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}

	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		ResponseRequest: &responseRequest,
		Request: openai.ChatCompletionRequest{
			Provider: responseRequest.Provider, Model: responseRequest.Model, Messages: responseMessages(responseRequest),
		},
	}
	if err := h.pipeline.RunTokenCount(r.Context(), &reqCtx); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
		} else {
			writeProviderFailure(w, err)
		}
		return
	}
	reqCtx.APIKey = ""
	if reqCtx.ResponseRequest == nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed response token-count request")
		return
	}
	responseRequest = *reqCtx.ResponseRequest
	if !h.prepareAccessGroups(w, &reqCtx) {
		return
	}
	if _, err := openai.ResponseImageAttachments(responseRequest.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_image", err.Error())
		return
	}
	if _, err := openai.ResponseAudioAttachments(responseRequest.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_audio", err.Error())
		return
	}
	if _, err := openai.ResponseFileAttachments(responseRequest.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_file", err.Error())
		return
	}
	toolIdentifiers, validTools := responseToolIdentifiers(responseRequest.Tools)
	if !h.authorizeTools(w, reqCtx, toolIdentifiers, validTools) ||
		!h.authorizeAccess(w, r.Context(), reqCtx, responseRequest.Model, openai.ResponseInputTokens(responseRequest)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, responseRequest.Model) {
		return
	}
	counter, ok := h.provider.(provider.ResponseInputTokenCountProvider)
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported_operation", "response input token counting is not supported by the configured provider")
		return
	}
	result, err := counter.CountResponseInputTokens(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
