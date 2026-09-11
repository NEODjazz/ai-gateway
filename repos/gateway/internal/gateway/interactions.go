package gateway

import (
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func (h Handler) Interactions(w http.ResponseWriter, r *http.Request) {
	var request openai.InteractionRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if native, ok := h.provider.(provider.InteractionProvider); ok && native.CanRouteInteraction(r.Context(), request) {
		h.serveNativeInteraction(w, r, native, request)
		return
	}
	responseRequest, message := request.ResponseRequest()
	if message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	h.serveResponsesAs(w, r, responseRequest, "interactions", func(response openai.ResponseResponse, _ modules.RequestContext) any {
		return openai.InteractionFromResponse(response)
	}, func(event, payload string, _ modules.RequestContext) ([]responseStreamEvent, error) {
		return newInteractionStreamTransformer().Transform(event, payload)
	}, nil, false)
}

func (h Handler) serveNativeInteraction(w http.ResponseWriter, r *http.Request, native provider.InteractionProvider, request openai.InteractionRequest) {
	shared, message := request.NativeResponseRequest()
	if message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	if request.Stream || request.Background || request.PreviousInteractionID != "" || request.Store != nil && *request.Store {
		writeError(w, http.StatusBadRequest, "unsupported_operation", "native interaction persistence and streaming require lifecycle support")
		return
	}
	if _, err := openai.ResponseImageAttachments(shared.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_image", err.Error())
		return
	}
	if _, err := openai.ResponseAudioAttachments(shared.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_audio", err.Error())
		return
	}
	if _, err := openai.ResponseFileAttachments(shared.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_file", err.Error())
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		ResponseRequest: &shared,
		Request:         openai.ChatCompletionRequest{Provider: shared.Provider, Model: shared.Model, Messages: responseMessages(shared)},
		Metadata:        map[string]string{"gateway.api_type": "interactions"},
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
	if reqCtx.ResponseRequest == nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	if !h.prepareAccessGroups(w, &reqCtx) {
		return
	}
	shared = *reqCtx.ResponseRequest
	toolIdentifiers, validTools := responseToolIdentifiers(shared.Tools)
	if !h.authorizeTools(w, reqCtx, toolIdentifiers, validTools) {
		return
	}
	if !h.authorizeAccess(w, r.Context(), reqCtx, shared.Model, estimateResponseTokens(shared)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, shared.Model) {
		return
	}
	request = request.WithResponseRequest(*reqCtx.ResponseRequest)
	response, err := native.Interactions(r.Context(), reqCtx, request)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) GetInteraction(w http.ResponseWriter, r *http.Request) {
	h.getResponseAs(w, r, func(response openai.ResponseResponse) any {
		return openai.InteractionFromResponse(response)
	})
}

func (h Handler) CancelInteraction(w http.ResponseWriter, r *http.Request) {
	h.cancelResponseAs(w, r, func(response openai.ResponseResponse) any {
		return openai.InteractionFromResponse(response)
	})
}

func (h Handler) DeleteInteraction(w http.ResponseWriter, r *http.Request) {
	h.deleteResponseAs(w, r, true)
}
