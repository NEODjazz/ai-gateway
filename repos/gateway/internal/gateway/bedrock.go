package gateway

import (
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

func (h Handler) BedrockConverse(w http.ResponseWriter, r *http.Request) {
	model := strings.TrimSpace(r.PathValue("model"))
	if model == "" || len(model) > 2048 {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	var request openai.BedrockConverseRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	chat, err := request.ChatRequest(model, strings.TrimSpace(r.URL.Query().Get("provider")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	h.serveChatAdapted(w, r, chat, "bedrock_converse", func(response openai.ChatCompletionResponse) (any, error) {
		return openai.BedrockFromChat(response)
	})
}
