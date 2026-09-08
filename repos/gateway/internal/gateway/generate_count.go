package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
)

type generateCountRequest struct {
	Contents []generateContent `json:"contents,omitempty"`
	Generate *struct {
		Model            string          `json:"model,omitempty"`
		GenerationConfig json.RawMessage `json:"generationConfig,omitempty"`
		generateRequest
	} `json:"generateContentRequest,omitempty"`
}

func (h Handler) countGenerateTokens(output *generateWriter, r *http.Request, model, key string) {
	var native generateCountRequest
	if !decodeInferenceRequest(output, r, &native) {
		return
	}
	request := generateRequest{Contents: native.Contents}
	if native.Generate != nil {
		if native.Contents != nil {
			writeError(output, 400, "invalid_request", "contents and generateContentRequest are mutually exclusive")
			return
		}
		if nested := native.Generate.Model; nested != "" && strings.TrimPrefix(nested, "models/") != model {
			writeError(output, 400, "invalid_request", "generateContentRequest.model must match the path model")
			return
		}
		if native.Generate.GenerationConfig != nil {
			writeError(output, 400, "unsupported_parameter", "generationConfig is not supported for token counting")
			return
		}
		request = native.Generate.generateRequest
	}
	chat, err := request.chat(model, false)
	if err != nil {
		writeError(output, 400, "invalid_request", err.Error())
		return
	}
	count, ok := h.countContextTokens(output, r, chat, key)
	if !ok {
		return
	}
	output.copyHeaders()
	writeJSON(output.destination, 200, map[string]int{"totalTokens": count})
	// The counting response is already in native format, not a Chat response.
	output.started, output.terminal = true, true
}
