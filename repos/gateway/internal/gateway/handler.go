package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/assistantstate"
	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/batchstate"
	"ai-gateway-gateway/internal/containerstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/finetunestate"
	"ai-gateway-gateway/internal/mcpclient"
	"ai-gateway-gateway/internal/mcpstate"
	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/publichttp"
	"ai-gateway-gateway/internal/ragstate"
	"ai-gateway-gateway/internal/skillstate"
	"ai-gateway-gateway/internal/vectorstate"
	"ai-gateway-gateway/internal/videostate"
)

type Handler struct {
	pipeline          modules.Pipeline
	resourceBilling   modules.Pipeline
	provider          provider.Provider
	rateLimits        RateLimitStore
	metrics           *Metrics
	ready             func(context.Context) error
	management        ManagementClient
	directory         IdentityDirectoryClient
	organizations     OrganizationDirectoryClient
	dlp               modules.Module
	av                modules.Module
	guardrails        *GuardrailMonitor
	cacheConfig       CacheRuntimeConfig
	logging           *LoggingRegistry
	agents            *AgentRegistry
	a2aTasks          a2astate.Store
	a2aTaskConfig     A2ATaskRuntimeConfig
	assistants        assistantstate.Store
	assistantThreads  assistantstate.ThreadStore
	assistantRuns     assistantstate.RunStore
	assistantConfig   AssistantRuntimeConfig
	a2aSubscriptions  chan struct{}
	a2aHTTPClient     httpDoer
	a2aPushJobs       asyncstate.Store
	a2aPushConfigs    a2astate.AtomicOutboxStore
	a2aPushVault      *a2aPushVault
	mcp               *MCPRegistry
	mcpRuntime        MCPRuntimeFactory
	mcpCalls          mcpstate.Store
	files             filestate.Store
	fileConfig        FileRuntimeConfig
	batches           batchstate.Store
	fineTuning        finetunestate.Store
	fineTuningJobs    asyncstate.Store
	videos            videostate.Store
	containers        containerstate.Store
	videoJobs         asyncstate.Store
	batchJobs         asyncstate.Store
	skills            skillstate.Store
	vectorStores      vectorstate.Store
	ragIngest         ragstate.Store
	vectorStoreConfig VectorStoreRuntimeConfig
	access            *AccessRegistry
	budgets           BudgetManagementClient
	usage             UsageManagementClient
	requestLogs       RequestLogClient
	models            *modelcatalog.Registry
	audit             AuditClient
	apiDocs           apiDocsConfig
	adminUI           bool
	browserSSO        *BrowserSSO
	adminState        *AdminStateRuntime
}

func (h Handler) WithBatchStore(store batchstate.Store, jobs asyncstate.Store) Handler {
	h.batches = store
	h.batchJobs = jobs
	return h
}

// WithResourceBillingPipeline configures billing for owned resource APIs whose
// authentication pipeline is intentionally separate from provider modules.
func (h Handler) WithResourceBillingPipeline(pipeline modules.Pipeline) Handler {
	h.resourceBilling = pipeline
	return h
}

func (h Handler) resourceBillingPipeline() modules.Pipeline {
	if h.resourceBilling.HasModule("billing") {
		return h.resourceBilling
	}
	return h.pipeline
}

func (h Handler) WithAdminState(runtime *AdminStateRuntime) Handler {
	h.adminState = runtime
	return h
}

// WithAPIDocs enables the embedded API documentation. Interactive requests are
// controlled separately so operators can expose read-only documentation.
func (h Handler) WithAPIDocs(tryItOutEnabled bool) Handler {
	h.apiDocs = apiDocsConfig{enabled: true, tryItOutEnabled: tryItOutEnabled}
	return h
}

// WithAdminUI enables the embedded management console. All data APIs remain
// protected by the normal admin bearer authentication and RBAC pipeline.
func (h Handler) WithAdminUI() Handler {
	h.adminUI = true
	return h
}

func (h Handler) WithBrowserSSO(sso *BrowserSSO) Handler {
	h.browserSSO = sso
	return h
}

func NewHandler(pipeline modules.Pipeline, llmProvider provider.Provider) Handler {
	return NewHandlerWithRateLimitStore(pipeline, llmProvider, NewMemoryRateLimitStore())

}

func NewHandlerWithRateLimitStore(pipeline modules.Pipeline, llmProvider provider.Provider, rateLimits RateLimitStore) Handler {
	return NewHandlerWithReadiness(pipeline, llmProvider, rateLimits, nil)
}

func NewHandlerWithReadiness(pipeline modules.Pipeline, llmProvider provider.Provider, rateLimits RateLimitStore, ready func(context.Context) error) Handler {
	return NewHandlerWithMetrics(pipeline, llmProvider, rateLimits, ready, NewMetrics())
}

func NewHandlerWithMetrics(pipeline modules.Pipeline, llmProvider provider.Provider, rateLimits RateLimitStore, ready func(context.Context) error, metrics *Metrics) Handler {
	if rateLimits == nil {
		rateLimits = NewMemoryRateLimitStore()
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return Handler{pipeline: pipeline, provider: llmProvider, rateLimits: rateLimits, metrics: metrics, ready: ready, a2aHTTPClient: publichttp.NewClient(15 * time.Second), mcpRuntime: func(endpoint string) (MCPRuntimeClient, error) { return mcpclient.New(endpoint) }}
}

func (h Handler) Health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) Ready(w http.ResponseWriter, r *http.Request) {
	if h.ready != nil {
		if err := h.ready(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "required storage is unavailable")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) Models(w http.ResponseWriter, r *http.Request) {
	models, ok := h.authorizedModels(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, openai.ModelsResponse{Object: "list", Data: models})
}

func (h Handler) GetModel(w http.ResponseWriter, r *http.Request) {
	models, ok := h.authorizedModels(w, r)
	if !ok {
		return
	}
	modelID := r.PathValue("model")
	for _, model := range models {
		if model.ID == modelID {
			writeJSON(w, http.StatusOK, model)
			return
		}
	}
	writeError(w, http.StatusNotFound, "model_not_found", "model not found")
}

func (h Handler) authorizedModels(w http.ResponseWriter, r *http.Request) ([]openai.Model, bool) {
	reqCtx := modules.RequestContext{
		APIKey:    bearerToken(r.Header.Get("Authorization")),
		RequestID: requestID(r),
		SessionID: sessionID(r),
	}
	if err := h.pipeline.Run(r.Context(), &reqCtx); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return nil, false
		}
		writeError(w, http.StatusBadGateway, "module_failed", err.Error())
		return nil, false
	}
	reqCtx.APIKey = ""
	if !h.prepareAccessGroups(w, &reqCtx) {
		return nil, false
	}

	models := filterModels(h.provider.Models(), reqCtx.AllowedModels)
	if reqCtx.AccessGroupsEvaluated {
		if len(reqCtx.AccessGroupModels) == 0 {
			models = nil
		} else {
			models = filterModels(models, reqCtx.AccessGroupModels)
		}
	}
	if h.access != nil {
		filtered := models[:0]
		for _, model := range models {
			if allowed, _ := h.access.TagModelAllowed(reqCtx.Tags, model.ID); allowed {
				filtered = append(filtered, model)
			}
		}
		models = filtered
	}
	return models, true
}

func (h Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	var request openai.ChatCompletionRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	h.serveChat(w, r, request)
}

func (h Handler) serveChat(w http.ResponseWriter, r *http.Request, request openai.ChatCompletionRequest) {
	h.serveChatAs(w, r, request, "")
}

func (h Handler) serveChatAs(w http.ResponseWriter, r *http.Request, request openai.ChatCompletionRequest, apiType string) {
	h.serveChatAdapted(w, r, request, apiType, nil)
}

func (h Handler) serveChatAdapted(w http.ResponseWriter, r *http.Request, request openai.ChatCompletionRequest, apiType string, transform func(openai.ChatCompletionResponse) (any, error)) {
	if err := openai.ValidateLegacyFunctionRequest(request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if message := request.ChatGenerationOptions.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	if _, message := openai.ChatRequestPromptCacheBreakpoints(request); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	for _, message := range request.Messages {
		if err := openai.ValidateChatReasoningContent(message.Role, message.ReasoningContent); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if message.Annotations != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "messages.annotations is response-only")
			return
		}
		if message.Audio != nil {
			if message.Role != "assistant" {
				writeError(w, http.StatusBadRequest, "invalid_request", "messages.audio requires role=assistant")
				return
			}
			if err := openai.ValidateChatAudioReference(message.Audio); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
		}
	}
	if request.MaxTokens != nil && request.MaxCompletionTokens != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "max_tokens and max_completion_tokens are mutually exclusive")
		return
	}
	if request.StreamOptions != nil && !request.Stream {
		writeError(w, http.StatusBadRequest, "invalid_request", "stream_options requires stream=true")
		return
	}

	invalidMaxTokens := request.MaxTokens != nil && (*request.MaxTokens < 0 || *request.MaxTokens == 0 && !request.AllowZeroMaxTokens)
	if invalidMaxTokens || (request.MaxCompletionTokens != nil && *request.MaxCompletionTokens <= 0) {
		writeError(w, http.StatusBadRequest, "invalid_request", "output token limit must be positive")
		return
	}
	stream := request.Stream
	reqCtx := modules.RequestContext{
		APIKey:    bearerToken(r.Header.Get("Authorization")),
		RequestID: executionID(w),
		SessionID: sessionID(r),
		Request:   request,
	}
	if apiType != "" {
		reqCtx.Metadata = map[string]string{"gateway.api_type": apiType}
	}

	var pipelineErr error
	if openai.HasChatDocumentReferences(request) {
		pipelineErr = h.pipeline.RunAuthentication(r.Context(), &reqCtx)
		if pipelineErr == nil {
			reqCtx.APIKey = ""
			if err := h.resolveMessagesDocumentReferences(r.Context(), reqCtx, &reqCtx.Request); err != nil {
				if errors.Is(err, errMessagesFileStorageUnavailable) {
					writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", err.Error())
				} else if errors.Is(err, errMessagesURLUnavailable) {
					writeError(w, http.StatusBadGateway, "document_unavailable", err.Error())
				} else {
					writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				}
				return
			}
			pipelineErr = h.pipeline.RunAfterAuthentication(r.Context(), &reqCtx)
		}
	} else {
		pipelineErr = h.pipeline.Run(r.Context(), &reqCtx)
	}
	if pipelineErr != nil {
		if errors.Is(pipelineErr, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return
		}
		writeError(w, http.StatusBadGateway, "module_failed", pipelineErr.Error())
		return
	}
	reqCtx.APIKey = ""
	if !h.prepareAccessGroups(w, &reqCtx) {
		return
	}
	if _, err := openai.ChatImageAttachments(reqCtx.Request.Messages); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_image", err.Error())
		return
	}
	if _, err := openai.ChatAudioAttachments(reqCtx.Request.Messages); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_audio", err.Error())
		return
	}
	if _, err := openai.ChatFileAttachments(reqCtx.Request.Messages); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_file", err.Error())
		return
	}
	if _, err := openai.ChatVideoAttachments(reqCtx.Request.Messages); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_video", err.Error())
		return
	}
	request = reqCtx.Request
	if err := h.bindSkillExecution(r.Context(), &reqCtx); err != nil {
		writeSkillExecutionError(w, err)
		return
	}
	request = reqCtx.Request
	if err := openai.ValidateLegacyFunctionRequest(request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	toolIdentifiers, validTools := chatToolIdentifiers(request.Tools, request.Functions)
	toolIdentifiers = append(toolIdentifiers, skillExecutionIdentifiers(request.AnthropicSkills)...)
	if request.GeminiCodeExecution {
		toolIdentifiers = append(toolIdentifiers, "code_execution")
	}
	if request.AnthropicCodeExecution {
		toolIdentifiers = append(toolIdentifiers, "code_execution")
	}
	if request.AnthropicToolSearch != "" {
		toolIdentifiers = append(toolIdentifiers, "tool_search")
	}
	toolIdentifiers = append(toolIdentifiers, anthropicClientToolIdentifiers(request.AnthropicClientTools)...)
	toolIdentifiers = append(toolIdentifiers, anthropicClientToolsetIdentifiers(request.AnthropicClientToolsets)...)
	if !h.authorizeTools(w, reqCtx, toolIdentifiers, validTools) {
		return
	}
	if !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateChatTokens(request)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	if stream && len(reqCtx.Request.AnthropicSkills) == 0 {
		streamStarted := false
		includeUsage := request.StreamOptions != nil && request.StreamOptions.IncludeUsage
		usageDelivered := false
		writeStreamPayload := func(payload string) error {
			payload, hasUsage, deliver, err := transformChatStreamPayload(payload, request.StreamOptions, includeUsage)
			if err != nil {
				return err
			}
			usageDelivered = usageDelivered || hasUsage && deliver
			if !deliver {
				return nil
			}
			if !streamStarted {
				writeStreamHeaders(w)
				w.WriteHeader(http.StatusOK)
				streamStarted = true
			}
			return writeSSEPayload(w, payload)
		}
		if response, streamed, err := h.provider.StreamChatCompletions(r.Context(), reqCtx, writeStreamPayload); streamed {
			if err != nil {
				if streamStarted {
					_ = writeStreamPayload(errorStreamPayload(err))
					writeSSEDone(w)
					return
				}
				writeProviderFailure(w, err)
				return
			}
			if sink, ok := w.(interface {
				chatStreamResult(openai.ChatCompletionResponse)
			}); ok {
				sink.chatStreamResult(response)
			}
			if includeUsage && !usageDelivered {
				_ = writeStreamPayload(chatCompletionUsagePayload(response))
			}
			writeSSEDone(w)
			return
		} else if err != nil {
			writeProviderFailure(w, err)
			return
		}
	}

	reqCtx.Request.Stream = false
	response, err := h.provider.ChatCompletions(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	if err := h.recordSkillExecution(r.Context(), reqCtx, response); err != nil {
		writeSkillExecutionError(w, err)
		return
	}
	if sink, ok := w.(interface {
		chatResult(openai.ChatCompletionResponse, bool)
	}); ok {
		sink.chatResult(response, stream)
		return
	}

	if stream {
		writeChatCompletionStream(w, response, request.StreamOptions)
		return
	}
	if transform != nil {
		adapted, err := transform(response)
		if err != nil {
			writeProviderFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, adapted)
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h Handler) Completions(w http.ResponseWriter, r *http.Request) {
	var request openai.CompletionRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if message := validateCompletionRequest(request); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		CompletionRequest: &request,
		Request: openai.ChatCompletionRequest{
			Provider: request.Provider, Model: request.Model, Messages: []openai.Message{{Role: "user", Content: openai.CompletionPromptPolicyContent(request.Prompt)}},
		},
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
	if reqCtx.CompletionRequest == nil || len(reqCtx.Request.Messages) != 1 {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed completion request")
		return
	}
	request = *reqCtx.CompletionRequest
	request.Provider = reqCtx.Request.Provider
	request.Model = reqCtx.Request.Model
	effectivePrompt, err := openai.ApplyCompletionPromptPolicyContent(request.Prompt, reqCtx.Request.Messages[0].Content)
	if err != nil {
		writeError(w, http.StatusBadGateway, "module_failed", err.Error())
		return
	}
	request.Prompt = effectivePrompt
	*reqCtx.CompletionRequest = request
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateCompletionTokens(request)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	completionProvider, ok := h.provider.(provider.CompletionProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "text completions are not supported by the configured provider")
		return
	}
	if request.Stream {
		if streamingProvider, ok := h.provider.(provider.StreamingCompletionProvider); ok {
			streamStarted := false
			writeStreamPayload := func(payload string) error {
				if !streamStarted {
					writeStreamHeaders(w)
					w.WriteHeader(http.StatusOK)
					streamStarted = true
				}
				return writeSSEPayload(w, payload)
			}
			if response, streamed, err := streamingProvider.StreamCompletions(r.Context(), reqCtx, writeStreamPayload); streamed {
				if err != nil {
					if streamStarted {
						_ = writeStreamPayload(errorStreamPayload(err))
						writeSSEDone(w)
						return
					}
					writeProviderFailure(w, err)
					return
				}
				_ = response
				writeSSEDone(w)
				return
			} else if err != nil {
				writeProviderFailure(w, err)
				return
			}
		}
	}
	response, err := completionProvider.Completions(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	if request.Stream {
		writeCompletionStream(w, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func validateCompletionRequest(request openai.CompletionRequest) string {
	if strings.TrimSpace(request.Model) == "" {
		return "model is required"
	}
	if _, err := openai.InspectCompletionPrompt(request.Prompt); err != nil {
		return err.Error()
	}
	if request.MaxTokens != nil && *request.MaxTokens < 0 {
		return "max_tokens must be nonnegative"
	}
	if request.MinTokens != nil && (*request.MinTokens < 0 || request.MaxTokens != nil && *request.MinTokens > *request.MaxTokens) {
		return "min_tokens must be nonnegative and not exceed max_tokens"
	}
	if message := openai.ValidateMetadata(request.Metadata); message != "" {
		return message
	}
	n := 1
	if request.N != nil {
		n = *request.N
		if n < 1 || n > 128 {
			return "n must be between 1 and 128"
		}
	}
	if _, err := openai.CompletionChoiceCount(request.Prompt, request.N); err != nil {
		return err.Error()
	}
	if request.BestOf != nil {
		if *request.BestOf < 1 || *request.BestOf > 20 || *request.BestOf < n {
			return "best_of must be between n and 20"
		}
		if request.Stream && *request.BestOf > 1 {
			return "best_of greater than 1 cannot be streamed"
		}
	}
	if request.Logprobs != nil && (*request.Logprobs < 0 || *request.Logprobs > 5) {
		return "logprobs must be between 0 and 5"
	}
	for _, value := range []*float64{request.FrequencyPenalty, request.PresencePenalty} {
		if value != nil && (*value < -2 || *value > 2) {
			return "frequency_penalty and presence_penalty must be between -2 and 2"
		}
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 2) {
		return "temperature must be between 0 and 2"
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return "top_p must be between 0 and 1"
	}
	if _, valid := openai.StopSequences(request.Stop); !valid {
		return "stop must contain between 1 and 4 non-empty strings"
	}
	for token, bias := range request.LogitBias {
		if _, err := strconv.ParseUint(token, 10, 64); err != nil || bias < -100 || bias > 100 {
			return "logit_bias requires nonnegative token IDs and biases between -100 and 100"
		}
	}
	return ""
}

func (h Handler) Responses(w http.ResponseWriter, r *http.Request) {
	var request openai.ResponseRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	h.serveResponsesAs(w, r, request, "", nil, nil, nil, false)
}

type responseStreamEvent struct {
	Name    string
	Payload string
}

type responseStreamTransform func(string, string, modules.RequestContext) ([]responseStreamEvent, error)
type responseStreamFinalize func(openai.ResponseResponse, modules.RequestContext) ([]responseStreamEvent, error)

func (h Handler) serveResponsesAs(w http.ResponseWriter, r *http.Request, request openai.ResponseRequest, apiType string, transform func(openai.ResponseResponse, modules.RequestContext) any, streamTransform responseStreamTransform, streamFinalize responseStreamFinalize, closeStreamWithoutSentinel bool) {
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	if _, err := openai.ResponseImageAttachments(request.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_image", err.Error())
		return
	}
	if _, err := openai.ResponseAudioAttachments(request.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_audio", err.Error())
		return
	}
	if _, err := openai.ResponseFileAttachments(request.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_file", err.Error())
		return
	}

	reqCtx := modules.RequestContext{
		APIKey:          bearerToken(r.Header.Get("Authorization")),
		RequestID:       executionID(w),
		SessionID:       sessionID(r),
		ResponseRequest: &request,
		Request: openai.ChatCompletionRequest{
			Provider: request.Provider,
			Model:    request.Model,
			Messages: responseMessages(request),
		},
	}
	if apiType != "" {
		reqCtx.Metadata = map[string]string{"gateway.api_type": apiType}
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
	request = *reqCtx.ResponseRequest
	if !h.prepareAccessGroups(w, &reqCtx) {
		return
	}
	if _, err := openai.ResponseImageAttachments(reqCtx.ResponseRequest.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_image", err.Error())
		return
	}
	if _, err := openai.ResponseAudioAttachments(reqCtx.ResponseRequest.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_audio", err.Error())
		return
	}
	if _, err := openai.ResponseFileAttachments(reqCtx.ResponseRequest.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_file", err.Error())
		return
	}
	toolIdentifiers, validTools := responseToolIdentifiers(request.Tools)
	if !h.authorizeTools(w, reqCtx, toolIdentifiers, validTools) {
		return
	}
	if !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateResponseTokens(request)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	stream := request.Stream
	if stream {
		streamStarted := false
		writeStreamEvent := func(event string, payload string) error {
			events := []responseStreamEvent{{Name: event, Payload: payload}}
			var err error
			if streamTransform != nil {
				events, err = streamTransform(event, payload, reqCtx)
				if err != nil {
					return err
				}
			}
			if len(events) == 0 {
				return nil
			}
			if !streamStarted {
				writeStreamHeaders(w)
				w.WriteHeader(http.StatusOK)
				streamStarted = true
			}
			for _, transformed := range events {
				if err := writeSSEResponseEvent(w, transformed.Name, transformed.Payload); err != nil {
					return err
				}
			}
			return nil
		}
		if response, streamed, err := h.provider.StreamResponses(r.Context(), reqCtx, writeStreamEvent); streamed {
			if err != nil {
				if streamStarted {
					_ = writeStreamEvent("error", errorStreamPayload(err))
					if !closeStreamWithoutSentinel {
						writeResponseStreamDone(w, streamTransform != nil)
					}
					return
				}
				writeProviderFailure(w, err)
				return
			}
			if streamFinalize != nil {
				events, finalizeErr := streamFinalize(response, reqCtx)
				if finalizeErr != nil {
					_ = writeStreamEvent("error", errorStreamPayload(finalizeErr))
					return
				}
				if len(events) > 0 && !streamStarted {
					writeStreamHeaders(w)
					w.WriteHeader(http.StatusOK)
					streamStarted = true
				}
				for _, finalized := range events {
					if err := writeSSEResponseEvent(w, finalized.Name, finalized.Payload); err != nil {
						return
					}
				}
			}
			if !closeStreamWithoutSentinel {
				writeResponseStreamDone(w, streamTransform != nil)
			}
			return
		} else if err != nil {
			writeProviderFailure(w, err)
			return
		}
	}

	reqCtx.ResponseRequest.Stream = false
	response, err := h.provider.Responses(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	if stream {
		started := false
		err := synthesizeResponseStream(response, func(event, payload string) error {
			events := []responseStreamEvent{{Name: event, Payload: payload}}
			var transformErr error
			if streamTransform != nil {
				events, transformErr = streamTransform(event, payload, reqCtx)
				if transformErr != nil {
					return transformErr
				}
			}
			if len(events) == 0 {
				return nil
			}
			if !started {
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
			if !started {
				writeProviderFailure(w, err)
			}
			return
		}
		if streamFinalize != nil {
			events, finalizeErr := streamFinalize(response, reqCtx)
			if finalizeErr != nil {
				return
			}
			if len(events) > 0 && !started {
				writeStreamHeaders(w)
				w.WriteHeader(http.StatusOK)
				started = true
			}
			for _, finalized := range events {
				if err := writeSSEResponseEvent(w, finalized.Name, finalized.Payload); err != nil {
					return
				}
			}
		}
		if !closeStreamWithoutSentinel {
			writeResponseStreamDone(w, streamTransform != nil)
		}
		return
	}
	if transform != nil {
		writeJSON(w, http.StatusOK, transform(response, reqCtx))
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func writeResponseStreamDone(w http.ResponseWriter, named bool) {
	if named {
		_ = writeSSEResponseEvent(w, "done", "[DONE]")
		return
	}
	writeSSEDone(w)
}

func (h Handler) CompactResponse(w http.ResponseWriter, r *http.Request) {
	var request openai.ResponseCompactRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if message := validateResponseCompactRequest(request); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	responseRequest := openai.ResponseRequest{Provider: request.Provider, Model: request.Model, Input: request.Input, Instructions: request.Instructions}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		ResponseRequest: &responseRequest,
		Request:         openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model, Messages: responseMessages(responseRequest)},
		Metadata:        map[string]string{"gateway.api_type": "responses_compact"},
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
		writeError(w, http.StatusBadGateway, "module_failed", "module removed response compaction request")
		return
	}
	request = openai.ResponseCompactRequest{
		Provider: reqCtx.ResponseRequest.Provider, Model: reqCtx.ResponseRequest.Model,
		Input: reqCtx.ResponseRequest.Input, Instructions: reqCtx.ResponseRequest.Instructions,
	}
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateResponseCompactTokens(request)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	compactProvider, ok := h.provider.(provider.ResponseCompactProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "response compaction is not supported by the configured provider")
		return
	}
	response, err := compactProvider.CompactResponse(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func validateResponseCompactRequest(request openai.ResponseCompactRequest) string {
	if strings.TrimSpace(request.Model) == "" {
		return "model is required"
	}
	switch input := request.Input.(type) {
	case string:
		if strings.TrimSpace(input) == "" {
			return "input is required"
		}
	case []any:
		if len(input) == 0 {
			return "input is required"
		}
	case nil:
		return "input is required"
	default:
		return "input must be a string or a non-empty array"
	}
	return ""
}

func (h Handler) GetResponse(w http.ResponseWriter, r *http.Request) {
	h.getResponseAs(w, r, func(response openai.ResponseResponse) any { return response })
}

func (h Handler) getResponseAs(w http.ResponseWriter, r *http.Request, transform func(openai.ResponseResponse) any) {
	resourceProvider, ok := h.provider.(provider.ResponseResourceProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "response_lifecycle_unsupported", "response lifecycle is not supported")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	reqCtx, ok := h.authorizeResponseResource(w, r, resourceProvider, id)
	if !ok {
		return
	}
	response, err := resourceProvider.RetrieveResponse(r.Context(), reqCtx, id)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, transform(response))
}

func (h Handler) DeleteResponse(w http.ResponseWriter, r *http.Request) {
	h.deleteResponseAs(w, r, false)
}

func (h Handler) deleteResponseAs(w http.ResponseWriter, r *http.Request, noContent bool) {
	resourceProvider, ok := h.provider.(provider.ResponseDeletionProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "response_lifecycle_unsupported", "response deletion is not supported")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	reqCtx, ok := h.authorizeResponseResource(w, r, resourceProvider, id)
	if !ok {
		return
	}
	response, err := resourceProvider.DeleteResponse(r.Context(), reqCtx, id)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	if noContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) CancelResponse(w http.ResponseWriter, r *http.Request) {
	h.cancelResponseAs(w, r, func(response openai.ResponseResponse) any { return response })
}

func (h Handler) cancelResponseAs(w http.ResponseWriter, r *http.Request, transform func(openai.ResponseResponse) any) {
	resourceProvider, ok := h.provider.(provider.ResponseCancellationProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "response_lifecycle_unsupported", "response cancellation is not supported")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	reqCtx, ok := h.authorizeResponseResource(w, r, resourceProvider, id)
	if !ok {
		return
	}
	response, err := resourceProvider.CancelResponse(r.Context(), reqCtx, id)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, transform(response))
}

func (h Handler) ListResponseInputItems(w http.ResponseWriter, r *http.Request) {
	resourceProvider, ok := h.provider.(provider.ResponseInputItemsProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "response_lifecycle_unsupported", "response input items are not supported")
		return
	}
	options, err := responseInputItemsOptions(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	reqCtx, ok := h.authorizeResponseResource(w, r, resourceProvider, id)
	if !ok {
		return
	}
	response, err := resourceProvider.ListResponseInputItems(r.Context(), reqCtx, id, options)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func responseInputItemsOptions(r *http.Request) (provider.ResponseInputItemsOptions, error) {
	query := r.URL.Query()
	for key := range query {
		if key != "after" && key != "limit" && key != "order" && key != "include" {
			return provider.ResponseInputItemsOptions{}, fmt.Errorf("unsupported query parameter %q", key)
		}
	}
	options := provider.ResponseInputItemsOptions{After: query.Get("after"), Order: query.Get("order"), Include: append([]string(nil), query["include"]...)}
	if options.After != "" && !validLifecycleToken(options.After) {
		return provider.ResponseInputItemsOptions{}, errors.New("after is invalid")
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			return provider.ResponseInputItemsOptions{}, errors.New("limit must be between 1 and 100")
		}
		options.Limit = limit
	}
	if options.Order != "" && options.Order != "asc" && options.Order != "desc" {
		return provider.ResponseInputItemsOptions{}, errors.New("order must be asc or desc")
	}
	if len(options.Include) > 16 {
		return provider.ResponseInputItemsOptions{}, errors.New("include must contain at most 16 values")
	}
	for _, value := range options.Include {
		if !validLifecycleToken(value) {
			return provider.ResponseInputItemsOptions{}, errors.New("include contains an invalid value")
		}
	}
	return options, nil
}

func validLifecycleToken(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func (h Handler) authorizeResponseResource(w http.ResponseWriter, r *http.Request, resolver provider.ResponseResourceResolver, id string) (modules.RequestContext, bool) {
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
	model, err := resolver.ResolveResponseResource(r.Context(), reqCtx, id)
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

func (h Handler) Embeddings(w http.ResponseWriter, r *http.Request) {
	var request openai.EmbeddingRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.Model) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	if _, err := openai.InspectEmbeddingInput(request.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if message := openai.ValidateMetadata(request.Metadata); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	if request.EncodingFormat != "" && request.EncodingFormat != "float" && request.EncodingFormat != "base64" {
		writeError(w, http.StatusBadRequest, "invalid_request", "encoding_format must be float or base64")
		return
	}
	if request.OutputDType != "" && request.OutputDType != "float" && request.OutputDType != "int8" && request.OutputDType != "uint8" && request.OutputDType != "binary" && request.OutputDType != "ubinary" {
		writeError(w, http.StatusBadRequest, "invalid_request", "output_dtype must be float, int8, uint8, binary, or ubinary")
		return
	}
	if request.InputType != "" && request.InputType != "search_document" && request.InputType != "search_query" && request.InputType != "classification" && request.InputType != "clustering" {
		writeError(w, http.StatusBadRequest, "invalid_request", "input_type is invalid")
		return
	}
	if request.Dimensions != nil && *request.Dimensions <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "dimensions must be positive")
		return
	}

	reqCtx := modules.RequestContext{
		APIKey:           bearerToken(r.Header.Get("Authorization")),
		RequestID:        executionID(w),
		SessionID:        sessionID(r),
		EmbeddingRequest: &request,
		Request: openai.ChatCompletionRequest{
			Provider: request.Provider,
			Model:    request.Model,
		},
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
	if reqCtx.EmbeddingRequest == nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	request = *reqCtx.EmbeddingRequest
	if !h.prepareAccessGroups(w, &reqCtx) {
		return
	}
	if !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateEmbeddingTokens(request)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	embeddingProvider, ok := h.provider.(provider.EmbeddingProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "embeddings are not supported by the configured provider")
		return
	}
	response, err := embeddingProvider.Embeddings(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) Rerank(w http.ResponseWriter, r *http.Request) {
	var request openai.RerankRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if message := validateRerankRequest(request); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r), RerankRequest: &request,
		Request: openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model},
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
	if reqCtx.RerankRequest == nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	request = *reqCtx.RerankRequest
	if !h.prepareAccessGroups(w, &reqCtx) {
		return
	}
	if !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateRerankTokens(request)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	rerankProvider, ok := h.provider.(provider.RerankProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "rerank is not supported by the configured provider")
		return
	}
	response, err := rerankProvider.Rerank(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) Moderations(w http.ResponseWriter, r *http.Request) {
	var request openai.ModerationRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.Model) == "" {
		request.Model = "omni-moderation-latest"
	}
	if _, err := openai.InspectModerationInput(request.Input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if message := openai.ValidateMetadata(request.Metadata); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r), ModerationRequest: &request,
		Request: openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model},
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
	if reqCtx.ModerationRequest == nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	request = *reqCtx.ModerationRequest
	if _, err := openai.InspectModerationInput(request.Input); err != nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module returned an invalid moderation input")
		return
	}
	if !h.prepareAccessGroups(w, &reqCtx) {
		return
	}
	if !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, openai.ModerationInputTokenCount(request.Input)) {
		return
	}
	if !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	moderationProvider, ok := h.provider.(provider.ModerationProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "moderations are not supported by the configured provider")
		return
	}
	response, err := moderationProvider.Moderations(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) GenerateImage(w http.ResponseWriter, r *http.Request) {
	var request openai.ImageGenerationRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		ImageGenerationRequest: &request,
		Request:                openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model},
	}
	reqCtx.Metadata = map[string]string{"gateway.api_type": "image_generation"}
	if err := h.pipeline.Run(r.Context(), &reqCtx); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return
		}
		writeError(w, http.StatusBadGateway, "module_failed", err.Error())
		return
	}
	reqCtx.APIKey = ""
	if reqCtx.ImageGenerationRequest == nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	request = *reqCtx.ImageGenerationRequest
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadGateway, "module_failed", "module returned an invalid image request")
		return
	}
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateImageGenerationTokens(request)) || !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	imageProvider, ok := h.provider.(provider.ImageGenerationProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "image generation is not supported by the configured provider")
		return
	}
	if request.Stream {
		streamStarted := false
		writeStreamPayload := func(payload string) error {
			if !streamStarted {
				writeStreamHeaders(w)
				w.WriteHeader(http.StatusOK)
				streamStarted = true
			}
			return writeSSEPayload(w, payload)
		}
		if streamingProvider, supported := h.provider.(provider.StreamingImageGenerationProvider); supported {
			if response, streamed, err := streamingProvider.StreamGenerateImage(r.Context(), reqCtx, writeStreamPayload); streamed {
				if err != nil {
					if streamStarted {
						_ = writeStreamPayload(errorStreamPayload(err))
						return
					}
					writeProviderFailure(w, err)
					return
				}
				if sink, ok := w.(interface {
					imageGenerationStreamResult(openai.ImageGenerationResponse)
				}); ok {
					sink.imageGenerationStreamResult(response)
				}
				return
			} else if err != nil {
				writeProviderFailure(w, err)
				return
			}
		}
		if request.PartialImages != nil && *request.PartialImages > 0 {
			writeError(w, http.StatusBadGateway, "streaming_unsupported", "partial image streaming is not supported by the selected deployment policy")
			return
		}
		request.Stream = false
		request.PartialImages = nil
		reqCtx.ImageGenerationRequest = &request
		response, err := imageProvider.GenerateImage(r.Context(), reqCtx)
		if err != nil {
			writeProviderFailure(w, err)
			return
		}
		payload, err := imageCompletedPayload(response, "image_generation.completed")
		if err != nil {
			writeError(w, http.StatusBadGateway, "provider_failed", err.Error())
			return
		}
		_ = writeStreamPayload(payload)
		return
	}
	response, err := imageProvider.GenerateImage(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func imageCompletedPayload(response openai.ImageGenerationResponse, eventType string) (string, error) {
	if len(response.Data) != 1 || response.Data[0].B64JSON == "" || response.Usage == nil {
		return "", errors.New("synthesized image streaming requires one base64 image and exact usage")
	}
	payload, err := json.Marshal(map[string]any{
		"type": eventType, "b64_json": response.Data[0].B64JSON,
		"background": response.Background, "created_at": response.Created, "output_format": response.OutputFormat,
		"quality": response.Quality, "size": response.Size, "usage": response.Usage,
	})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (h Handler) EditImage(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeImageEditRequest(w, r)
	if !ok {
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		ImageEditRequest: &request,
		Request:          openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model},
		Metadata:         map[string]string{"gateway.api_type": "image_edit"},
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
	if reqCtx.ImageEditRequest == nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	request = *reqCtx.ImageEditRequest
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadGateway, "module_failed", "module returned an invalid image edit request")
		return
	}
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateImageEditTokens(request)) || !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	imageProvider, ok := h.provider.(provider.ImageEditProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "image edits are not supported by the configured provider")
		return
	}
	if request.Stream {
		streamStarted := false
		writeStreamPayload := func(payload string) error {
			if !streamStarted {
				writeStreamHeaders(w)
				w.WriteHeader(http.StatusOK)
				streamStarted = true
			}
			return writeSSEPayload(w, payload)
		}
		if streamingProvider, supported := h.provider.(provider.StreamingImageEditProvider); supported {
			if response, streamed, err := streamingProvider.StreamEditImage(r.Context(), reqCtx, writeStreamPayload); streamed {
				if err != nil {
					if streamStarted {
						_ = writeStreamPayload(errorStreamPayload(err))
						return
					}
					writeProviderFailure(w, err)
					return
				}
				if sink, ok := w.(interface {
					imageEditStreamResult(openai.ImageGenerationResponse)
				}); ok {
					sink.imageEditStreamResult(response)
				}
				return
			} else if err != nil {
				writeProviderFailure(w, err)
				return
			}
		}
		if request.PartialImages != nil && *request.PartialImages > 0 {
			writeError(w, http.StatusBadGateway, "streaming_unsupported", "partial image edit streaming is not supported by the selected deployment policy")
			return
		}
		request.Stream = false
		request.PartialImages = nil
		reqCtx.ImageEditRequest = &request
		response, err := imageProvider.EditImage(r.Context(), reqCtx)
		if err != nil {
			writeProviderFailure(w, err)
			return
		}
		payload, err := imageCompletedPayload(response, "image_edit.completed")
		if err != nil {
			writeError(w, http.StatusBadGateway, "provider_failed", err.Error())
			return
		}
		_ = writeStreamPayload(payload)
		return
	}
	response, err := imageProvider.EditImage(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) CreateImageVariation(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeImageVariationRequest(w, r)
	if !ok {
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		ImageVariationRequest: &request,
		Request:               openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model},
		Metadata:              map[string]string{"gateway.api_type": "image_variation"},
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
	if reqCtx.ImageVariationRequest == nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	request = *reqCtx.ImageVariationRequest
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadGateway, "module_failed", "module returned an invalid image variation request")
		return
	}
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateImageVariationTokens(request)) || !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	imageProvider, ok := h.provider.(provider.ImageVariationProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "image variations are not supported by the configured provider")
		return
	}
	response, err := imageProvider.CreateImageVariation(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) TranscribeAudio(w http.ResponseWriter, r *http.Request) {
	h.handleAudio(w, r, false)
}

func (h Handler) TranslateAudio(w http.ResponseWriter, r *http.Request) {
	h.handleAudio(w, r, true)
}

func (h Handler) handleAudio(w http.ResponseWriter, r *http.Request, translation bool) {
	request, ok := decodeAudioTranscriptionRequest(w, r)
	if !ok {
		return
	}
	if translation && request.Stream {
		writeError(w, http.StatusBadRequest, "invalid_request", "stream is not supported for audio translations")
		return
	}
	apiType := "audio_transcription"
	if translation {
		apiType = "audio_translation"
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		AudioTranscriptionRequest: &request,
		Request:                   openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model},
		Metadata:                  map[string]string{"gateway.api_type": apiType},
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
	if reqCtx.AudioTranscriptionRequest == nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	request = *reqCtx.AudioTranscriptionRequest
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadGateway, "module_failed", "module returned an invalid audio transcription request")
		return
	}
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateAudioTranscriptionTokens(request)) || !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	var response openai.AudioTranscriptionResponse
	var err error
	if translation {
		audioProvider, ok := h.provider.(provider.AudioTranslationProvider)
		if !ok {
			writeError(w, http.StatusBadGateway, "provider_failed", "audio translation is not supported by the configured provider")
			return
		}
		response, err = audioProvider.TranslateAudio(r.Context(), reqCtx)
	} else {
		audioProvider, ok := h.provider.(provider.AudioTranscriptionProvider)
		if !ok {
			writeError(w, http.StatusBadGateway, "provider_failed", "audio transcription is not supported by the configured provider")
			return
		}
		if request.Stream {
			streamStarted := false
			writeStreamPayload := func(payload string) error {
				if !streamStarted {
					writeStreamHeaders(w)
					w.WriteHeader(http.StatusOK)
					streamStarted = true
				}
				return writeSSEPayload(w, payload)
			}
			if streamingProvider, supported := h.provider.(provider.StreamingAudioTranscriptionProvider); supported {
				if streamResponse, streamed, streamErr := streamingProvider.StreamTranscribeAudio(r.Context(), reqCtx, writeStreamPayload); streamed {
					if streamErr != nil {
						if streamStarted {
							_ = writeStreamPayload(errorStreamPayload(streamErr))
							return
						}
						writeProviderFailure(w, streamErr)
						return
					}
					if sink, ok := w.(interface {
						audioTranscriptionStreamResult(openai.AudioTranscriptionResponse)
					}); ok {
						sink.audioTranscriptionStreamResult(streamResponse)
					}
					return
				} else if streamErr != nil {
					writeProviderFailure(w, streamErr)
					return
				}
			}
			request.Stream = false
			reqCtx.AudioTranscriptionRequest = &request
			response, err = audioProvider.TranscribeAudio(r.Context(), reqCtx)
			if err != nil {
				writeProviderFailure(w, err)
				return
			}
			payload, err := audioTranscriptionDonePayload(response)
			if err != nil {
				writeError(w, http.StatusBadGateway, "provider_failed", err.Error())
				return
			}
			_ = writeStreamPayload(payload)
			return
		}
		response, err = audioProvider.TranscribeAudio(r.Context(), reqCtx)
	}
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func audioTranscriptionDonePayload(response openai.AudioTranscriptionResponse) (string, error) {
	if message := response.Validate(); message != "" {
		return "", errors.New(message)
	}
	payload, err := json.Marshal(map[string]any{
		"type": "transcript.text.done", "text": response.Text,
		"languages": response.Languages, "logprobs": response.Logprobs, "usage": response.Usage,
	})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (h Handler) GenerateSpeech(w http.ResponseWriter, r *http.Request) {
	var request openai.AudioSpeechRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		AudioSpeechRequest: &request,
		InputCharacters:    request.InputCharacters(),
		Request:            openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model, Messages: []openai.Message{{Role: "user", Content: request.Input}}},
		Metadata:           map[string]string{"gateway.api_type": "audio_speech"},
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
	if reqCtx.AudioSpeechRequest == nil || len(reqCtx.Request.Messages) != 1 {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	request = *reqCtx.AudioSpeechRequest
	request.Input = openai.ContentText(reqCtx.Request.Messages[0].Content)
	reqCtx.AudioSpeechRequest = &request
	reqCtx.InputCharacters = request.InputCharacters()
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadGateway, "module_failed", "module returned an invalid audio speech request")
		return
	}
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, estimateAudioSpeechTokens(request)) || !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	audioProvider, ok := h.provider.(provider.AudioSpeechProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "audio speech is not supported by the configured provider")
		return
	}
	if request.StreamFormat == "sse" {
		streamingProvider, supported := h.provider.(provider.StreamingAudioSpeechProvider)
		if !supported {
			writeError(w, http.StatusBadGateway, "streaming_unsupported", "SSE audio speech is not supported by the configured provider")
			return
		}
		streamStarted := false
		writeStreamPayload := func(payload string) error {
			if !streamStarted {
				writeStreamHeaders(w)
				w.WriteHeader(http.StatusOK)
				streamStarted = true
			}
			return writeSSEPayload(w, payload)
		}
		response, streamed, err := streamingProvider.StreamGenerateSpeech(r.Context(), reqCtx, writeStreamPayload)
		if !streamed {
			if err != nil {
				writeProviderFailure(w, err)
			} else {
				writeError(w, http.StatusBadGateway, "streaming_unsupported", "SSE audio speech is not supported by the selected deployment policy")
			}
			return
		}
		if err != nil {
			if streamStarted {
				_ = writeStreamPayload(errorStreamPayload(err))
				return
			}
			writeProviderFailure(w, err)
			return
		}
		if sink, ok := w.(interface {
			audioSpeechStreamResult(openai.AudioSpeechResponse)
		}); ok {
			sink.audioSpeechStreamResult(response)
		}
		return
	}
	response, err := audioProvider.GenerateSpeech(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", response.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(response.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response.Data)
}

func (h Handler) Search(w http.ResponseWriter, r *http.Request) {
	var request openai.SearchRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	model, _ := request.RoutingModel()
	queries, _ := request.Queries()
	messages := make([]openai.Message, len(queries))
	for index, query := range queries {
		messages[index] = openai.Message{Role: "user", Content: query}
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		SearchRequest: &request,
		Request:       openai.ChatCompletionRequest{Provider: request.Provider, Model: model, Messages: messages},
		Metadata:      map[string]string{"gateway.api_type": "search"},
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
	if reqCtx.SearchRequest == nil || len(reqCtx.Request.Messages) != len(queries) {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	request = *reqCtx.SearchRequest
	queries = make([]string, len(reqCtx.Request.Messages))
	for index := range reqCtx.Request.Messages {
		queries[index] = openai.ContentText(reqCtx.Request.Messages[index].Content)
	}
	request.SetQueries(queries)
	reqCtx.SearchRequest = &request
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadGateway, "module_failed", "module returned an invalid search request")
		return
	}
	model, _ = request.RoutingModel()
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeAccess(w, r.Context(), reqCtx, model, openai.SearchReserveTokens(request)) || !h.prepareModelFallbacks(w, r.Context(), &reqCtx, model) {
		return
	}
	searchProvider, ok := h.provider.(provider.SearchProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "search is not supported by the configured provider")
		return
	}
	response, err := searchProvider.Search(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) OCR(w http.ResponseWriter, r *http.Request) {
	var request openai.OCRRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		OCRRequest: &request,
		InputPages: request.ReservePages(),
		Request:    openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model},
		Metadata:   map[string]string{"gateway.api_type": "ocr"},
	}
	if request.Document.Type == "file" {
		identity := reqCtx
		if err := h.pipeline.RunAuthentication(r.Context(), &identity); err != nil {
			if errors.Is(err, modules.ErrUnauthorized) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
				return
			}
			writeError(w, http.StatusBadGateway, "module_failed", "authentication failed")
			return
		}
		resolved, err := h.resolveOCRFile(r.Context(), fileOwnerKey(identity), request.Document.FileID)
		if err != nil {
			writeOCRFileError(w, err)
			return
		}
		request.Document = resolved
		reqCtx.OCRRequest = &request
		reqCtx.InputPages = request.ReservePages()
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
	if reqCtx.OCRRequest == nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	request = *reqCtx.OCRRequest
	reqCtx.InputPages = request.ReservePages()
	if request.Document.Type == "file" {
		writeError(w, http.StatusBadGateway, "module_failed", "module returned an unresolved OCR file reference")
		return
	}
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadGateway, "module_failed", "module returned an invalid OCR request")
		return
	}
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeAccess(w, r.Context(), reqCtx, request.Model, request.InputTokens()) || !h.prepareModelFallbacks(w, r.Context(), &reqCtx, request.Model) {
		return
	}
	ocrProvider, ok := h.provider.(provider.OCRProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "OCR is not supported by the configured provider")
		return
	}
	response, err := ocrProvider.OCR(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func validateRerankRequest(request openai.RerankRequest) string {
	if strings.TrimSpace(request.Model) == "" {
		return "model is required"
	}
	if strings.TrimSpace(request.Query) == "" {
		return "query is required"
	}
	if len(request.Documents) == 0 || len(request.Documents) > 1000 {
		return "documents must contain between 1 and 1000 items"
	}
	if request.TopN != nil && (*request.TopN <= 0 || *request.TopN > len(request.Documents)) {
		return "top_n must be between 1 and the number of documents"
	}
	if request.MaxChunksPerDoc != nil && *request.MaxChunksPerDoc <= 0 {
		return "max_chunks_per_doc must be positive"
	}
	if request.MaxTokensPerDoc != nil && *request.MaxTokensPerDoc <= 0 {
		return "max_tokens_per_doc must be positive"
	}
	if len(request.RankFields) > 32 {
		return "rank_fields must not contain more than 32 fields"
	}
	seen := map[string]bool{}
	for _, field := range request.RankFields {
		field = strings.TrimSpace(field)
		if field == "" || seen[field] {
			return "rank_fields must contain unique non-empty fields"
		}
		seen[field] = true
	}
	text, ok := openai.RerankDocumentText(request)
	if !ok {
		return "documents must be non-empty strings or objects containing text in rank_fields (default: text)"
	}
	if len(text) > 4<<20 {
		return "query and document text exceed the rerank limit"
	}
	return ""
}

func decodeInferenceRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, openai.MaxInferenceBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the inference limit")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain exactly one JSON value")
		return false
	}
	return true
}

func writeProviderFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, provider.ErrCompletionsUnsupported) {
		writeProviderParameterError(w, http.StatusBadRequest, "unsupported_operation", "text completions are not supported by the selected deployment", "")
		return
	}
	if errors.Is(err, provider.ErrResponseCompactionUnsupported) {
		writeProviderParameterError(w, http.StatusBadRequest, "unsupported_operation", "response compaction is not supported by the selected deployment", "")
		return
	}
	if errors.Is(err, provider.ErrResponseCompactionAnonymized) {
		writeProviderParameterError(w, http.StatusBadRequest, "unsupported_operation", "response compaction is incompatible with an anonymizing policy", "")
		return
	}
	if errors.Is(err, provider.ErrResponseInputTokenCountUnsupported) {
		writeProviderParameterError(w, http.StatusBadRequest, "unsupported_operation", "response input token counting is not supported by the selected deployment", "")
		return
	}
	if errors.Is(err, provider.ErrResponseNotFound) {
		writeError(w, http.StatusNotFound, "response_not_found", "response not found")
		return
	}
	if errors.Is(err, provider.ErrResponseDeploymentChanged) {
		writeError(w, http.StatusConflict, "response_deployment_changed", "response deployment has changed")
		return
	}
	if errors.Is(err, provider.ErrResponseAffinityUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "response_affinity_unavailable", "response session storage is unavailable")
		return
	}
	if errors.Is(err, provider.ErrResponseOwnershipUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "response_ownership_unavailable", "response ownership storage is unavailable")
		return
	}
	if errors.Is(err, provider.ErrResponseOwnershipConflict) {
		writeError(w, http.StatusConflict, "response_ownership_conflict", "response ownership conflict")
		return
	}
	if errors.Is(err, provider.ErrBackgroundResponseStorageUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "background_response_unavailable", "background response storage is unavailable")
		return
	}
	if errors.Is(err, provider.ErrBackgroundResponsesUnsupported) {
		writeProviderParameterError(w, http.StatusBadRequest, "unsupported_operation", "background responses are not supported by the selected deployment", "background")
		return
	}
	if errors.Is(err, modules.ErrContentRejected) {
		writeError(w, http.StatusUnavailableForLegalReasons, "content_rejected", "content rejected")
		return
	}
	if errors.Is(err, modules.ErrGuardrailUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "guardrail_unavailable", "required content policy service is unavailable")
		return
	}
	if errors.Is(err, modules.ErrBudgetExceeded) {
		writeError(w, http.StatusTooManyRequests, "budget_exceeded", "budget exceeded")
		return
	}
	if errors.Is(err, modules.ErrBillingConflict) {
		writeError(w, http.StatusConflict, "billing_conflict", "billing lifecycle conflict")
		return
	}
	var accountQuotaErr *provider.ProviderQuotaError
	if errors.As(err, &accountQuotaErr) {
		seconds := max(1, int((accountQuotaErr.RetryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests, "provider_rate_limit_exceeded", "provider rate limit exceeded")
		return
	}
	var quotaErr *provider.DeploymentQuotaError
	if errors.As(err, &quotaErr) {
		seconds := max(1, int((quotaErr.RetryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests, "deployment_rate_limit_exceeded", "deployment rate limit exceeded")
		return
	}
	var admissionErr *provider.AdmissionError
	if errors.As(err, &admissionErr) {
		seconds := int((admissionErr.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests, "provider_busy", "provider capacity is temporarily exhausted")
		return
	}
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		switch providerErr.Class {
		case provider.FailureClientRequest:
			code := providerErr.UpstreamCode
			if code == "" {
				code = "provider_invalid_request"
			}
			message := "provider rejected the request"
			if providerErr.Param != "" {
				message = "provider rejected parameter " + providerErr.Param
			}
			writeProviderParameterError(w, providerErr.StatusCode, code, message, providerErr.Param)
			return
		case provider.FailureContextLength:
			code := providerErr.UpstreamCode
			if code == "" {
				code = "context_length_exceeded"
			}
			writeProviderParameterError(w, http.StatusBadRequest, code, "request exceeds the model context window", providerErr.Param)
			return
		case provider.FailureContentPolicy:
			writeError(w, http.StatusUnavailableForLegalReasons, "provider_content_policy", "upstream provider rejected the request under its content policy")
			return
		}
	}
	writeError(w, http.StatusBadGateway, "provider_failed", err.Error())
}

func writeProviderParameterError(w http.ResponseWriter, status int, code, message, param string) {
	if status < 400 || status >= 500 {
		status = http.StatusBadRequest
	}
	detail := map[string]any{"code": code, "message": message}
	if param != "" {
		detail["param"] = param
	}
	writeJSON(w, status, map[string]any{"error": detail, "ts": time.Now().UTC().Format(time.RFC3339)})
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

func requestID(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); value != "" && len(value) <= 128 {
		return value
	}
	return newExecutionID()
}

func executionID(w http.ResponseWriter) string {
	id := newExecutionID()
	w.Header().Set("X-Execution-ID", id)
	return id
}

func newExecutionID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UTC().UnixNano())
	}
	return fmt.Sprintf("%x", value[:])
}

func sessionID(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("X-Session-ID"))
	if value != "" && len(value) <= 128 {
		return value
	}
	return ""
}

func responseMessages(request openai.ResponseRequest) []openai.Message {
	var messages []openai.Message
	if request.Instructions != "" {
		messages = append(messages, openai.Message{Role: "system", Content: request.Instructions})
	}
	messages = append(messages, openai.Message{Role: "user", Content: responseInputText(request.Input)})
	return messages
}

func responseInputText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, responseInputText(item))
		}
		return strings.Join(parts, " ")
	case map[string]any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, responseInputText(item))
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
		"ts": time.Now().UTC().Format(time.RFC3339),
	})
}

func writeChatCompletionStream(w http.ResponseWriter, response openai.ChatCompletionResponse, options *openai.ChatStreamOptions) {
	writeStreamHeaders(w)
	w.WriteHeader(http.StatusOK)
	created := response.Created
	if created == 0 {
		created = time.Now().UTC().Unix()
	}
	envelope := func() map[string]any {
		chunk := map[string]any{
			"id": response.ID, "object": "chat.completion.chunk", "model": response.Model, "created": created,
		}
		if len(response.Metadata) > 0 {
			chunk["metadata"] = response.Metadata
		}
		if response.ServiceTier != "" {
			chunk["service_tier"] = response.ServiceTier
		}
		if response.SystemFingerprint != "" {
			chunk["system_fingerprint"] = response.SystemFingerprint
		}
		return chunk
	}

	for _, choice := range response.Choices {
		calls := append([]openai.ToolCall(nil), choice.Message.ToolCalls...)
		for index := range calls {
			calls[index].Index = &index
		}
		content := envelope()
		content["choices"] = []map[string]any{
			{
				"index":    choice.Index,
				"logprobs": choice.Logprobs,
				"delta": map[string]any{
					"role":          choice.Message.Role,
					"content":       openai.ContentText(choice.Message.Content),
					"refusal":       choice.Message.Refusal,
					"audio":         choice.Message.Audio,
					"function_call": choice.Message.FunctionCall,
					"tool_calls":    calls,
				},
				"finish_reason": nil,
			},
		}
		writeChatSSE(w, content, options)
		finished := envelope()
		finished["choices"] = []map[string]any{
			{
				"index":         choice.Index,
				"delta":         map[string]any{},
				"finish_reason": choice.FinishReason,
				"stop_sequence": choice.StopSequence,
			},
		}
		writeChatSSE(w, finished, options)
	}

	if options != nil && options.IncludeUsage {
		writeSSEPayload(w, chatCompletionUsagePayload(response))
	}
	writeSSEDone(w)
}

func transformChatStreamPayload(payload string, options *openai.ChatStreamOptions, includeUsage bool) (string, bool, bool, error) {
	var chunk map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return payload, false, true, nil
	}
	usage, usagePresent := chunk["usage"]
	hasUsage := usagePresent && string(usage) != "null"
	if includeUsage || !usagePresent {
		return transformChatObfuscation(chunk, options, hasUsage)
	}
	delete(chunk, "usage")
	var choices []json.RawMessage
	if raw, ok := chunk["choices"]; ok && json.Unmarshal(raw, &choices) == nil && len(choices) == 0 {
		return "", true, false, nil
	}
	return transformChatObfuscation(chunk, options, true)
}

func transformChatObfuscation(chunk map[string]json.RawMessage, options *openai.ChatStreamOptions, hasUsage bool) (string, bool, bool, error) {
	include := true
	if options != nil && options.IncludeObfuscation != nil {
		include = *options.IncludeObfuscation
	}
	delete(chunk, "obfuscation")
	var choices []json.RawMessage
	deltaEvent := json.Unmarshal(chunk["choices"], &choices) == nil && len(choices) > 0
	if include && deltaEvent {
		encoded, err := json.Marshal(chunk)
		if err != nil {
			return "", hasUsage, true, err
		}
		padding := 256 - ((len(encoded) + len(`,"obfuscation":""`)) % 256)
		if padding < 16 {
			padding += 256
		}
		random := make([]byte, padding)
		if _, err := rand.Read(random); err != nil {
			return "", hasUsage, true, err
		}
		const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		for index := range random {
			random[index] = alphabet[int(random[index])%len(alphabet)]
		}
		value, _ := json.Marshal(string(random))
		chunk["obfuscation"] = value
	}
	encoded, err := json.Marshal(chunk)
	return string(encoded), hasUsage, true, err
}

func writeChatSSE(w http.ResponseWriter, payload map[string]any, options *openai.ChatStreamOptions) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	transformed, _, deliver, err := transformChatStreamPayload(string(encoded), options, true)
	if err == nil && deliver {
		_ = writeSSEPayload(w, transformed)
	}
}

func chatCompletionUsagePayload(response openai.ChatCompletionResponse) string {
	created := response.Created
	if created == 0 {
		created = time.Now().UTC().Unix()
	}
	payload := map[string]any{"id": response.ID, "object": "chat.completion.chunk", "model": response.Model, "created": created, "choices": []any{}, "usage": response.Usage}
	if len(response.Metadata) > 0 {
		payload["metadata"] = response.Metadata
	}
	if response.ServiceTier != "" {
		payload["service_tier"] = response.ServiceTier
	}
	if response.SystemFingerprint != "" {
		payload["system_fingerprint"] = response.SystemFingerprint
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func writeCompletionStream(w http.ResponseWriter, response openai.CompletionResponse) {
	writeStreamHeaders(w)
	w.WriteHeader(http.StatusOK)
	for _, choice := range response.Choices {
		content := map[string]any{
			"id": response.ID, "object": "text_completion", "created": response.Created, "model": response.Model,
			"choices": []map[string]any{{
				"index": choice.Index, "text": choice.Text, "logprobs": choice.Logprobs, "finish_reason": nil,
			}},
		}
		finished := map[string]any{
			"id": response.ID, "object": "text_completion", "created": response.Created, "model": response.Model,
			"choices": []map[string]any{{
				"index": choice.Index, "text": "", "logprobs": nil, "finish_reason": choice.FinishReason,
			}},
		}
		if response.SystemFingerprint != "" {
			content["system_fingerprint"] = response.SystemFingerprint
			finished["system_fingerprint"] = response.SystemFingerprint
		}
		writeSSE(w, content)
		writeSSE(w, finished)
	}
	writeSSEDone(w)
}

func writeSSE(w http.ResponseWriter, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func writeSSEPayload(w http.ResponseWriter, payload string) error {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeSSEResponseEvent(w http.ResponseWriter, event string, payload string) error {
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	return writeSSEPayload(w, payload)
}

func writeSSEDone(w http.ResponseWriter) {
	_ = writeSSEPayload(w, "[DONE]")
}

func errorStreamPayload(err error) string {
	payload, marshalErr := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    "provider_failed",
			"message": err.Error(),
		},
	})
	if marshalErr != nil {
		return `{"error":{"code":"provider_failed","message":"stream failed"}}`
	}
	return string(payload)
}
