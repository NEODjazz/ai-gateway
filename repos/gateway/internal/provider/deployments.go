package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"ai-gateway-gateway/internal/config"
)

type ModelDeployment struct {
	ID                    string   `json:"id"`
	ProviderID            string   `json:"provider_id"`
	CredentialID          string   `json:"credential_id,omitempty"`
	CredentialSet         bool     `json:"-"`
	ProviderType          string   `json:"provider_type"`
	UpstreamModel         string   `json:"upstream_model,omitempty"`
	Models                []string `json:"models"`
	Capabilities          []string `json:"capabilities,omitempty"`
	Priority              int      `json:"priority"`
	Weight                int      `json:"weight"`
	GuardrailPolicy       string   `json:"guardrail_policy,omitempty"`
	RequestTimeoutMS      int      `json:"request_timeout_ms,omitempty"`
	MaxRetries            int      `json:"max_retries,omitempty"`
	CooldownAfterFailures int      `json:"cooldown_after_failures,omitempty"`
	CooldownSeconds       int      `json:"cooldown_seconds,omitempty"`
	MaxParallelRequests   int      `json:"max_parallel_requests,omitempty"`
	QueueCapacity         int      `json:"queue_capacity,omitempty"`
	QueueTimeoutMS        int      `json:"queue_timeout_ms,omitempty"`
	RateLimitRPM          int      `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM          int      `json:"rate_limit_tpm,omitempty"`
	Enabled               bool     `json:"enabled"`
	RuntimeState          string   `json:"runtime_state"`
	LatencyEWMAms         float64  `json:"latency_ewma_ms,omitempty"`
	FailureEWMA           float64  `json:"failure_ewma,omitempty"`
}

type DeploymentController interface {
	ListModelDeployments(context.Context) []ModelDeployment
	CreateModelDeployment(ModelDeployment) (ModelDeployment, error)
	UpdateModelDeployment(string, ModelDeployment) (ModelDeployment, error)
	DeleteModelDeployment(string) error
}

var ErrDeploymentNotFound = errors.New("model deployment not found")
var ErrDeploymentExists = errors.New("model deployment already exists")
var ErrDeploymentInUse = errors.New("model deployment is used by a model group")
var ErrInvalidDeployment = errors.New("invalid model deployment")
var ErrUnsupportedProviderCapability = errors.New("model deployment capability is not supported by the provider adapter")

type deploymentRegistry struct {
	current atomic.Pointer[map[string]ModelDeployment]
}

type endpointRegistry struct {
	current atomic.Pointer[[]Endpoint]
}

func (r *Router) ListModelDeployments(ctx context.Context) []ModelDeployment {
	_ = r.refreshControlPlane(ctx)
	if r == nil {
		return nil
	}
	if r.deployments == nil {
		return nil
	}
	current := r.deployments.current.Load()
	if current == nil {
		return nil
	}
	result := make([]ModelDeployment, 0, len(*current))
	for _, deployment := range *current {
		deployment.Models = append([]string(nil), deployment.Models...)
		deployment.Capabilities = append([]string(nil), deployment.Capabilities...)
		deployment.RuntimeState = "disabled"
		if deployment.Enabled {
			deployment.RuntimeState = "available"
			for _, endpoint := range r.configuredEndpoints() {
				if endpoint.Name == deployment.ID {
					if !r.health.available(ctx, endpoint) {
						deployment.RuntimeState = "cooling_down"
					}
					adaptive := r.adaptive.snapshot(endpoint.Name)
					deployment.LatencyEWMAms = adaptive.latencyEWMA
					deployment.FailureEWMA = adaptive.failureEWMA
					break
				}
			}
		}
		result = append(result, deployment)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority == result[j].Priority {
			return result[i].ID < result[j].ID
		}
		return result[i].Priority < result[j].Priority
	})
	return result
}

func (r *Router) UpdateModelDeployment(id string, deployment ModelDeployment) (ModelDeployment, error) {
	previous, unlock, err := r.beginControlMutation(context.Background())
	if err != nil {
		return ModelDeployment{}, err
	}
	defer unlock()
	if r == nil || r.deployments == nil {
		return ModelDeployment{}, ErrDeploymentNotFound
	}
	id = strings.TrimSpace(id)
	current := r.deployments.current.Load()
	if current == nil {
		return ModelDeployment{}, ErrDeploymentNotFound
	}
	existing, found := (*current)[id]
	if !found {
		return ModelDeployment{}, ErrDeploymentNotFound
	}
	deployment.ID = id
	if deployment.ProviderID == "" {
		deployment.ProviderID = existing.ProviderID
	}
	if !deployment.CredentialSet {
		deployment.CredentialID = existing.CredentialID
	}
	if deployment.Capabilities == nil {
		deployment.Capabilities = append([]string(nil), existing.Capabilities...)
	}
	if err := r.validateDeployment(deployment); err != nil {
		return ModelDeployment{}, ErrInvalidDeployment
	}
	providerConfig, found := r.managedProvider(deployment.ProviderID)
	if !found {
		return ModelDeployment{}, ErrInvalidDeployment
	}
	deployment.ProviderType = providerConfig.Type
	endpoint, err := r.endpointForDeployment(deployment)
	if err != nil {
		if errors.Is(err, ErrUnsupportedProviderCapability) {
			return ModelDeployment{}, err
		}
		return ModelDeployment{}, ErrInvalidDeployment
	}
	if deployment.Weight == 0 {
		deployment.Weight = 1
	}
	deployment.Models = append([]string(nil), deployment.Models...)
	deployment.Capabilities = append([]string(nil), deployment.Capabilities...)
	next := make(map[string]ModelDeployment, len(*current))
	for key, value := range *current {
		next[key] = value
	}
	next[id] = deployment
	r.deployments.current.Store(&next)
	r.replaceRuntimeEndpoint(id, endpoint)
	if err := r.persistControlMutation(context.Background(), previous); err != nil {
		return ModelDeployment{}, err
	}
	return deployment, nil
}

func (r *Router) CreateModelDeployment(deployment ModelDeployment) (ModelDeployment, error) {
	previous, unlock, err := r.beginControlMutation(context.Background())
	if err != nil {
		return ModelDeployment{}, err
	}
	defer unlock()
	if r == nil || r.deployments == nil {
		return ModelDeployment{}, ErrInvalidDeployment
	}
	deployment.ID = strings.TrimSpace(deployment.ID)
	current := r.deployments.current.Load()
	if _, found := (*current)[deployment.ID]; found {
		return ModelDeployment{}, ErrDeploymentExists
	}
	if err := r.validateDeployment(deployment); err != nil {
		return ModelDeployment{}, err
	}
	providerConfig, found := r.managedProvider(deployment.ProviderID)
	if !found {
		return ModelDeployment{}, ErrInvalidDeployment
	}
	deployment.ProviderType = providerConfig.Type
	if deployment.Weight == 0 {
		deployment.Weight = 1
	}
	endpoint, err := r.endpointForDeployment(deployment)
	if err != nil {
		if errors.Is(err, ErrUnsupportedProviderCapability) {
			return ModelDeployment{}, err
		}
		return ModelDeployment{}, ErrInvalidDeployment
	}
	deployment.Models = append([]string(nil), deployment.Models...)
	deployment.Capabilities = append([]string(nil), deployment.Capabilities...)
	next := make(map[string]ModelDeployment, len(*current)+1)
	for key, value := range *current {
		next[key] = value
	}
	next[deployment.ID] = deployment
	r.deployments.current.Store(&next)
	r.replaceRuntimeEndpoint(deployment.ID, endpoint)
	if err := r.persistControlMutation(context.Background(), previous); err != nil {
		return ModelDeployment{}, err
	}
	return deployment, nil
}

func (r *Router) DeleteModelDeployment(id string) error {
	previous, unlock, err := r.beginControlMutation(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	if r == nil || r.deployments == nil {
		return ErrDeploymentNotFound
	}
	id = strings.TrimSpace(id)
	current := r.deployments.current.Load()
	if _, found := (*current)[id]; !found {
		return ErrDeploymentNotFound
	}
	if r.modelGroups != nil && r.modelGroups.current.Load() != nil {
		for _, group := range *r.modelGroups.current.Load() {
			if containsDeployment(group.DeploymentIDs, id) {
				return ErrDeploymentInUse
			}
		}
	}
	next := make(map[string]ModelDeployment, len(*current)-1)
	for key, value := range *current {
		if key != id {
			next[key] = value
		}
	}
	r.deployments.current.Store(&next)
	r.removeRuntimeEndpoint(id)
	r.deploymentHealth.delete(id)
	return r.persistControlMutation(context.Background(), previous)
}

func (r Router) runtimeEndpoints() []Endpoint {
	_ = (&r).refreshControlPlane(context.Background())
	configured := r.configuredEndpoints()
	if r.deployments == nil {
		return configured
	}
	current := r.deployments.current.Load()
	if current == nil {
		return configured
	}
	result := make([]Endpoint, 0, len(configured))
	for _, endpoint := range configured {
		deployment, found := (*current)[endpoint.Name]
		if found && !deployment.Enabled {
			continue
		}
		if found {
			providerConfig, exists := r.managedProvider(deployment.ProviderID)
			if !exists || !providerConfig.Enabled {
				continue
			}
		}
		if found {
			endpoint.Models = append([]string(nil), deployment.Models...)
			endpoint.Capabilities = append([]string(nil), deployment.Capabilities...)
			endpoint.Priority = deployment.Priority
			endpoint.Weight = deployment.Weight
			endpoint.GuardrailPolicy = deployment.GuardrailPolicy
			endpoint.Anonymization = ""
			endpoint.AnonymizationRules = nil
		}
		if endpoint.GuardrailPolicy != "" && r.guardrails != nil {
			policies := r.guardrails.current.Load()
			policy, exists := GuardrailPolicy{}, false
			if policies != nil {
				policy, exists = (*policies)[endpoint.GuardrailPolicy]
			}
			endpoint.GuardrailPolicyValid = exists && policy.Enabled
			if endpoint.GuardrailPolicyValid {
				endpoint.DLPEnabled = policy.DLP
				endpoint.OutputDLPEnabled = policy.OutputDLP && policy.DLP
				endpoint.AVEnabled = policy.AV
				endpoint.Anonymization = policy.Anonymization
				endpoint.AnonymizationRules = append([]string(nil), policy.AnonymizationRules...)
			}
		}
		result = append(result, endpoint)
	}
	if r.modelGroups != nil && r.modelGroups.current.Load() != nil {
		groups := r.modelGroups.current.Load()
		for index := range result {
			aliases := make(map[string]string, len(result[index].ModelAliases))
			for alias, upstream := range result[index].ModelAliases {
				aliases[alias] = upstream
			}
			result[index].ModelAliases = aliases
			for _, group := range *groups {
				if !group.Enabled || !containsDeployment(group.DeploymentIDs, result[index].Name) {
					continue
				}
				if !containsDeployment(result[index].Models, group.ID) {
					result[index].Models = append(result[index].Models, group.ID)
				}
				if result[index].ModelAliases == nil {
					result[index].ModelAliases = map[string]string{}
				}
				if deployment, found := (*current)[result[index].Name]; found && deployment.UpstreamModel != "" {
					result[index].ModelAliases[group.ID] = deployment.UpstreamModel
				}
			}
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Priority < result[j].Priority })
	return result
}

func containsDeployment(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (r *Router) validateDeployment(deployment ModelDeployment) error {
	if strings.TrimSpace(deployment.ID) == "" || len(deployment.ID) > 128 || strings.TrimSpace(deployment.ProviderID) == "" || len(deployment.ProviderID) > 128 || len(deployment.CredentialID) > 128 || len(deployment.UpstreamModel) > 256 || deployment.Priority < 0 || deployment.Weight < 0 || len(deployment.Models) == 0 || len(deployment.Models) > 128 || !validDeploymentStrings(deployment.Models) || !validDeploymentCapabilities(deployment.Capabilities) || len(deployment.GuardrailPolicy) > 128 || !validDeploymentOperations(deployment) {
		return ErrInvalidDeployment
	}
	if deployment.CredentialID != "" {
		if _, err := r.providerCredentialSecret(deployment.ProviderID, deployment.CredentialID); err != nil {
			return ErrInvalidDeployment
		}
	}
	return nil
}

func validDeploymentCapabilities(capabilities []string) bool {
	seen := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		if !ValidModelCapability(capability) || seen[capability] {
			return false
		}
		seen[capability] = true
	}
	requiresChatOrResponses := []string{"stream", "tools", "structured_output", "vision", "file_input"}
	for _, capability := range requiresChatOrResponses {
		if seen[capability] && !seen["chat"] && !seen["responses"] && !seen["interactions"] {
			return false
		}
	}
	if seen["bedrock_invoke"] && !seen["chat"] && !seen["responses"] {
		return false
	}
	for _, capability := range []string{"web_search", "web_fetch", "tool_search", "memory_tool", "bash_tool", "text_editor_tool", "computer_toolset", "browser_toolset", "thinking", "zero_output", "inference_geo", "context_management", "tool_result_error", "document_citations", "document_metadata", "document_text", "audio", "audio_input", "video_input", "prompt_cache", "assistant_prefill", "gemini_code_execution", "url_context", "google_maps"} {
		if seen[capability] && !seen["chat"] {
			return false
		}
	}
	if seen["background_responses"] && !seen["responses"] {
		return false
	}
	if seen["interaction_agents"] && !seen["interactions"] {
		return false
	}
	if seen["interaction_environment_reuse"] && (!seen["interactions"] || !seen["interaction_agents"]) {
		return false
	}
	if seen["gemini_safety_settings"] && !seen["chat"] {
		return false
	}
	if seen["background_interactions"] && !seen["interactions"] {
		return false
	}
	if (seen["video_remix"] || seen["video_extension"]) && !seen["video"] {
		return false
	}
	if seen["container_files"] && !seen["container"] {
		return false
	}
	if seen["container_network"] && !seen["container"] {
		return false
	}
	return !seen["mcp"] || (seen["responses"] && seen["tools"])
}

func ValidModelCapability(capability string) bool {
	switch capability {
	case "chat", "responses", "interactions", "interaction_agents", "interaction_environment_reuse", "gemini_safety_settings", "background_interactions", "embeddings", "rerank", "moderation",
		"image_generation", "image_edit", "image_variation",
		"audio_transcription", "audio_translation", "audio_speech", "ocr", "search", "skills", "fine_tuning", "video", "video_remix", "video_extension", "container", "container_files", "container_network", "sandbox", "realtime",
		"stream", "tools", "structured_output", "mcp", "vision",
		"web_search", "web_fetch", "tool_search", "memory_tool", "bash_tool", "text_editor_tool", "computer_toolset", "browser_toolset", "thinking", "zero_output", "inference_geo", "context_management", "tool_result_error", "document_citations", "document_metadata", "document_text", "audio", "audio_input", "video_input", "prompt_cache", "assistant_prefill", "background_responses", "file_input", "bedrock_invoke", "gemini_code_execution", "url_context", "google_maps":
		return true
	default:
		return false
	}
}

func validDeploymentOperations(deployment ModelDeployment) bool {
	if !(deployment.RequestTimeoutMS >= 0 && deployment.RequestTimeoutMS <= 600000 &&
		deployment.MaxRetries >= 0 && deployment.MaxRetries <= 10 &&
		deployment.CooldownAfterFailures >= 0 && deployment.CooldownAfterFailures <= 100 &&
		deployment.CooldownSeconds >= 0 && deployment.CooldownSeconds <= 86400 &&
		deployment.MaxParallelRequests >= 0 && deployment.MaxParallelRequests <= 100000 &&
		deployment.QueueCapacity >= 0 && deployment.QueueCapacity <= 100000 &&
		deployment.QueueTimeoutMS >= 0 && deployment.QueueTimeoutMS <= 600000 &&
		deployment.RateLimitRPM >= 0 && deployment.RateLimitRPM <= 10000000 &&
		deployment.RateLimitTPM >= 0 && deployment.RateLimitTPM <= 1000000000) {
		return false
	}
	if deployment.MaxParallelRequests == 0 {
		return deployment.QueueCapacity == 0 && deployment.QueueTimeoutMS == 0
	}
	if deployment.QueueCapacity == 0 {
		return deployment.QueueTimeoutMS == 0
	}
	return deployment.QueueTimeoutMS > 0
}

func (r *Router) managedProvider(id string) (ManagedProvider, bool) {
	if r == nil || r.providers == nil || r.providers.current.Load() == nil {
		return ManagedProvider{}, false
	}
	item, found := (*r.providers.current.Load())[strings.TrimSpace(id)]
	return item, found
}

func (r *Router) endpointForDeployment(deployment ModelDeployment) (Endpoint, error) {
	managed, found := r.managedProvider(deployment.ProviderID)
	if !found {
		return Endpoint{}, ErrInvalidDeployment
	}
	return r.endpointForManagedDeployment(deployment, managed)
}

func (r *Router) endpointForManagedDeployment(deployment ModelDeployment, managed ManagedProvider) (Endpoint, error) {
	secret, err := r.providerCredentialSecret(deployment.ProviderID, deployment.CredentialID)
	if err != nil {
		return Endpoint{}, err
	}
	return r.endpointForManagedDeploymentWithSecret(deployment, managed, secret)
}

func (r *Router) endpointForManagedDeploymentWithSecret(deployment ModelDeployment, managed ManagedProvider, secret string) (Endpoint, error) {
	providerConfig := config.ProviderEndpointConfig{Type: managed.Type, BaseURL: managed.BaseURL, APIKey: secret, Stream: hasCapability(deployment.Capabilities, "stream"), APIVersion: managed.APIVersion, AuthType: managed.AuthType, Region: managed.Region}
	client := providerFor(providerConfig)
	if managed.Type == "bedrock" && managed.AuthType == "aws_sigv4" {
		bedrock := NewBedrockWithAuth(providerConfig.BaseURL, providerConfig.APIKey, providerConfig.AuthType, providerConfig.Region)
		bedrock.aws = r.awsCredentialSource(managed.ID, deployment.CredentialID, secret, managed.Region)
		client = bedrock
	}
	if client == nil {
		return Endpoint{}, ErrInvalidDeployment
	}
	aliases := map[string]string{}
	if deployment.UpstreamModel != "" {
		for _, model := range deployment.Models {
			aliases[model] = deployment.UpstreamModel
		}
	}
	endpoint := Endpoint{Name: deployment.ID, ProviderID: deployment.ProviderID, Type: managed.Type, Models: append([]string(nil), deployment.Models...), Capabilities: append([]string(nil), deployment.Capabilities...), Priority: deployment.Priority, Weight: deployment.Weight, GuardrailPolicy: deployment.GuardrailPolicy, GuardrailPolicyValid: true, ModelAliases: aliases, Provider: client, Admission: newAdmissionController(deployment.MaxParallelRequests, deployment.QueueCapacity, time.Duration(deployment.QueueTimeoutMS)*time.Millisecond), BaseURL: managed.BaseURL, CredentialID: deployment.CredentialID, RequestTimeout: time.Duration(deployment.RequestTimeoutMS) * time.Millisecond, MaxRetries: deployment.MaxRetries, CooldownAfterFailures: deployment.CooldownAfterFailures, Cooldown: time.Duration(deployment.CooldownSeconds) * time.Second, RateLimitRPM: deployment.RateLimitRPM, RateLimitTPM: deployment.RateLimitTPM, ProviderRateLimitRPM: managed.RateLimitRPM, ProviderRateLimitTPM: managed.RateLimitTPM}
	for _, capability := range deployment.Capabilities {
		if !supportsManagedAdapterCapability(endpoint, capability) {
			return Endpoint{}, fmt.Errorf("%w: %s does not support %s", ErrUnsupportedProviderCapability, managed.Type, capability)
		}
	}
	return endpoint, nil
}

func supportsManagedAdapterCapability(endpoint Endpoint, capability string) bool {
	if !endpoint.supportsCapabilities(capability) {
		return false
	}
	switch capability {
	case "chat", "responses":
		return true
	case "completions":
		_, ok := endpoint.Provider.(CompletionClient)
		return ok
	case "interactions":
		_, ok := endpoint.Provider.(InteractionClient)
		return ok
	case "interaction_agents":
		_, ok := endpoint.Provider.(InteractionClient)
		return ok && endpoint.Type == "gemini"
	case "interaction_environment_reuse":
		_, ok := endpoint.Provider.(InteractionClient)
		return ok && endpoint.Type == "gemini"
	case "gemini_safety_settings":
		return endpoint.Type == "gemini"
	case "background_interactions":
		_, creates := endpoint.Provider.(InteractionClient)
		_, lifecycle := endpoint.Provider.(InteractionResourceClient)
		return creates && lifecycle && endpoint.Type == "gemini"
	case "count_tokens":
		_, ok := endpoint.Provider.(TokenCountClient)
		return ok
	case "embeddings":
		_, ok := endpoint.Provider.(EmbeddingClient)
		return ok
	case "rerank":
		_, ok := endpoint.Provider.(RerankClient)
		return ok
	case "moderation":
		_, ok := endpoint.Provider.(ModerationClient)
		return ok
	case "image_generation":
		_, ok := endpoint.Provider.(ImageGenerationClient)
		return ok
	case "image_edit":
		_, ok := endpoint.Provider.(ImageEditClient)
		return ok
	case "image_variation":
		_, ok := endpoint.Provider.(ImageVariationClient)
		return ok
	case "audio_transcription":
		_, ok := endpoint.Provider.(AudioTranscriptionClient)
		return ok
	case "audio_translation":
		_, ok := endpoint.Provider.(AudioTranslationClient)
		return ok
	case "audio_speech":
		_, ok := endpoint.Provider.(AudioSpeechClient)
		return ok
	case "search":
		_, ok := endpoint.Provider.(SearchClient)
		return ok
	case "ocr":
		_, ok := endpoint.Provider.(OCRClient)
		return ok
	case "skills":
		_, ok := endpoint.Provider.(SkillClient)
		return ok
	case "fine_tuning":
		_, ok := endpoint.Provider.(FineTuningClient)
		return ok && (endpoint.Type == "openai" || endpoint.Type == "openai-compatible")
	case "video":
		_, ok := endpoint.Provider.(VideoClient)
		return ok && (endpoint.Type == "openai" || endpoint.Type == "openai-compatible" || endpoint.Type == "xai")
	case "video_remix":
		_, ok := endpoint.Provider.(VideoClient)
		return ok && (endpoint.Type == "openai" || endpoint.Type == "openai-compatible" || endpoint.Type == "xai")
	case "video_extension":
		_, ok := endpoint.Provider.(VideoExtensionClient)
		return ok && endpoint.Type == "xai"
	case "container":
		_, ok := endpoint.Provider.(ContainerClient)
		return ok && (endpoint.Type == "openai" || endpoint.Type == "openai-compatible")
	case "container_files":
		_, ok := endpoint.Provider.(ContainerFileClient)
		return ok && (endpoint.Type == "openai" || endpoint.Type == "openai-compatible")
	case "container_network":
		_, ok := endpoint.Provider.(ContainerClient)
		return ok && (endpoint.Type == "openai" || endpoint.Type == "openai-compatible")
	case "sandbox":
		_, ok := endpoint.Provider.(SandboxClient)
		return ok && endpoint.Type == "opensandbox"
	case "realtime":
		_, ok := endpoint.Provider.(RealtimeClient)
		return ok && (endpoint.Type == "openai" || endpoint.Type == "openai-compatible")
	case "background_responses":
		if endpoint.Type != "openai" && endpoint.Type != "openai-compatible" && endpoint.Type != "azure-openai" {
			return false
		}
		_, retrieves := endpoint.Provider.(responseRetrieveClient)
		_, cancels := endpoint.Provider.(responseCancelClient)
		return retrieves && cancels
	case "stream":
		_, chat := endpoint.Provider.(StreamingClient)
		_, responses := endpoint.Provider.(StreamingResponseClient)
		return chat || responses
	case "tools":
		client, ok := endpoint.Provider.(interface{ SupportsTools() bool })
		return ok && client.SupportsTools()
	case "structured_output":
		client, ok := endpoint.Provider.(interface{ SupportsStructuredOutput() bool })
		return ok && client.SupportsStructuredOutput()
	case "mcp":
		client, ok := endpoint.Provider.(MCPClient)
		return ok && client.SupportsMCP()
	case "vision":
		client, ok := endpoint.Provider.(VisionClient)
		return ok && client.SupportsVision()
	case "web_search":
		client, ok := endpoint.Provider.(interface{ SupportsWebSearch() bool })
		return ok && client.SupportsWebSearch()
	case "tool_search":
		client, ok := endpoint.Provider.(interface{ SupportsToolSearch() bool })
		return ok && client.SupportsToolSearch()
	case "memory_tool":
		client, ok := endpoint.Provider.(interface{ SupportsMemoryTool() bool })
		return ok && client.SupportsMemoryTool()
	case "bash_tool":
		client, ok := endpoint.Provider.(interface{ SupportsBashTool() bool })
		return ok && client.SupportsBashTool()
	case "text_editor_tool":
		client, ok := endpoint.Provider.(interface{ SupportsTextEditorTool() bool })
		return ok && client.SupportsTextEditorTool()
	case "computer_toolset":
		client, ok := endpoint.Provider.(interface{ SupportsComputerToolset() bool })
		return ok && client.SupportsComputerToolset()
	case "browser_toolset":
		client, ok := endpoint.Provider.(interface{ SupportsBrowserToolset() bool })
		return ok && client.SupportsBrowserToolset()
	case "thinking":
		client, ok := endpoint.Provider.(interface{ SupportsThinking() bool })
		return ok && client.SupportsThinking()
	case "zero_output":
		client, ok := endpoint.Provider.(interface{ SupportsZeroOutput() bool })
		return ok && client.SupportsZeroOutput()
	case "inference_geo":
		client, ok := endpoint.Provider.(interface{ SupportsInferenceGeo() bool })
		return ok && client.SupportsInferenceGeo()
	case "context_management":
		client, ok := endpoint.Provider.(interface{ SupportsContextManagement() bool })
		return ok && client.SupportsContextManagement()
	case "tool_result_error":
		client, ok := endpoint.Provider.(interface{ SupportsToolResultError() bool })
		return ok && client.SupportsToolResultError()
	case "document_citations":
		client, ok := endpoint.Provider.(interface{ SupportsDocumentCitations() bool })
		return ok && client.SupportsDocumentCitations()
	case "document_metadata":
		client, ok := endpoint.Provider.(interface{ SupportsDocumentMetadata() bool })
		return ok && client.SupportsDocumentMetadata()
	case "document_text":
		client, ok := endpoint.Provider.(interface{ SupportsTextDocuments() bool })
		return ok && client.SupportsTextDocuments()
	case "gemini_code_execution":
		client, ok := endpoint.Provider.(interface{ SupportsCodeExecution() bool })
		return endpoint.Type == "gemini" && ok && client.SupportsCodeExecution()
	case "url_context":
		client, ok := endpoint.Provider.(interface{ SupportsURLContext() bool })
		return endpoint.Type == "gemini" && ok && client.SupportsURLContext()
	case "google_maps":
		client, ok := endpoint.Provider.(interface{ SupportsGoogleMaps() bool })
		return endpoint.Type == "gemini" && ok && client.SupportsGoogleMaps()
	case "web_fetch":
		client, ok := endpoint.Provider.(interface{ SupportsWebFetch() bool })
		return ok && client.SupportsWebFetch()
	case "audio":
		client, ok := endpoint.Provider.(interface{ SupportsChatAudio() bool })
		return ok && client.SupportsChatAudio()
	case "audio_input":
		client, ok := endpoint.Provider.(interface{ SupportsAudioInput() bool })
		return ok && client.SupportsAudioInput()
	case "video_input":
		client, ok := endpoint.Provider.(interface{ SupportsVideoInput() bool })
		return ok && client.SupportsVideoInput()
	case "file_input":
		if endpoint.Type != "openai" && endpoint.Type != "openai-compatible" && endpoint.Type != "azure-openai" && endpoint.Type != "gemini" && endpoint.Type != "anthropic" {
			return false
		}
		client, ok := endpoint.Provider.(interface{ SupportsFileInput() bool })
		return ok && client.SupportsFileInput()
	case "bedrock_invoke":
		client, ok := endpoint.Provider.(interface{ SupportsBedrockInvoke() bool })
		return endpoint.Type == "bedrock" && ok && client.SupportsBedrockInvoke()
	case "prompt_cache":
		client, ok := endpoint.Provider.(interface{ SupportsPromptCache() bool })
		return ok && client.SupportsPromptCache()
	case "assistant_prefill":
		client, ok := endpoint.Provider.(interface{ SupportsAssistantPrefill() bool })
		return ok && client.SupportsAssistantPrefill()
	default:
		return false
	}
}

func (r *Router) configuredEndpoints() []Endpoint {
	if r == nil {
		return nil
	}
	if r.endpointState != nil {
		if current := r.endpointState.current.Load(); current != nil {
			return append([]Endpoint(nil), (*current)...)
		}
	}
	return append([]Endpoint(nil), r.endpoints...)
}

func (r *Router) replaceRuntimeEndpoint(id string, endpoint Endpoint) {
	current := r.configuredEndpoints()
	next := make([]Endpoint, 0, len(current)+1)
	replaced := false
	for _, item := range current {
		if item.Name == id {
			next = append(next, endpoint)
			replaced = true
		} else {
			next = append(next, item)
		}
	}
	if !replaced {
		next = append(next, endpoint)
	}
	sort.SliceStable(next, func(i, j int) bool { return next[i].Priority < next[j].Priority })
	if r.endpointState == nil {
		r.endpointState = &endpointRegistry{}
	}
	r.endpointState.current.Store(&next)
}

func (r *Router) removeRuntimeEndpoint(id string) {
	current := r.configuredEndpoints()
	next := make([]Endpoint, 0, len(current))
	for _, item := range current {
		if item.Name != id {
			next = append(next, item)
		}
	}
	if r.endpointState == nil {
		r.endpointState = &endpointRegistry{}
	}
	r.endpointState.current.Store(&next)
}

func validDeploymentStrings(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return false
		}
	}
	return true
}
