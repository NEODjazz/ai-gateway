package gateway

import (
	"errors"
	"net/http"
	"strings"

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
	request = request.WithResponseRequest(*reqCtx.ResponseRequest)
	shared, message = request.NativeResponseRequest()
	if message != "" {
		writeError(w, http.StatusBadGateway, "module_failed", "module produced an invalid interaction request: "+message)
		return
	}
	reqCtx.ResponseRequest = &shared
	if !h.prepareAccessGroups(w, &reqCtx) {
		return
	}
	toolIdentifiers, validTools := responseToolIdentifiers(shared.Tools)
	if !h.authorizeTools(w, reqCtx, toolIdentifiers, validTools) {
		return
	}
	if !h.authorizeResponseToolResources(w, r.Context(), &reqCtx, &shared) {
		return
	}
	if !h.authorizeAccess(w, r.Context(), reqCtx, shared.Model, estimateResponseTokens(shared)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, shared.Model) {
		return
	}
	if request.Stream {
		streaming, ok := native.(provider.StreamingInteractionProvider)
		if ok {
			started := false
			write := func(event, payload string) error {
				if !started {
					writeStreamHeaders(w)
					w.WriteHeader(http.StatusOK)
					started = true
				}
				return writeSSEResponseEvent(w, event, payload)
			}
			if _, streamed, err := streaming.StreamInteractions(r.Context(), reqCtx, request, write); streamed {
				if err != nil {
					if started {
						_ = write("error", errorStreamPayload(err))
						writeResponseStreamDone(w, true)
						return
					}
					writeProviderFailure(w, err)
					return
				}
				writeResponseStreamDone(w, true)
				return
			} else if err != nil {
				writeProviderFailure(w, err)
				return
			}
		}
		request.Stream = false
		response, err := native.Interactions(r.Context(), reqCtx, request)
		if err != nil {
			writeProviderFailure(w, err)
			return
		}
		started := false
		transformer := newInteractionStreamTransformer()
		err = synthesizeResponseStream(openai.ResponseFromInteraction(response), func(event, payload string) error {
			events, transformErr := transformer.Transform(event, payload)
			if transformErr != nil {
				return transformErr
			}
			if len(events) > 0 && !started {
				writeStreamHeaders(w)
				w.WriteHeader(http.StatusOK)
				started = true
			}
			for _, transformed := range events {
				if err := writeSSEResponseEvent(w, transformed.Name, transformed.Payload); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			if started {
				_ = writeSSEResponseEvent(w, "error", errorStreamPayload(err))
				return
			}
			writeProviderFailure(w, err)
			return
		}
		writeResponseStreamDone(w, true)
		return
	}
	response, err := native.Interactions(r.Context(), reqCtx, request)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) GetInteraction(w http.ResponseWriter, r *http.Request) {
	resource, ok := h.provider.(provider.InteractionResourceProvider)
	if !ok {
		h.getResponseAs(w, r, func(response openai.ResponseResponse) any { return openai.InteractionFromResponse(response) })
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	reqCtx, ok := h.authorizeInteractionResource(w, r, resource, id)
	if !ok {
		return
	}
	response, err := resource.RetrieveInteraction(r.Context(), reqCtx, id)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) CancelInteraction(w http.ResponseWriter, r *http.Request) {
	resource, ok := h.provider.(provider.InteractionResourceProvider)
	if !ok {
		h.cancelResponseAs(w, r, func(response openai.ResponseResponse) any { return openai.InteractionFromResponse(response) })
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	reqCtx, ok := h.authorizeInteractionResource(w, r, resource, id)
	if !ok {
		return
	}
	response, err := resource.CancelInteraction(r.Context(), reqCtx, id)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) DeleteInteraction(w http.ResponseWriter, r *http.Request) {
	resource, ok := h.provider.(provider.InteractionResourceProvider)
	if !ok {
		h.deleteResponseAs(w, r, true)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	reqCtx, ok := h.authorizeInteractionResource(w, r, resource, id)
	if !ok {
		return
	}
	if err := resource.DeleteInteraction(r.Context(), reqCtx, id); err != nil {
		writeProviderFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) authorizeInteractionResource(w http.ResponseWriter, r *http.Request, resource provider.InteractionResourceProvider, id string) (modules.RequestContext, bool) {
	reqCtx := modules.RequestContext{APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w)}
	if err := h.pipeline.RunAuthentication(r.Context(), &reqCtx); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return modules.RequestContext{}, false
		}
		writeError(w, http.StatusBadGateway, "module_failed", "authentication failed")
		return modules.RequestContext{}, false
	}
	reqCtx.APIKey = ""
	model, err := resource.ResolveInteractionResource(r.Context(), reqCtx, id)
	if err != nil {
		writeProviderFailure(w, err)
		return modules.RequestContext{}, false
	}
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeAccess(w, r.Context(), reqCtx, model, 0) {
		return modules.RequestContext{}, false
	}
	reqCtx.Request.Model = model
	return reqCtx, true
}
