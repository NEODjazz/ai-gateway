package gateway

import (
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func (h Handler) ExecuteSandbox(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	var request openai.SandboxExecuteRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	request.ApplyDefaults()
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		InputCharacters: len(request.Code),
		SandboxRequest:  &request,
		Request: openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model,
			Messages: []openai.Message{{Role: "user", Content: request.Code}}},
		Metadata: map[string]string{"gateway.api_type": "sandbox"},
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
	if reqCtx.SandboxRequest == nil || len(reqCtx.Request.Messages) != 1 {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed sandbox request")
		return
	}
	request = *reqCtx.SandboxRequest
	request.Code = openai.ContentText(reqCtx.Request.Messages[0].Content)
	reqCtx.SandboxRequest = &request
	reqCtx.InputCharacters = len(request.Code)
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadGateway, "module_failed", "module returned an invalid sandbox request")
		return
	}
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, request.InputTokens()) || !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	sandbox, ok := h.provider.(provider.SandboxProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "sandbox execution is not supported")
		return
	}
	response, err := sandbox.ExecuteSandbox(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
