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
	Model             string                     `json:"model"`
	Messages          []messagesInput            `json:"messages"`
	System            json.RawMessage            `json:"system,omitempty"`
	Tools             []messagesTool             `json:"tools,omitempty"`
	ToolChoice        *messagesToolChoice        `json:"tool_choice,omitempty"`
	OutputConfig      *messagesOutputConfig      `json:"output_config,omitempty"`
	Thinking          *messagesThinking          `json:"thinking,omitempty"`
	Container         *messagesContainer         `json:"container,omitempty"`
	CacheControl      *messagesCacheControl      `json:"cache_control,omitempty"`
	InferenceGeo      string                     `json:"inference_geo,omitempty"`
	ContextManagement *messagesContextManagement `json:"context_management,omitempty"`
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
	request, err := (messagesRequest{Model: native.Model, MaxTokens: 1, Messages: native.Messages, System: native.System, Tools: native.Tools, ToolChoice: native.ToolChoice, OutputConfig: native.OutputConfig, Thinking: native.Thinking, Container: native.Container, CacheControl: native.CacheControl, InferenceGeo: native.InferenceGeo, ContextManagement: native.ContextManagement}).chatContext(true)
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
	payload := map[string]any{"input_tokens": count.InputTokens}
	if count.OriginalInputTokens != nil {
		payload["context_management"] = map[string]int{"original_input_tokens": *count.OriginalInputTokens}
	}
	writeJSON(w, 200, payload)
	completed = true
}

// countContextTokens applies the same admission and policy checks to each native
// counting protocol, without opening a generation billing lifecycle.
func (h Handler) countContextTokens(w http.ResponseWriter, r *http.Request, request openai.ChatCompletionRequest, key string) (provider.TokenCountResult, bool) {
	if kind, err := chatAttachmentError(request.Messages); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_"+kind, err.Error())
		return provider.TokenCountResult{}, false
	}
	req := modules.RequestContext{APIKey: key, RequestID: executionID(w), SessionID: sessionID(r), Request: request}
	var pipelineErr error
	if openai.HasChatResolvableReferences(request) {
		pipelineErr = h.pipeline.RunAuthentication(r.Context(), &req)
		if pipelineErr == nil {
			req.APIKey = ""
			if err := h.resolveMessagesDocumentReferences(r.Context(), req, &req.Request); err != nil {
				if errors.Is(err, errMessagesFileStorageUnavailable) {
					writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", err.Error())
				} else if errors.Is(err, errMessagesRemoteUnavailable) {
					writeError(w, http.StatusBadGateway, "remote_content_unavailable", err.Error())
				} else {
					writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				}
				return provider.TokenCountResult{}, false
			}
			if err := h.resolveCachedContentReference(r.Context(), req, &req.Request); err != nil {
				writeCachedContentReferenceError(w, err)
				return provider.TokenCountResult{}, false
			}
			pipelineErr = h.pipeline.RunTokenCountAfterAuthentication(r.Context(), &req)
		}
	} else {
		pipelineErr = h.pipeline.RunTokenCount(r.Context(), &req)
	}
	if pipelineErr != nil {
		if errors.Is(pipelineErr, modules.ErrUnauthorized) {
			writeError(w, 401, "unauthorized", "invalid api key")
		} else {
			writeProviderFailure(w, pipelineErr)
		}
		return provider.TokenCountResult{}, false
	}
	req.APIKey = ""
	if kind, err := chatAttachmentError(req.Request.Messages); err != nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module produced invalid "+kind+" input: "+err.Error())
		return provider.TokenCountResult{}, false
	}
	if !h.prepareAccessGroups(w, &req) {
		return provider.TokenCountResult{}, false
	}
	if err := h.bindSkillExecution(r.Context(), &req); err != nil {
		writeSkillExecutionError(w, err)
		return provider.TokenCountResult{}, false
	}
	request = req.Request
	tools, valid := chatToolIdentifiers(request.Tools, nil)
	tools = append(tools, skillExecutionIdentifiers(request.AnthropicSkills)...)
	if request.GeminiCodeExecution {
		tools = append(tools, "code_execution")
	}
	if request.GeminiURLContext {
		tools = append(tools, "url_context")
	}
	if request.GeminiGoogleMaps {
		tools = append(tools, "google_maps")
	}
	if request.AnthropicCodeExecution {
		tools = append(tools, "code_execution")
	}
	if request.AnthropicToolSearch != "" {
		tools = append(tools, "tool_search")
	}
	tools = append(tools, anthropicClientToolIdentifiers(request.AnthropicClientTools)...)
	tools = append(tools, anthropicClientToolsetIdentifiers(request.AnthropicClientToolsets)...)
	if !h.authorizeTools(w, req, tools, valid) || !h.authorizeAccess(w, r.Context(), req, request.Model, openai.ChatInputTokens(request)) {
		return provider.TokenCountResult{}, false
	}
	if !h.prepareModelFallbacks(w, r.Context(), &req, request.Model) {
		return provider.TokenCountResult{}, false
	}
	counter, ok := h.provider.(provider.TokenCountProvider)
	if !ok {
		writeError(w, 400, "unsupported_parameter", "count_tokens is not supported")
		return provider.TokenCountResult{}, false
	}
	result, err := counter.CountTokens(r.Context(), req)
	if err != nil {
		writeProviderFailure(w, err)
		return provider.TokenCountResult{}, false
	}
	if result.InputTokens < 0 {
		writeError(w, 502, "provider_error", "invalid provider token count")
		return provider.TokenCountResult{}, false
	}
	return result, true
}
