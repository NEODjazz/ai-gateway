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
	Type                         string                                    `json:"type"`
	Operations                   []string                                  `json:"operations"`
	Capabilities                 []string                                  `json:"capabilities"`
	AuthTypes                    []string                                  `json:"auth_types"`
	ChatParameters               ProviderChatParameterPolicy               `json:"chat_parameters"`
	ResponseParameters           ProviderResponseParameterPolicy           `json:"response_parameters"`
	InteractionParameters        ProviderInteractionParameterPolicy        `json:"interaction_parameters"`
	EmbeddingParameters          ProviderEmbeddingParameterPolicy          `json:"embedding_parameters"`
	RerankParameters             ProviderRerankParameterPolicy             `json:"rerank_parameters"`
	CompletionParameters         ProviderCompletionParameterPolicy         `json:"completion_parameters"`
	ModerationParameters         ProviderModerationParameterPolicy         `json:"moderation_parameters"`
	SearchParameters             ProviderSearchParameterPolicy             `json:"search_parameters"`
	ImageGenerationParameters    ProviderImageGenerationParameterPolicy    `json:"image_generation_parameters"`
	ImageEditParameters          ProviderImageEditParameterPolicy          `json:"image_edit_parameters"`
	ImageVariationParameters     ProviderImageVariationParameterPolicy     `json:"image_variation_parameters"`
	AudioTranscriptionParameters ProviderAudioTranscriptionParameterPolicy `json:"audio_transcription_parameters"`
	AudioTranslationParameters   ProviderAudioTranslationParameterPolicy   `json:"audio_translation_parameters"`
	AudioSpeechParameters        ProviderAudioSpeechParameterPolicy        `json:"audio_speech_parameters"`
	OCRParameters                ProviderOCRParameterPolicy                `json:"ocr_parameters"`
	VideoCreateParameters        ProviderVideoCreateParameterPolicy        `json:"video_create_parameters"`
	VideoExtendParameters        ProviderVideoExtendParameterPolicy        `json:"video_extend_parameters"`
	FineTuningCreateParameters   ProviderFineTuningCreateParameterPolicy   `json:"fine_tuning_create_parameters"`
	ContainerCreateParameters    ProviderContainerCreateParameterPolicy    `json:"container_create_parameters"`
	ChatModelParameters          []ProviderChatModelParameterPolicy        `json:"chat_model_parameters"`
}

type ProviderChatParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	ReasoningEffort  []string `json:"reasoning_effort"`
	ReasoningFormat  []string `json:"reasoning_format"`
	CitationOptions  []string `json:"citation_options"`
	Thinking         []string `json:"thinking"`
	Logprobs         []string `json:"logprobs"`
	ServiceTier      []string `json:"service_tier"`
}

type ProviderChatModelParameterPolicy struct {
	Model            string   `json:"model"`
	SupportedOptions []string `json:"supported_options"`
	ReasoningEffort  []string `json:"reasoning_effort"`
	ReasoningFormat  []string `json:"reasoning_format"`
}

type ProviderResponseParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	ReasoningEffort  []string `json:"reasoning_effort"`
	ServiceTier      []string `json:"service_tier"`
}

type ProviderInteractionParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	InputForms       []string `json:"input_forms"`
	ThinkingLevels   []string `json:"thinking_levels"`
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

type ProviderSearchParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	QueryForms       []string `json:"query_forms"`
}

type ProviderImageGenerationParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
}

type ProviderImageEditParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	MaxImages        int      `json:"max_images"`
}

type ProviderImageVariationParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
}

type ProviderAudioTranscriptionParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
}

type ProviderAudioTranslationParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
}

type ProviderAudioSpeechParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	SSESupported     bool     `json:"sse_supported"`
}

type ProviderOCRParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	DocumentForms    []string `json:"document_forms"`
}

type ProviderVideoCreateParameterPolicy struct {
	SupportedOptions    []string `json:"supported_options"`
	Seconds             []string `json:"seconds"`
	Sizes               []string `json:"sizes"`
	InputReferenceForms []string `json:"input_reference_forms"`
}

type ProviderVideoExtendParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
	Seconds          []string `json:"seconds"`
}

type ProviderFineTuningCreateParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
}

type ProviderContainerCreateParameterPolicy struct {
	SupportedOptions []string `json:"supported_options"`
}

var managedProviderTypes = []string{"demo", "ollama", "openai", "openai-compatible", "openrouter", "azure-openai", "anthropic", "gemini", "vertex-gemini", "cohere", "mistral", "voyage", "bedrock", "groq", "deepseek", "cerebras", "nvidia-nim", "together", "xai", "opensandbox"}

var managedOperationCapabilities = []string{
	"chat", "completions", "responses", "interactions", "count_tokens", "embeddings", "rerank", "moderation",
	"image_generation", "image_edit", "image_variation",
	"audio_transcription", "audio_translation", "audio_speech", "ocr", "search", "skills", "fine_tuning", "video", "video_remix", "video_extension", "container", "container_files", "container_network", "cached_content", "sandbox", "realtime", "stream", "bedrock_invoke",
}

var managedFeatureCapabilities = []string{
	"tools", "custom_tools", "response_image_generation", "response_computer", "response_shell", "response_apply_patch", "structured_output", "mcp", "code_interpreter", "file_search", "vision", "web_search", "tool_search", "audio_input", "video_input",
	"web_fetch", "audio", "prompt_cache", "assistant_prefill", "memory_tool", "bash_tool", "text_editor_tool", "computer_toolset", "browser_toolset", "thinking", "zero_output", "inference_geo", "context_management", "tool_result_error", "document_citations", "document_metadata", "document_text",
	"background_responses", "background_interactions", "file_input", "interaction_agents", "interaction_environment_reuse", "gemini_safety_settings", "gemini_code_execution", "gemini_audio_timestamp", "gemini_media_resolution", "gemini_media_processing", "gemini_search_time_range", "gemini_file_search", "gemini_computer_use", "gemini_mcp", "url_context", "google_maps",
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
	input.AuthType = strings.ToLower(strings.TrimSpace(input.AuthType))
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
		input.AuthType = normalizeAzureAuthType(input.AuthType)
		parsed, _ := url.Parse(input.BaseURL)
		if parsed.RawQuery != "" || parsed.Fragment != "" || !validAzureProviderVersion(input.APIVersion) || (input.AuthType != "api_key" && input.AuthType != "entra") {
			return ManagedProvider{}, ErrInvalidProvider
		}
		input.Region = ""
	} else if input.Type == "gemini" {
		input.APIVersion = ""
		if input.AuthType == "" {
			input.AuthType = "api_key"
		}
		if input.AuthType != "api_key" && input.AuthType != "gcp_adc" {
			return ManagedProvider{}, ErrInvalidProvider
		}
		input.Region = ""
	} else if input.Type == "vertex-gemini" {
		input.APIVersion = ""
		if input.AuthType == "" {
			input.AuthType = "gcp_adc"
		}
		if input.AuthType != "gcp_adc" || !validManagedVertexGeminiBaseURL(input.BaseURL) {
			return ManagedProvider{}, ErrInvalidProvider
		}
		input.Region = ""
	} else if input.Type == "bedrock" {
		input.APIVersion = ""
		parsed, _ := url.Parse(input.BaseURL)
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return ManagedProvider{}, ErrInvalidProvider
		}
		if input.AuthType == "" || input.AuthType == "api_key" {
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
			Type:                         providerType,
			Operations:                   operations,
			Capabilities:                 capabilities,
			AuthTypes:                    managedProviderAuthTypes(providerType),
			ChatParameters:               managedProviderChatParameterPolicy(client, slicesContain(operations, "chat")),
			ResponseParameters:           managedProviderResponseParameterPolicy(client, slicesContain(operations, "responses")),
			InteractionParameters:        managedProviderInteractionParameterPolicy(client, slicesContain(operations, "interactions")),
			EmbeddingParameters:          managedProviderEmbeddingParameterPolicy(client, slicesContain(operations, "embeddings")),
			RerankParameters:             managedProviderRerankParameterPolicy(client, slicesContain(operations, "rerank")),
			CompletionParameters:         managedProviderCompletionParameterPolicy(client, slicesContain(operations, "completions")),
			ModerationParameters:         managedProviderModerationParameterPolicy(client, slicesContain(operations, "moderation")),
			SearchParameters:             managedProviderSearchParameterPolicy(client, slicesContain(operations, "search")),
			ImageGenerationParameters:    managedProviderImageGenerationParameterPolicy(client, slicesContain(operations, "image_generation")),
			ImageEditParameters:          managedProviderImageEditParameterPolicy(client, slicesContain(operations, "image_edit")),
			ImageVariationParameters:     managedProviderImageVariationParameterPolicy(client, slicesContain(operations, "image_variation")),
			AudioTranscriptionParameters: managedProviderAudioTranscriptionParameterPolicy(client, slicesContain(operations, "audio_transcription")),
			AudioTranslationParameters:   managedProviderAudioTranslationParameterPolicy(client, slicesContain(operations, "audio_translation")),
			AudioSpeechParameters:        managedProviderAudioSpeechParameterPolicy(client, slicesContain(operations, "audio_speech")),
			OCRParameters:                managedProviderOCRParameterPolicy(client, slicesContain(operations, "ocr")),
			VideoCreateParameters:        managedProviderVideoCreateParameterPolicy(client, slicesContain(operations, "video")),
			VideoExtendParameters:        managedProviderVideoExtendParameterPolicy(client, slicesContain(operations, "video_extension")),
			FineTuningCreateParameters:   managedProviderFineTuningCreateParameterPolicy(client, slicesContain(operations, "fine_tuning")),
			ContainerCreateParameters:    managedProviderContainerCreateParameterPolicy(client, operations),
			ChatModelParameters:          managedProviderChatModelParameterPolicies(client, slicesContain(operations, "chat")),
		})
	}
	return profiles
}

func managedProviderChatModelParameterPolicies(client Client, supported bool) []ProviderChatModelParameterPolicy {
	result := []ProviderChatModelParameterPolicy{}
	prober, ok := client.(interface{ ManagedChatModelProbes() []string })
	if !supported || !ok {
		return result
	}
	base := managedProviderChatParameterPolicy(client, true)
	for _, model := range prober.ManagedChatModelProbes() {
		policy := managedProviderChatParameterPolicyForModel(client, true, model)
		options := make([]string, 0, len(policy.SupportedOptions))
		for _, option := range policy.SupportedOptions {
			if !slicesContain(base.SupportedOptions, option) {
				options = append(options, option)
			}
		}
		result = append(result, ProviderChatModelParameterPolicy{Model: model, SupportedOptions: options, ReasoningEffort: policy.ReasoningEffort, ReasoningFormat: policy.ReasoningFormat})
	}
	return result
}

func managedProviderInteractionParameterPolicy(client Client, supported bool) ProviderInteractionParameterPolicy {
	policy := ProviderInteractionParameterPolicy{SupportedOptions: []string{}, InputForms: []string{}, ThinkingLevels: []string{}}
	validator, ok := client.(interface {
		ValidateInteractionParameters(openai.InteractionRequest) error
	})
	if !supported || !ok {
		return policy
	}
	baseline := openai.InteractionRequest{Model: "model", Input: "hello"}
	for _, probe := range []struct {
		name  string
		apply func(*openai.InteractionRequest)
	}{
		{"agent", func(r *openai.InteractionRequest) { r.Model, r.Agent = "", "agent" }},
		{"environment", func(r *openai.InteractionRequest) {
			r.Model, r.Agent, r.Environment, r.PreviousInteractionID = "", "agent", "env_existing", "interaction_previous"
		}},
		{"system_instruction", func(r *openai.InteractionRequest) { r.SystemInstruction = "be concise" }},
		{"tools", func(r *openai.InteractionRequest) {
			r.Tools = []openai.ResponseTool{{Type: "function", Name: "lookup", Parameters: map[string]any{"type": "object"}}}
		}},
		{"response_format", func(r *openai.InteractionRequest) {
			r.ResponseFormat = map[string]any{"type": "json_schema", "name": "answer", "schema": map[string]any{"type": "object"}}
		}},
		{"response_mime_type", func(r *openai.InteractionRequest) { r.ResponseMIMEType = "application/json" }},
		{"previous_interaction_id", func(r *openai.InteractionRequest) { r.PreviousInteractionID = "interaction_previous" }},
		{"store", func(r *openai.InteractionRequest) { value := true; r.Store = &value }},
		{"stream", func(r *openai.InteractionRequest) { r.Stream = true }},
		{"background", func(r *openai.InteractionRequest) { r.Background = true }},
		{"generation_config.max_output_tokens", func(r *openai.InteractionRequest) { value := 8; r.GenerationConfig.MaxOutputTokens = &value }},
		{"generation_config.temperature", func(r *openai.InteractionRequest) { value := 0.5; r.GenerationConfig.Temperature = &value }},
		{"generation_config.top_p", func(r *openai.InteractionRequest) { value := 0.8; r.GenerationConfig.TopP = &value }},
		{"generation_config.seed", func(r *openai.InteractionRequest) { value := int64(7); r.GenerationConfig.Seed = &value }},
		{"generation_config.stop_sequences", func(r *openai.InteractionRequest) { r.GenerationConfig.StopSequences = []string{"END"} }},
		{"generation_config.thinking_level", func(r *openai.InteractionRequest) { r.GenerationConfig.ThinkingLevel = "high" }},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateInteractionParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	for _, input := range []struct {
		name  string
		value any
	}{{"string", "hello"}, {"steps", []any{"hello"}}} {
		request := baseline
		request.Input = input.value
		if validator.ValidateInteractionParameters(request) == nil {
			policy.InputForms = append(policy.InputForms, input.name)
		}
	}
	for _, level := range []string{"minimal", "low", "medium", "high"} {
		request := baseline
		request.GenerationConfig.ThinkingLevel = level
		if validator.ValidateInteractionParameters(request) == nil {
			policy.ThinkingLevels = append(policy.ThinkingLevels, level)
		}
	}
	return policy
}

func managedProviderContainerCreateParameterPolicy(client Client, operations []string) ProviderContainerCreateParameterPolicy {
	policy := ProviderContainerCreateParameterPolicy{SupportedOptions: []string{}}
	validator, ok := client.(interface {
		ValidateContainerCreateParameters(openai.ContainerProviderCreateRequest) error
	})
	if !slicesContain(operations, "container") || !ok {
		return policy
	}
	baseline := openai.ContainerProviderCreateRequest{Name: "container"}
	for _, probe := range []struct {
		name  string
		apply func(*openai.ContainerProviderCreateRequest)
	}{
		{"expires_after", func(r *openai.ContainerProviderCreateRequest) {
			r.ExpiresAfter = &openai.ContainerExpiresAfter{Anchor: "last_active_at", Minutes: 60}
		}},
		{"memory_limit", func(r *openai.ContainerProviderCreateRequest) { r.MemoryLimit = "4g" }},
		{"network_policy", func(r *openai.ContainerProviderCreateRequest) {
			r.NetworkPolicy = &openai.ContainerNetworkPolicyRequest{Type: "disabled"}
		}},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateContainerCreateParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	if slicesContain(operations, "container_files") {
		policy.SupportedOptions = append(policy.SupportedOptions, "file_ids")
	}
	return policy
}

func managedProviderFineTuningCreateParameterPolicy(client Client, supported bool) ProviderFineTuningCreateParameterPolicy {
	policy := ProviderFineTuningCreateParameterPolicy{SupportedOptions: []string{}}
	validator, ok := client.(interface {
		ValidateFineTuningCreateParameters(openai.FineTuningCreateRequest) error
	})
	if !supported || !ok {
		return policy
	}
	baseline := openai.FineTuningCreateRequest{Model: "model", TrainingFile: "file_training"}
	for _, probe := range []struct {
		name  string
		apply func(*openai.FineTuningCreateRequest)
	}{
		{"validation_file", func(r *openai.FineTuningCreateRequest) { r.ValidationFile = "file_validation" }},
		{"suffix", func(r *openai.FineTuningCreateRequest) { r.Suffix = "custom" }},
		{"seed", func(r *openai.FineTuningCreateRequest) { value := int64(1); r.Seed = &value }},
		{"metadata", func(r *openai.FineTuningCreateRequest) { r.Metadata = map[string]string{"key": "value"} }},
		{"method", func(r *openai.FineTuningCreateRequest) { r.Method = []byte(`{"type":"supervised"}`) }},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateFineTuningCreateParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	return policy
}

func managedProviderVideoCreateParameterPolicy(client Client, supported bool) ProviderVideoCreateParameterPolicy {
	policy := ProviderVideoCreateParameterPolicy{SupportedOptions: []string{}, Seconds: []string{}, Sizes: []string{}, InputReferenceForms: []string{}}
	validator, ok := client.(interface {
		ValidateVideoCreateParameters(openai.VideoCreateRequest) error
	})
	if !supported || !ok {
		return policy
	}
	baseline := openai.VideoCreateRequest{Model: "model", Prompt: "prompt"}
	for _, probe := range []struct {
		name  string
		apply func(*openai.VideoCreateRequest)
	}{
		{"seconds", func(r *openai.VideoCreateRequest) { r.Seconds = "4" }},
		{"size", func(r *openai.VideoCreateRequest) { r.Size = "1280x720" }},
		{"input_reference", func(r *openai.VideoCreateRequest) {
			r.InputReference = &openai.VideoInputReference{ImageURL: "https://example.test/image.png"}
		}},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateVideoCreateParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	for _, seconds := range []string{"4", "8", "12"} {
		request := baseline
		request.Seconds = seconds
		if validator.ValidateVideoCreateParameters(request) == nil {
			policy.Seconds = append(policy.Seconds, seconds)
		}
	}
	for _, size := range []string{"720x1280", "1280x720", "1024x1792", "1792x1024"} {
		request := baseline
		request.Size = size
		if validator.ValidateVideoCreateParameters(request) == nil {
			policy.Sizes = append(policy.Sizes, size)
		}
	}
	request := baseline
	request.InputReference = &openai.VideoInputReference{ImageURL: "https://example.test/image.png"}
	if validator.ValidateVideoCreateParameters(request) == nil {
		policy.InputReferenceForms = append(policy.InputReferenceForms, "image_url")
	}
	return policy
}

func managedProviderVideoExtendParameterPolicy(client Client, supported bool) ProviderVideoExtendParameterPolicy {
	policy := ProviderVideoExtendParameterPolicy{SupportedOptions: []string{}, Seconds: []string{}}
	validator, ok := client.(interface {
		ValidateVideoExtendParameters(openai.VideoExtendRequest) error
	})
	if !supported || !ok {
		return policy
	}
	baseline := openai.VideoExtendRequest{Prompt: "prompt"}
	for _, seconds := range []string{"4", "8", "12"} {
		request := baseline
		request.Seconds = seconds
		if validator.ValidateVideoExtendParameters(request) == nil {
			policy.Seconds = append(policy.Seconds, seconds)
		}
	}
	if len(policy.Seconds) > 0 {
		policy.SupportedOptions = append(policy.SupportedOptions, "seconds")
	}
	return policy
}

func managedProviderOCRParameterPolicy(client Client, supported bool) ProviderOCRParameterPolicy {
	policy := ProviderOCRParameterPolicy{SupportedOptions: []string{}, DocumentForms: []string{}}
	validator, ok := client.(interface {
		ValidateOCRParameters(openai.OCRRequest) error
	})
	if !supported || !ok {
		return policy
	}
	for _, probe := range []struct {
		name     string
		document openai.OCRDocument
	}{
		{"https_document", openai.OCRDocument{Type: "document_url", DocumentURL: "https://example.test/document.pdf"}},
		{"inline_document", openai.OCRDocument{Type: "document_url", DocumentURL: "data:application/pdf;base64,JVBERi0xLjcKcGFnZQ=="}},
		{"https_image", openai.OCRDocument{Type: "image_url", ImageURL: "https://example.test/image.png"}},
		{"inline_image", openai.OCRDocument{Type: "image_url", ImageURL: "data:image/png;base64,iVBORw0KGgo="}},
	} {
		if validator.ValidateOCRParameters(openai.OCRRequest{Model: "model", Document: probe.document}) == nil {
			policy.DocumentForms = append(policy.DocumentForms, probe.name)
		}
	}
	baseline := openai.OCRRequest{Model: "model", Document: openai.OCRDocument{Type: "document_url", DocumentURL: "data:application/pdf;base64,JVBERi0xLjcKcGFnZQ=="}}
	for _, probe := range []struct {
		name  string
		apply func(*openai.OCRRequest)
	}{
		{"pages", func(r *openai.OCRRequest) { r.Pages = []int{0} }},
		{"include_image_base64", func(r *openai.OCRRequest) { value := true; r.IncludeImageBase64 = &value }},
		{"image_limit", func(r *openai.OCRRequest) { value := 1; r.ImageLimit = &value }},
		{"image_min_size", func(r *openai.OCRRequest) { value := 1; r.ImageMinSize = &value }},
		{"table_format", func(r *openai.OCRRequest) { r.TableFormat = "markdown" }},
		{"extract_header", func(r *openai.OCRRequest) { value := true; r.ExtractHeader = &value }},
		{"extract_footer", func(r *openai.OCRRequest) { value := true; r.ExtractFooter = &value }},
		{"include_blocks", func(r *openai.OCRRequest) { value := true; r.IncludeBlocks = &value }},
		{"confidence_scores_granularity", func(r *openai.OCRRequest) { r.ConfidenceScoresGranularity = "word" }},
		{"document_annotation_format", func(r *openai.OCRRequest) { r.DocumentAnnotationFormat = &openai.ResponseFormat{Type: "json_object"} }},
		{"document_annotation_prompt", func(r *openai.OCRRequest) {
			r.DocumentAnnotationFormat = &openai.ResponseFormat{Type: "json_object"}
			r.DocumentAnnotationPrompt = "extract"
		}},
		{"bbox_annotation_format", func(r *openai.OCRRequest) { r.BBoxAnnotationFormat = &openai.ResponseFormat{Type: "json_object"} }},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateOCRParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	return policy
}

func managedProviderAudioSpeechParameterPolicy(client Client, supported bool) ProviderAudioSpeechParameterPolicy {
	policy := ProviderAudioSpeechParameterPolicy{SupportedOptions: []string{}}
	validator, ok := client.(interface {
		ValidateAudioSpeechParameters(openai.AudioSpeechRequest) error
	})
	if !supported || !ok {
		return policy
	}
	baseline := openai.AudioSpeechRequest{Model: "model", Input: "text", Voice: "voice"}
	for _, probe := range []struct {
		name  string
		apply func(*openai.AudioSpeechRequest)
	}{
		{"language", func(r *openai.AudioSpeechRequest) { r.Language = "en" }},
		{"instructions", func(r *openai.AudioSpeechRequest) { r.Instructions = "warmly" }},
		{"response_format", func(r *openai.AudioSpeechRequest) { r.ResponseFormat = "mp3" }},
		{"speed", func(r *openai.AudioSpeechRequest) { value := 1.0; r.Speed = &value }},
		{"stream_format", func(r *openai.AudioSpeechRequest) { r.StreamFormat = "audio" }},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateAudioSpeechParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	if streaming, ok := client.(interface{ SupportsAudioSpeechStreaming() bool }); ok && streaming.SupportsAudioSpeechStreaming() {
		request := baseline
		request.StreamFormat = "sse"
		policy.SSESupported = validator.ValidateAudioSpeechParameters(request) == nil
	}
	return policy
}

func managedProviderAudioTranslationParameterPolicy(client Client, supported bool) ProviderAudioTranslationParameterPolicy {
	policy := ProviderAudioTranslationParameterPolicy{SupportedOptions: []string{}}
	validator, ok := client.(interface {
		ValidateAudioTranslationParameters(openai.AudioTranscriptionRequest) error
	})
	if !supported || !ok {
		return policy
	}
	baseline := openai.AudioTranscriptionRequest{
		Model: "model",
		File:  openai.AudioAttachment{Filename: "audio.wav", MediaType: "audio/wav", Data: "UklGRi4AAABXQVZFZm10IBAAAAABAAEAQB8AAEAfAAABAAgAZGF0YQoAAAAAAAAAAAA="},
	}
	for _, probe := range []struct {
		name  string
		apply func(*openai.AudioTranscriptionRequest)
	}{
		{"language", func(r *openai.AudioTranscriptionRequest) { r.Language = "en" }},
		{"prompt", func(r *openai.AudioTranscriptionRequest) { r.Prompt = "terms" }},
		{"response_format", func(r *openai.AudioTranscriptionRequest) { r.ResponseFormat = "json" }},
		{"temperature", func(r *openai.AudioTranscriptionRequest) { value := 0.5; r.Temperature = &value }},
		{"timestamp_granularities", func(r *openai.AudioTranscriptionRequest) {
			r.ResponseFormat = "verbose_json"
			r.TimestampGranularities = []string{"word"}
		}},
		{"include", func(r *openai.AudioTranscriptionRequest) { r.Include = []string{"logprobs"} }},
		{"languages", func(r *openai.AudioTranscriptionRequest) { r.Languages = []string{"en-US"} }},
		{"keywords", func(r *openai.AudioTranscriptionRequest) { r.Keywords = []string{"term"} }},
		{"mode", func(r *openai.AudioTranscriptionRequest) { r.Mode = "SMART" }},
		{"chunking_strategy", func(r *openai.AudioTranscriptionRequest) {
			r.ChunkingStrategy = &openai.AudioChunkingStrategy{Type: "auto"}
		}},
		{"known_speakers", func(r *openai.AudioTranscriptionRequest) {
			r.KnownSpeakerNames = []string{"speaker"}
			r.KnownSpeakerReferences = []openai.AudioAttachment{baseline.File}
		}},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateAudioTranslationParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	return policy
}

func managedProviderAudioTranscriptionParameterPolicy(client Client, supported bool) ProviderAudioTranscriptionParameterPolicy {
	policy := ProviderAudioTranscriptionParameterPolicy{SupportedOptions: []string{}}
	validator, ok := client.(interface {
		ValidateAudioTranscriptionParameters(openai.AudioTranscriptionRequest) error
	})
	if !supported || !ok {
		return policy
	}
	baseline := openai.AudioTranscriptionRequest{
		Model: "model",
		File:  openai.AudioAttachment{Filename: "audio.wav", MediaType: "audio/wav", Data: "UklGRi4AAABXQVZFZm10IBAAAAABAAEAQB8AAEAfAAABAAgAZGF0YQoAAAAAAAAAAAA="},
	}
	for _, probe := range []struct {
		name  string
		apply func(*openai.AudioTranscriptionRequest)
	}{
		{"language", func(r *openai.AudioTranscriptionRequest) { r.Language = "en" }},
		{"prompt", func(r *openai.AudioTranscriptionRequest) { r.Prompt = "terms" }},
		{"response_format", func(r *openai.AudioTranscriptionRequest) { r.ResponseFormat = "json" }},
		{"temperature", func(r *openai.AudioTranscriptionRequest) { value := 0.5; r.Temperature = &value }},
		{"timestamp_granularities", func(r *openai.AudioTranscriptionRequest) {
			r.ResponseFormat = "verbose_json"
			r.TimestampGranularities = []string{"word"}
		}},
		{"include", func(r *openai.AudioTranscriptionRequest) { r.Include = []string{"logprobs"} }},
		{"languages", func(r *openai.AudioTranscriptionRequest) { r.Languages = []string{"en-US"} }},
		{"keywords", func(r *openai.AudioTranscriptionRequest) { r.Keywords = []string{"term"} }},
		{"mode", func(r *openai.AudioTranscriptionRequest) { r.Mode = "SMART" }},
		{"chunking_strategy", func(r *openai.AudioTranscriptionRequest) {
			r.ChunkingStrategy = &openai.AudioChunkingStrategy{Type: "auto"}
		}},
		{"known_speakers", func(r *openai.AudioTranscriptionRequest) {
			r.KnownSpeakerNames = []string{"speaker"}
			r.KnownSpeakerReferences = []openai.AudioAttachment{baseline.File}
		}},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateAudioTranscriptionParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	if streaming, ok := client.(interface{ SupportsAudioTranscriptionStreaming() bool }); ok && streaming.SupportsAudioTranscriptionStreaming() {
		request := baseline
		request.Stream = true
		if validator.ValidateAudioTranscriptionParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, "stream")
		}
	}
	return policy
}

func managedProviderImageVariationParameterPolicy(client Client, supported bool) ProviderImageVariationParameterPolicy {
	policy := ProviderImageVariationParameterPolicy{SupportedOptions: []string{}}
	validator, ok := client.(interface {
		ValidateImageVariationParameters(openai.ImageVariationRequest) error
	})
	if !supported || !ok {
		return policy
	}
	baseline := openai.ImageVariationRequest{
		Model: "model",
		Image: openai.ImageAttachment{MediaType: "image/png", Data: "iVBORw0KGgpmaXh0dXJl"},
	}
	for _, probe := range []struct {
		name  string
		apply func(*openai.ImageVariationRequest)
	}{
		{"n", func(r *openai.ImageVariationRequest) { value := 1; r.N = &value }},
		{"response_format", func(r *openai.ImageVariationRequest) { r.ResponseFormat = "b64_json" }},
		{"size", func(r *openai.ImageVariationRequest) { r.Size = "1024x1024" }},
		{"user", func(r *openai.ImageVariationRequest) { r.User = "probe" }},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateImageVariationParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	return policy
}

func managedProviderImageEditParameterPolicy(client Client, supported bool) ProviderImageEditParameterPolicy {
	policy := ProviderImageEditParameterPolicy{SupportedOptions: []string{}}
	validator, ok := client.(interface {
		ValidateImageEditParameters(openai.ImageEditRequest) error
	})
	if !supported || !ok {
		return policy
	}
	image := openai.ImageAttachment{MediaType: "image/png", Data: "iVBORw0KGgpmaXh0dXJl"}
	baseline := openai.ImageEditRequest{Model: "model", Prompt: "prompt", Images: []openai.ImageAttachment{image}}
	for count := 1; count <= openai.MaxImageAttachments; count++ {
		request := baseline
		request.Images = make([]openai.ImageAttachment, count)
		for index := range request.Images {
			request.Images[index] = image
		}
		if validator.ValidateImageEditParameters(request) == nil {
			policy.MaxImages = count
		}
	}
	for _, probe := range []struct {
		name  string
		apply func(*openai.ImageEditRequest)
	}{
		{"mask", func(r *openai.ImageEditRequest) { mask := image; r.Mask = &mask }},
		{"n", func(r *openai.ImageEditRequest) { value := 1; r.N = &value }},
		{"quality", func(r *openai.ImageEditRequest) { r.Quality = "low" }},
		{"response_format", func(r *openai.ImageEditRequest) { r.ResponseFormat = "b64_json" }},
		{"size", func(r *openai.ImageEditRequest) { r.Size = "1024x1024" }},
		{"user", func(r *openai.ImageEditRequest) { r.User = "probe" }},
		{"background", func(r *openai.ImageEditRequest) { r.Background = "transparent" }},
		{"output_format", func(r *openai.ImageEditRequest) { r.OutputFormat = "png" }},
		{"output_compression", func(r *openai.ImageEditRequest) { value := 50; r.OutputCompression = &value }},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateImageEditParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	return policy
}

func managedProviderImageGenerationParameterPolicy(client Client, supported bool) ProviderImageGenerationParameterPolicy {
	policy := ProviderImageGenerationParameterPolicy{SupportedOptions: []string{}}
	validator, ok := client.(interface {
		ValidateImageGenerationParameters(openai.ImageGenerationRequest) error
	})
	if !supported || !ok {
		return policy
	}
	baseline := openai.ImageGenerationRequest{Model: "model", Prompt: "prompt"}
	for _, probe := range []struct {
		name  string
		apply func(*openai.ImageGenerationRequest)
	}{
		{"n", func(r *openai.ImageGenerationRequest) { value := 1; r.N = &value }},
		{"quality", func(r *openai.ImageGenerationRequest) { r.Quality = "low" }},
		{"response_format", func(r *openai.ImageGenerationRequest) { r.ResponseFormat = "b64_json" }},
		{"size", func(r *openai.ImageGenerationRequest) { r.Size = "1024x1024" }},
		{"style", func(r *openai.ImageGenerationRequest) { r.Style = "vivid" }},
		{"user", func(r *openai.ImageGenerationRequest) { r.User = "probe" }},
		{"background", func(r *openai.ImageGenerationRequest) { r.Background = "transparent" }},
		{"output_format", func(r *openai.ImageGenerationRequest) { r.OutputFormat = "png" }},
		{"output_compression", func(r *openai.ImageGenerationRequest) { value := 50; r.OutputCompression = &value }},
		{"resolution", func(r *openai.ImageGenerationRequest) { r.Resolution = "1K" }},
		{"aspect_ratio", func(r *openai.ImageGenerationRequest) { r.AspectRatio = "1:1" }},
		{"seed", func(r *openai.ImageGenerationRequest) { value := int64(1); r.Seed = &value }},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateImageGenerationParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	return policy
}

func managedProviderSearchParameterPolicy(client Client, supported bool) ProviderSearchParameterPolicy {
	policy := ProviderSearchParameterPolicy{SupportedOptions: []string{}, QueryForms: []string{}}
	validator, ok := client.(interface {
		ValidateSearchParameters(openai.SearchRequest) error
	})
	if !supported || !ok {
		return policy
	}
	baseline := openai.SearchRequest{Model: "model", Query: "query"}
	for _, probe := range []struct {
		name  string
		query any
	}{{"text", "query"}, {"text_array", []string{"one", "two"}}} {
		request := baseline
		request.Query = probe.query
		if validator.ValidateSearchParameters(request) == nil {
			policy.QueryForms = append(policy.QueryForms, probe.name)
		}
	}
	for _, probe := range []struct {
		name  string
		apply func(*openai.SearchRequest)
	}{
		{"max_results", func(r *openai.SearchRequest) { value := 10; r.MaxResults = &value }},
		{"search_domain_filter", func(r *openai.SearchRequest) { r.SearchDomainFilter = []string{"example.test"} }},
		{"max_tokens_per_page", func(r *openai.SearchRequest) { value := 1024; r.MaxTokensPerPage = &value }},
		{"country", func(r *openai.SearchRequest) { r.Country = "US" }},
	} {
		request := baseline
		probe.apply(&request)
		if validator.ValidateSearchParameters(request) == nil {
			policy.SupportedOptions = append(policy.SupportedOptions, probe.name)
		}
	}
	return policy
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
		{"truncate", func(r *openai.RerankRequest) { r.Truncate = "END" }},
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
	if prober, ok := client.(interface{ ManagedResponseParameterProbeModel() string }); ok {
		baseline.Model = prober.ManagedResponseParameterProbeModel()
	}
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
		if managedResponseOptionSupported(client, probe.name, request) {
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

func managedResponseOptionSupported(client Client, name string, request openai.ResponseRequest) bool {
	if validateResponseAdapter(client, request) == nil {
		return true
	}
	var effort string
	switch name {
	case "temperature":
		effort = "none"
	case "top_p":
		effort = "high"
		value := 0.97
		request.TopP = &value
	default:
		return false
	}
	request.Reasoning = &openai.ResponseReasoning{Effort: &effort}
	return validateResponseAdapter(client, request) == nil
}

type managedResponseOptionProbe struct {
	name  string
	apply func(*openai.ResponseRequest)
}

func managedResponseOptionProbes() []managedResponseOptionProbe {
	return []managedResponseOptionProbe{
		{name: "metadata", apply: func(request *openai.ResponseRequest) { request.Metadata = map[string]string{"trace": "profile-probe"} }},
		{name: "context_management", apply: func(request *openai.ResponseRequest) {
			threshold := 1000
			request.ContextManagement = []openai.ResponseContextEntry{{Type: "compaction", CompactThreshold: &threshold}}
		}},
		{name: "moderation", apply: func(request *openai.ResponseRequest) {
			request.Moderation = &openai.ProviderModeration{Model: "omni-moderation-latest", Policy: &openai.ProviderModerationPolicy{Input: &openai.ProviderModerationRule{Mode: "block"}}}
		}},
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
		{name: "prompt_cache_options", apply: func(request *openai.ResponseRequest) {
			request.PromptCacheOptions = &openai.PromptCacheOptions{Mode: "explicit", TTL: "30m", ComparisonResponseID: "resp_profile"}
		}},
		{name: "prompt_cache_retention", apply: func(request *openai.ResponseRequest) { request.PromptCacheRetention = "24h" }},
		{name: "stream_options", apply: func(request *openai.ResponseRequest) {
			value := false
			request.Stream, request.StreamOptions = true, &openai.ResponseStreamOptions{IncludeObfuscation: &value}
		}},
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
	return managedProviderChatParameterPolicyForModel(client, supportsChat, "model")
}

func managedProviderChatParameterPolicyForModel(client Client, supportsChat bool, model string) ProviderChatParameterPolicy {
	policy := ProviderChatParameterPolicy{SupportedOptions: []string{}, ReasoningEffort: []string{}, ReasoningFormat: []string{}, CitationOptions: []string{}, Thinking: []string{}, Logprobs: []string{}, ServiceTier: []string{}}
	if !supportsChat {
		return policy
	}
	baseline := openai.ChatCompletionRequest{Model: model, Messages: []openai.Message{{Role: "user", Content: "test"}}}
	for _, value := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "default"} {
		request := baseline
		request.ReasoningEffort = value
		if validateChatAdapter(client, request) == nil {
			policy.ReasoningEffort = append(policy.ReasoningEffort, value)
		}
	}
	for _, value := range []string{"hidden", "raw", "parsed"} {
		request := baseline
		request.ReasoningFormat = value
		if validateChatAdapter(client, request) == nil {
			policy.ReasoningFormat = append(policy.ReasoningFormat, value)
		}
	}
	for _, value := range []string{"enabled", "disabled"} {
		request := baseline
		request.CitationOptions = value
		if validateChatAdapter(client, request) == nil {
			policy.CitationOptions = append(policy.CitationOptions, value)
		}
	}
	for _, value := range []string{"enabled", "disabled"} {
		request := baseline
		request.Thinking = &openai.ChatThinkingOptions{Type: value}
		if validateChatAdapter(client, request) == nil {
			policy.Thinking = append(policy.Thinking, value)
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
	if len(policy.ReasoningFormat) > 0 {
		policy.SupportedOptions = append(policy.SupportedOptions, "reasoning_format")
	}
	if len(policy.CitationOptions) > 0 {
		policy.SupportedOptions = append(policy.SupportedOptions, "citation_options")
	}
	if len(policy.Thinking) > 0 {
		policy.SupportedOptions = append(policy.SupportedOptions, "thinking")
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
		{name: "moderation", apply: func(request *openai.ChatCompletionRequest) {
			request.Moderation = &openai.ProviderModeration{Model: "omni-moderation-latest", Policy: &openai.ProviderModerationPolicy{Input: &openai.ProviderModerationRule{Mode: "block"}}}
		}},
		{name: "clear_thinking", apply: setBool(func(request *openai.ChatCompletionRequest) **bool { return &request.ClearThinking }, true)},
		{name: "include_reasoning", apply: setBool(func(request *openai.ChatCompletionRequest) **bool { return &request.IncludeReasoning }, true)},
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
	case "vertex-gemini":
		return []string{"gcp_adc"}
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
