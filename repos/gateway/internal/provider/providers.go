package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/openai"
)

type ManagedProvider struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	BaseURL      string `json:"base_url,omitempty"`
	APIVersion   string `json:"api_version,omitempty"`
	AuthType     string `json:"auth_type,omitempty"`
	Region       string `json:"region,omitempty"`
	RateLimitRPM int    `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM int    `json:"rate_limit_tpm,omitempty"`
	Enabled      bool   `json:"enabled"`
}

type ProviderCapabilityProfile struct {
	Type                 string                            `json:"type"`
	Operations           []string                          `json:"operations"`
	Capabilities         []string                          `json:"capabilities"`
	AuthTypes            []string                          `json:"auth_types"`
	ChatParameters       ProviderChatParameterPolicy       `json:"chat_parameters"`
	ResponseParameters   ProviderResponseParameterPolicy   `json:"response_parameters"`
	EmbeddingParameters  ProviderEmbeddingParameterPolicy  `json:"embedding_parameters"`
	RerankParameters     ProviderRerankParameterPolicy     `json:"rerank_parameters"`
	CompletionParameters ProviderCompletionParameterPolicy `json:"completion_parameters"`
	ModerationParameters ProviderModerationParameterPolicy `json:"moderation_parameters"`
}

type ProviderChatParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	ReasoningEffort  []string `json:"reasoning_effort"`
	Logprobs         []string `json:"logprobs"`
	ServiceTier      []string `json:"service_tier"`
}

type ProviderResponseParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	ReasoningEffort  []string `json:"reasoning_effort"`
	ServiceTier      []string `json:"service_tier"`
}

type ProviderEmbeddingParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	InputForms       []string `json:"input_forms"`
	InputTypes       []string `json:"input_types"`
	EncodingFormats  []string `json:"encoding_formats"`
	OutputDTypes     []string `json:"output_dtypes"`
}

type ProviderRerankParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	DocumentForms    []string `json:"document_forms"`
}

type ProviderCompletionParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	PromptForms      []string `json:"prompt_forms"`
}

type ProviderModerationParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	InputForms       []string `json:"input_forms"`
}

var managedProviderTypes = []string{"demo", "ollama", "openai", "openai-compatible", "openrouter", "azure-openai", "anthropic", "gemini", "cohere", "mistral", "voyage", "bedrock", "groq", "deepseek", "xai", "opensandbox"}

var managedOperationCapabilities = []string{
	"chat", "completions", "responses", "interactions", "count_tokens", "embeddings", "rerank", "moderation",
	"image_generation", "image_edit", "image_variation",
	"audio_transcription", "audio_translation", "audio_speech", "ocr", "search", "skills", "fine_tuning", "video", "video_remix", "video_extension", "container", "container_files", "container_network", "sandbox", "realtime", "stream", "bedrock_invoke",
}

var managedFeatureCapabilities = []string{
	"tools", "structured_output", "mcp", "vision", "web_search", "audio_input", "video_input",
	"web_fetch", "audio", "prompt_cache", "assistant_prefill",
	"background_responses", "background_interactions", "file_input", "interaction_agents", "interaction_environment_reuse", "gemini_safety_settings",
}

type ProviderController interface {
	ListProviders(context.Context) []ManagedProvider
	CreateProvider(ManagedProvider) (ManagedProvider, error)
	UpdateProvider(string, ManagedProvider) (ManagedProvider, error)
	DeleteProvider(string) error
}

var (
	ErrProviderNotFound = errors.New("provider not found")
	ErrProviderExists   = errors.New("provider already exists")
	ErrProviderInUse    = errors.New("provider is in use")
	ErrInvalidProvider  = errors.New("invalid provider")
)

type managedProviderRegistry struct {
	current atomic.Pointer[map[string]ManagedProvider]
}

func (r *Router) ListProviders(ctx context.Context) []ManagedProvider {
	_ = r.refreshControlPlane(ctx)
	if r == nil || r.providers == nil || r.providers.current.Load() == nil {
		return nil
	}
	current := r.providers.current.Load()
	result := make([]ManagedProvider, 0, len(*current))
	for _, item := range *current {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *Router) CreateProvider(input ManagedProvider) (ManagedProvider, error) {
	previous, unlock, err := r.beginControlMutation(context.Background())
	if err != nil {
		return ManagedProvider{}, err
	}
	defer unlock()
	provider, err := normalizeManagedProvider(input)
	if err != nil {
		return ManagedProvider{}, err
	}
	current := r.providers.current.Load()
	if _, found := (*current)[provider.ID]; found {
		return ManagedProvider{}, ErrProviderExists
	}
	next := cloneProviders(*current)
	next[provider.ID] = provider
	r.providers.current.Store(&next)
	if err := r.persistControlMutation(context.Background(), previous); err != nil {
		return ManagedProvider{}, err
	}
	return provider, nil
}

func (r *Router) UpdateProvider(id string, input ManagedProvider) (ManagedProvider, error) {
	previous, unlock, err := r.beginControlMutation(context.Background())
	if err != nil {
		return ManagedProvider{}, err
	}
	defer unlock()
	id = strings.TrimSpace(id)
	current := r.providers.current.Load()
	if _, found := (*current)[id]; !found {
		return ManagedProvider{}, ErrProviderNotFound
	}
	input.ID = id
	provider, err := normalizeManagedProvider(input)
	if err != nil {
		return ManagedProvider{}, err
	}
	if err := r.validateCredentialsForProvider(provider); err != nil {
		return ManagedProvider{}, err
	}
	rebuilt := make([]Endpoint, 0)
	if deployments := r.deployments.current.Load(); deployments != nil {
		for _, deployment := range *deployments {
			if deployment.ProviderID != id {
				continue
			}
			endpoint, buildErr := r.endpointForManagedDeployment(deployment, provider)
			if buildErr != nil {
				return ManagedProvider{}, buildErr
			}
			rebuilt = append(rebuilt, endpoint)
		}
	}
	next := cloneProviders(*current)
	next[id] = provider
	r.providers.current.Store(&next)
	for _, endpoint := range rebuilt {
		r.replaceRuntimeEndpoint(endpoint.Name, endpoint)
	}
	if err := r.persistControlMutation(context.Background(), previous); err != nil {
		return ManagedProvider{}, err
	}
	return provider, nil
}

func (r *Router) DeleteProvider(id string) error {
	previous, unlock, err := r.beginControlMutation(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	id = strings.TrimSpace(id)
	current := r.providers.current.Load()
	if _, found := (*current)[id]; !found {
		return ErrProviderNotFound
	}
	if deployments := r.deployments.current.Load(); deployments != nil {
		for _, deployment := range *deployments {
			if deployment.ProviderID == id {
				return ErrProviderInUse
			}
		}
	}
	next := cloneProviders(*current)
	delete(next, id)
	r.providers.current.Store(&next)
	r.dropAWSCredentialSources(id, "")
	return r.persistControlMutation(context.Background(), previous)
}

func normalizeManagedProvider(input ManagedProvider) (ManagedProvider, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.APIVersion = strings.TrimSpace(input.APIVersion)
	input.AuthType = normalizeAzureAuthType(input.AuthType)
	input.Region = strings.ToLower(strings.TrimSpace(input.Region))
	if input.ID == "" || len(input.ID) > 128 || !validProviderType(input.Type) || len(input.BaseURL) > 2048 || input.RateLimitRPM < 0 || input.RateLimitRPM > 10000000 || input.RateLimitTPM < 0 || input.RateLimitTPM > 1000000000 {
		return ManagedProvider{}, ErrInvalidProvider
	}
	if input.Type != "demo" {
		parsed, err := url.ParseRequestURI(input.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return ManagedProvider{}, ErrInvalidProvider
		}
	}
	if input.Type == "azure-openai" {
		parsed, _ := url.Parse(input.BaseURL)
		if parsed.RawQuery != "" || parsed.Fragment != "" || !validAzureProviderVersion(input.APIVersion) || (input.AuthType != "api_key" && input.AuthType != "entra") {
			return ManagedProvider{}, ErrInvalidProvider
		}
		input.Region = ""
	} else if input.Type == "gemini" {
		input.APIVersion = ""
		if input.AuthType != "api_key" && input.AuthType != "gcp_adc" {
			return ManagedProvider{}, ErrInvalidProvider
		}
		input.Region = ""
	} else if input.Type == "bedrock" {
		input.APIVersion = ""
		parsed, _ := url.Parse(input.BaseURL)
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return ManagedProvider{}, ErrInvalidProvider
		}
		if input.AuthType == "api_key" {
			input.AuthType = "bearer"
		}
		if input.AuthType != "bearer" && input.AuthType != "aws_sigv4" {
			return ManagedProvider{}, ErrInvalidProvider
		}
		if input.AuthType == "aws_sigv4" && !validBedrockRegion(input.Region) {
			return ManagedProvider{}, ErrInvalidProvider
		}
		if input.AuthType == "bearer" {
			input.Region = ""
		}
	} else if input.Type == "opensandbox" {
		input.APIVersion = ""
		input.Region = ""
		if input.AuthType == "" {
			input.AuthType = "api_key"
		}
		if input.AuthType != "api_key" {
			return ManagedProvider{}, ErrInvalidProvider
		}
	} else {
		input.APIVersion = ""
		input.AuthType = ""
		input.Region = ""
	}
	return input, nil
}

func validBedrockRegion(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func validProviderType(value string) bool {
	for _, providerType := range managedProviderTypes {
		if value == providerType {
			return true
		}
	}
	return false
}

func ManagedProviderCapabilityProfiles() []ProviderCapabilityProfile {
	profiles := make([]ProviderCapabilityProfile, 0, len(managedProviderTypes))
	for _, providerType := range managedProviderTypes {
		client := providerFor(config.ProviderEndpointConfig{Type: providerType, Stream: true})
		available := append(append([]string(nil), managedOperationCapabilities...), managedFeatureCapabilities...)
		endpoint := Endpoint{Type: providerType, Provider: client, Capabilities: available}
		operations := make([]string, 0, len(managedOperationCapabilities))
		for _, capability := range managedOperationCapabilities {
			if supportsManagedAdapterCapability(endpoint, capability) {
				operations = append(operations, capability)
			}
		}
		capabilities := make([]string, 0, len(operations)+len(managedFeatureCapabilities))
		for _, operation := range operations {
			if operation != "count_tokens" {
				capabilities = append(capabilities, operation)
			}
		}
		for _, capability := range managedFeatureCapabilities {
			if supportsManagedAdapterCapability(endpoint, capability) {
				capabilities = append(capabilities, capability)
			}
		}
		profiles = append(profiles, ProviderCapabilityProfile{
			Type:                 providerType,
			Operations:           operations,
			Capabilities:         capabilities,
			AuthTypes:            managedProviderAuthTypes(providerType),
			ChatParameters:       managedProviderChatParameterPolicy(client, slicesContain(operations, "chat")),
			ResponseParameters:   managedProviderResponseParameterPolicy(client, slicesContain(operations, "responses")),
			EmbeddingParameters:  managedProviderEmbeddingParameterPolicy(client, slicesContain(operations, "embeddings")),
			RerankParameters:     managedProviderRerankParameterPolicy(client, slicesContain(operations, "rerank")),
			CompletionParameters: managedProviderCompletionParameterPolicy(client, slicesContain(operations, "completions")),
			ModerationParameters: managedProviderModerationParameterPolicy(client, slicesContain(operations, "moderation")),
		})
	}
	return profiles
}

func managedProviderModerationParameterPolicy(client Client, supported bool) ProviderModerationParameterPolicy {
	policy := ProviderModerationParameterPolicy{SupportedOptions: []string{}, InputForms: []string{}}
	moderationClient, ok := client.(ModerationClient)
	if !supported || !ok {
		return policy
	}
	for _, probe := range []struct {
		name  string
		input any
	}{
		{"text", "test"},
		{"text_array", []string{"test"}},
		{"content_parts", []any{map[string]any{"type": "text", "text": "test"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image.png"}}}},
	} {
		request := openai.ModerationRequest{Model: "model", Input: probe.input}
		if _, err := openai.InspectModerationInput(request.Input); err == nil && validateModerationAdapter(moderationClient, request) == nil {
			policy.InputForms = append(policy.InputForms, probe.name)
		}
	}
	request := openai.ModerationRequest{Model: "model", Input: "test", Metadata: map[string]string{"trace": "probe"}}
	if validateModerationAdapter(moderationClient, request) == nil {
		policy.SupportedOptions = append(policy.SupportedOptions, "metadata")
	}
	return policy
}

func managedProviderCompletionParameterPolicy(client Client, supported bool) ProviderCompletionParameterPolicy {
	policy := ProviderCompletionParameterPolicy{SupportedOptions: []string{}, PromptForms: []string{}}
	completionClient, ok := client.(CompletionClient)
	if !supported || !ok {
		return policy
	}
	baseline := openai.CompletionRequest{Model: "model", Prompt: "test"}
	for _, probe := range []struct {
		name   string
		prompt any
	}{{"text", "test"}, {"text_array", []string{"test"}}, {"token_array", []int{1}}, {"token_batch", [][]int{{1}}}} {
		request := baseline
		request.Prompt = probe.prompt
		if validateCompletionAdapter(completionClient, request) == nil {
			policy.PromptForms = append(policy.PromptForms, probe.name)
		}
	}
	for _, probe := range []struct {
		name  string
		apply func(*openai.CompletionRequest)
	}{
		{"metadata", func(r *openai.CompletionRequest) { r.Metadata = map[string]string{"trace": "probe"} }},
		{"best_of", func(r *openai.CompletionRequest) { value := 1; r.BestOf = &value }},
		{"echo", func(r *openai.CompletionRequest) { value := true; r.Echo = &value }},
		{"frequency_penalty", func(r *openai.CompletionRequest) { value := 0.5; r.FrequencyPenalty = &value }},
		{"logit_bias", func(r *openai.CompletionRequest) { r.LogitBias = map[string]int{"1": 1} }},
		{"logprobs", func(r *openai.CompletionRequest) { value := 1; r.Logprobs = &value }},
		{"max_tokens", func(r *openai.CompletionRequest) { value := 16; r.MaxTokens = &value }},
		{"min_tokens", func(r *openai.CompletionRequest) { value := 1; r.MinTokens = &value }},
		{"n", func(r *openai.CompletionRequest) { value := 1; r.N = &value }},
		{"presence_penalty", func(r *openai.CompletionRequest) { value := 0.5; r.PresencePenalty = &value }},
		{"prompt_cache_key", func(r *openai.CompletionRequest) { r.PromptCacheKey = "probe" }},
		{"seed", func(r *openai.CompletionRequest) { value := int64(1); r.Seed = &value }},
		{"stop", func(r *openai.CompletionRequest) { r.Stop = []string{"stop"} }},
		{"suffix", func(r *openai.CompletionRequest) { r.Suffix = "suffix" }},
		{"temperature", func(r *openai.CompletionRequest) { value := 0.5; r.Temperature = &value }},
		{"top_p", func(r *openai.CompletionRequest) { value := 0.5; r.TopP = &value }},
		{"user", func(r *openai.CompletionRequest) { r.User = "probe" }},
	} {
		request := baseline
		probe.apply(&request)
		if validateCompletionAdapter(completionClient, request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	return policy
}

func managedProviderEmbeddingParameterPolicy(client Client, supported bool) ProviderEmbeddingParameterPolicy {
	policy := ProviderEmbeddingParameterPolicy{SupportedOptions: []string{}, InputForms: []string{}, InputTypes: []string{}, EncodingFormats: []string{}, OutputDTypes: []string{}}
	if !supported {
		return policy
	}
	baseline := openai.EmbeddingRequest{Model: "model", Input: "test"}
	for _, value := range []string{"query", "document", "search_query", "search_document", "classification", "clustering"} {
		request := baseline
		request.InputType = value
		if validateEmbeddingAdapter(client, request) == nil {
			policy.InputTypes = append(policy.InputTypes, value)
		}
	}
	if validateEmbeddingAdapter(client, baseline) != nil && len(policy.InputTypes) > 0 {
		baseline.InputType = policy.InputTypes[0]
	}
	for _, probe := range []struct {
		name  string
		input any
	}{{"text", "test"}, {"text_array", []string{"test"}}, {"token_array", []int{1}}, {"token_batch", [][]int{{1}}}} {
		request := baseline
		request.Input = probe.input
		if validateEmbeddingAdapter(client, request) == nil {
			policy.InputForms = append(policy.InputForms, probe.name)
		}
	}
	for _, value := range []string{"float", "base64"} {
		request := baseline
		request.EncodingFormat = value
		if validateEmbeddingAdapter(client, request) == nil {
			policy.EncodingFormats = append(policy.EncodingFormats, value)
		}
	}
	for _, value := range []string{"float", "int8", "uint8", "binary", "ubinary"} {
		request := baseline
		request.OutputDType = value
		if validateEmbeddingAdapter(client, request) == nil {
			policy.OutputDTypes = append(policy.OutputDTypes, value)
		}
	}
	for _, probe := range []struct {
		name  string
		apply func(*openai.EmbeddingRequest)
	}{{"metadata", func(r *openai.EmbeddingRequest) { r.Metadata = map[string]string{"trace": "probe"} }}, {"dimensions", func(r *openai.EmbeddingRequest) { value := 256; r.Dimensions = &value }}, {"user", func(r *openai.EmbeddingRequest) { r.User = "probe" }}} {
		request := baseline
		probe.apply(&request)
		if validateEmbeddingAdapter(client, request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	if len(policy.InputTypes) > 0 {
		policy.SupportedOptions = append(policy.SupportedOptions, "input_type")
	}
	if len(policy.EncodingFormats) > 0 {
		policy.SupportedOptions = append(policy.SupportedOptions, "encoding_format")
	}
	if len(policy.OutputDTypes) > 0 {
		policy.SupportedOptions = append(policy.SupportedOptions, "output_dtype")
	}
	return policy
}

func managedProviderRerankParameterPolicy(client Client, supported bool) ProviderRerankParameterPolicy {
	policy := ProviderRerankParameterPolicy{SupportedOptions: []string{}, DocumentForms: []string{}}
	if !supported {
		return policy
	}
	baseline := openai.RerankRequest{Model: "model", Query: "query", Documents: []any{"document"}}
	for _, probe := range []struct {
		name      string
		documents []any
	}{{"text", []any{"document"}}, {"object", []any{map[string]any{"text": "document"}}}} {
		request := baseline
		request.Documents = probe.documents
		if validateRerankAdapter(client, request) == nil {
			policy.DocumentForms = append(policy.DocumentForms, probe.name)
		}
	}
	for _, probe := range []struct {
		name  string
		apply func(*openai.RerankRequest)
	}{
		{"top_n", func(r *openai.RerankRequest) { value := 1; r.TopN = &value }},
		{"rank_fields", func(r *openai.RerankRequest) { r.RankFields = []string{"text"} }},
		{"return_documents", func(r *openai.RerankRequest) { value := true; r.ReturnDocuments = &value }},
		{"max_chunks_per_doc", func(r *openai.RerankRequest) { value := 1; r.MaxChunksPerDoc = &value }},
		{"max_tokens_per_doc", func(r *openai.RerankRequest) { value := 128; r.MaxTokensPerDoc = &value }},
	} {
		request := baseline
		probe.apply(&request)
		if validateRerankAdapter(client, request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	return policy
}

func managedProviderResponseParameterPolicy(client Client, supportsResponses bool) ProviderResponseParameterPolicy {
	policy := ProviderResponseParameterPolicy{SupportedOptions: []string{}, ReasoningEffort: []string{}, ServiceTier: []string{}}
	if !supportsResponses {
		return policy
	}
	baseline := openai.ResponseRequest{Model: "model", Input: "test"}
	for _, value := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "default"} {
		request, effort := baseline, value
		request.Reasoning = &openai.ResponseReasoning{Effort: &effort}
		if validateResponseAdapter(client, request) == nil {
			policy.ReasoningEffort = append(policy.ReasoningEffort, value)
		}
	}
	for _, value := range []string{"auto", "default", "on_demand", "flex", "performance", "scale", "priority", "fast", "ultrafast", "standard_only"} {
		request := baseline
		request.ServiceTier = value
		if validateResponseAdapter(client, request) == nil {
			policy.ServiceTier = append(policy.ServiceTier, value)
		}
	}
	for _, probe := range managedResponseOptionProbes() {
		request := baseline
		probe.apply(&request)
		if validateResponseAdapter(client, request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	if len(policy.ReasoningEffort) > 0 {
		policy.SupportedOptions = append(policy.SupportedOptions, "reasoning")
	}
	if len(policy.ServiceTier) > 0 {
		policy.SupportedOptions = append(policy.SupportedOptions, "service_tier")
	}
	return policy
}

type managedResponseOptionProbe struct {
	name  string
	apply func(*openai.ResponseRequest)
}

func managedResponseOptionProbes() []managedResponseOptionProbe {
	return []managedResponseOptionProbe{
		{name: "metadata", apply: func(request *openai.ResponseRequest) { request.Metadata = map[string]string{"trace": "profile-probe"} }},
		{name: "top_logprobs", apply: func(request *openai.ResponseRequest) { value := 1; request.TopLogprobs = &value }},
		{name: "truncation", apply: func(request *openai.ResponseRequest) { value := "auto"; request.Truncation = &value }},
		{name: "store", apply: func(request *openai.ResponseRequest) { value := true; request.Store = &value }},
		{name: "include", apply: func(request *openai.ResponseRequest) { request.Include = []string{"reasoning.encrypted_content"} }},
		{name: "parallel_tool_calls", apply: func(request *openai.ResponseRequest) { value := true; request.ParallelToolCalls = &value }},
		{name: "text.verbosity", apply: func(request *openai.ResponseRequest) { request.Text = map[string]any{"verbosity": "medium"} }},
		{name: "previous_response_id", apply: func(request *openai.ResponseRequest) { request.PreviousResponse = "resp_profile" }},
		{name: "user", apply: func(request *openai.ResponseRequest) { request.User = "profile-probe" }},
		{name: "safety_identifier", apply: func(request *openai.ResponseRequest) { request.SafetyIdentifier = "profile-probe" }},
		{name: "prompt_cache_key", apply: func(request *openai.ResponseRequest) { request.PromptCacheKey = "profile-probe" }},
		{name: "max_output_tokens", apply: func(request *openai.ResponseRequest) { value := 16; request.MaxOutputTokens = &value }},
		{name: "max_tokens", apply: func(request *openai.ResponseRequest) { value := 16; request.MaxTokens = &value }},
		{name: "temperature", apply: func(request *openai.ResponseRequest) { value := 0.5; request.Temperature = &value }},
		{name: "top_p", apply: func(request *openai.ResponseRequest) { value := 0.5; request.TopP = &value }},
		{name: "frequency_penalty", apply: func(request *openai.ResponseRequest) { value := 0.5; request.FrequencyPenalty = &value }},
		{name: "presence_penalty", apply: func(request *openai.ResponseRequest) { value := 0.5; request.PresencePenalty = &value }},
		{name: "max_tool_calls", apply: func(request *openai.ResponseRequest) { value := 1; request.MaxToolCalls = &value }},
	}
}

func managedProviderChatParameterPolicy(client Client, supportsChat bool) ProviderChatParameterPolicy {
	policy := ProviderChatParameterPolicy{SupportedOptions: []string{}, ReasoningEffort: []string{}, Logprobs: []string{}, ServiceTier: []string{}}
	if !supportsChat {
		return policy
	}
	baseline := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "test"}}}
	for _, value := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "default"} {
		request := baseline
		request.ReasoningEffort = value
		if validateChatAdapter(client, request) == nil {
			policy.ReasoningEffort = append(policy.ReasoningEffort, value)
		}
	}
	for _, value := range []bool{false, true} {
		request := baseline
		request.Logprobs = &value
		if validateChatAdapter(client, request) == nil {
			policy.Logprobs = append(policy.Logprobs, fmt.Sprintf("%t", value))
		}
	}
	for _, value := range []string{"auto", "default", "on_demand", "flex", "performance", "scale", "priority", "fast", "ultrafast", "standard_only"} {
		request := baseline
		request.ServiceTier = value
		if validateChatAdapter(client, request) == nil {
			policy.ServiceTier = append(policy.ServiceTier, value)
		}
	}
	for _, probe := range managedChatOptionProbes() {
		request := baseline
		probe.apply(&request)
		if validateChatAdapter(client, request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	if len(policy.ReasoningEffort) > 0 {
		policy.SupportedOptions = append(policy.SupportedOptions, "reasoning_effort")
	}
	if len(policy.ServiceTier) > 0 {
		policy.SupportedOptions = append(policy.SupportedOptions, "service_tier")
	}
	return policy
}

type managedChatOptionProbe struct {
	name  string
	apply func(*openai.ChatCompletionRequest)
}

func managedChatOptionProbes() []managedChatOptionProbe {
	setBool := func(target func(*openai.ChatCompletionRequest) **bool, value bool) func(*openai.ChatCompletionRequest) {
		return func(request *openai.ChatCompletionRequest) { *target(request) = &value }
	}
	return []managedChatOptionProbe{
		{name: "metadata", apply: func(request *openai.ChatCompletionRequest) {
			request.Metadata = map[string]string{"user_id": "profile-probe"}
		}},
		{name: "store", apply: setBool(func(request *openai.ChatCompletionRequest) **bool { return &request.Store }, true)},
		{name: "modalities", apply: func(request *openai.ChatCompletionRequest) { request.Modalities = []string{"text"} }},
		{name: "audio", apply: func(request *openai.ChatCompletionRequest) {
			request.Modalities = []string{"audio"}
			request.Audio = &openai.ChatAudioOptions{Format: "wav", Voice: openai.ChatAudioVoice{Name: "alloy"}}
		}},
		{name: "safe_prompt", apply: setBool(func(request *openai.ChatCompletionRequest) **bool { return &request.SafePrompt }, true)},
		{name: "n", apply: func(request *openai.ChatCompletionRequest) { value := 2; request.N = &value }},
		{name: "safety_identifier", apply: func(request *openai.ChatCompletionRequest) { request.SafetyIdentifier = "profile-probe" }},
		{name: "prompt_cache_key", apply: func(request *openai.ChatCompletionRequest) { request.PromptCacheKey = "profile-probe" }},
		{name: "prompt_cache_options", apply: func(request *openai.ChatCompletionRequest) {
			request.PromptCacheOptions = &openai.PromptCacheOptions{Mode: "explicit", TTL: "30m"}
		}},
		{name: "prompt_cache_retention", apply: func(request *openai.ChatCompletionRequest) { request.PromptCacheRetention = "24h" }},
		{name: "prompt_mode", apply: func(request *openai.ChatCompletionRequest) { request.PromptMode = "reasoning" }},
		{name: "prediction", apply: func(request *openai.ChatCompletionRequest) {
			request.Prediction = &openai.ChatPrediction{Type: "content", Content: "expected"}
		}},
		{name: "user", apply: func(request *openai.ChatCompletionRequest) { request.User = "profile-probe" }},
		{name: "verbosity", apply: func(request *openai.ChatCompletionRequest) { request.Verbosity = "medium" }},
		{name: "web_search_options", apply: func(request *openai.ChatCompletionRequest) { request.WebSearchOptions = &openai.ChatWebSearchOptions{} }},
		{name: "web_fetch_options", apply: func(request *openai.ChatCompletionRequest) {
			request.WebFetchOptions = &openai.ChatWebFetchOptions{AllowedDomains: []string{"example.com"}, MaxContentTokens: 1000}
		}},
		{name: "logprobs", apply: setBool(func(request *openai.ChatCompletionRequest) **bool { return &request.Logprobs }, true)},
		{name: "top_logprobs", apply: func(request *openai.ChatCompletionRequest) {
			enabled, count := true, 1
			request.Logprobs, request.TopLogprobs = &enabled, &count
		}},
		{name: "frequency_penalty", apply: func(request *openai.ChatCompletionRequest) { value := 0.5; request.FrequencyPenalty = &value }},
		{name: "presence_penalty", apply: func(request *openai.ChatCompletionRequest) { value := 0.5; request.PresencePenalty = &value }},
		{name: "min_p", apply: func(request *openai.ChatCompletionRequest) { value := 0.1; request.MinP = &value }},
		{name: "top_k", apply: func(request *openai.ChatCompletionRequest) { value := 10; request.TopK = &value }},
		{name: "top_a", apply: func(request *openai.ChatCompletionRequest) { value := 0.1; request.TopA = &value }},
		{name: "repetition_penalty", apply: func(request *openai.ChatCompletionRequest) { value := 1.1; request.RepetitionPenalty = &value }},
		{name: "logit_bias", apply: func(request *openai.ChatCompletionRequest) { request.LogitBias = map[string]int{"1": 1} }},
	}
}

func slicesContain(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func managedProviderAuthTypes(providerType string) []string {
	switch providerType {
	case "azure-openai":
		return []string{"api_key", "entra"}
	case "gemini":
		return []string{"api_key", "gcp_adc"}
	case "bedrock":
		return []string{"bearer", "aws_sigv4"}
	case "opensandbox":
		return []string{"api_key"}
	default:
		return []string{}
	}
}

func validAzureProviderVersion(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "preview" {
		return true
	}
	date := value
	if strings.HasSuffix(date, "-preview") {
		date = strings.TrimSuffix(date, "-preview")
	}
	if len(date) != 10 || date[4] != '-' || date[7] != '-' {
		return false
	}
	_, err := time.Parse("2006-01-02", date)
	return err == nil
}

func cloneProviders(current map[string]ManagedProvider) map[string]ManagedProvider {
	next := make(map[string]ManagedProvider, len(current))
	for key, value := range current {
		next[key] = value
	}
	return next
}
