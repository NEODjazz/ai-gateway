package gateway

import (
	"net/http"

	"ai-gateway-gateway/internal/openai"
)

func (h Handler) Interactions(w http.ResponseWriter, r *http.Request) {
	var request openai.InteractionRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	responseRequest, message := request.ResponseRequest()
	if message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	h.serveResponsesAs(w, r, responseRequest, "interactions", func(response openai.ResponseResponse) any {
		return openai.InteractionFromResponse(response)
	})
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
