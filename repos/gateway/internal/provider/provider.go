package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type Client interface {
	ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
	Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error)
}

type Provider interface {
	ChatCompletions(ctx context.Context, req modules.RequestContext) (openai.ChatCompletionResponse, error)
	StreamChatCompletions(ctx context.Context, req modules.RequestContext, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error)
	Responses(ctx context.Context, req modules.RequestContext) (openai.ResponseResponse, error)
	StreamResponses(ctx context.Context, req modules.RequestContext, write ResponseStreamWriter) (openai.ResponseResponse, bool, error)
	Models() []openai.Model
}

type InteractionProvider interface {
	CanRouteInteraction(context.Context, openai.InteractionRequest) bool
	Interactions(context.Context, modules.RequestContext, openai.InteractionRequest) (openai.InteractionResponse, error)
}

type SandboxProvider interface {
	ExecuteSandbox(context.Context, modules.RequestContext) (openai.SandboxExecutionResult, error)
}

type InteractionClient interface {
	Interactions(context.Context, openai.InteractionRequest) (openai.InteractionResponse, error)
}

type StreamingInteractionProvider interface {
	StreamInteractions(context.Context, modules.RequestContext, openai.InteractionRequest, ResponseStreamWriter) (openai.InteractionResponse, bool, error)
}

type StreamingInteractionClient interface {
	StreamInteractions(context.Context, openai.InteractionRequest, ResponseStreamWriter) (openai.InteractionResponse, error)
}

type InteractionResourceProvider interface {
	ResolveInteractionResource(context.Context, modules.RequestContext, string) (string, error)
	RetrieveInteraction(context.Context, modules.RequestContext, string) (openai.InteractionResponse, error)
	CancelInteraction(context.Context, modules.RequestContext, string) (openai.InteractionResponse, error)
	DeleteInteraction(context.Context, modules.RequestContext, string) error
}

type InteractionResourceClient interface {
	RetrieveInteraction(context.Context, string) (openai.InteractionResponse, error)
	CancelInteraction(context.Context, string) (openai.InteractionResponse, error)
	DeleteInteraction(context.Context, string) error
}

type FineTuningBinding struct {
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	Deployment string `json:"deployment"`
}

type FineTuningProvider interface {
	CreateFineTuningJob(context.Context, modules.RequestContext, openai.FineTuningCreateRequest, func(context.Context, *modules.RequestContext) error) (openai.FineTuningJob, FineTuningBinding, error)
	RetrieveFineTuningJob(context.Context, FineTuningBinding, string) (openai.FineTuningJob, error)
	CancelFineTuningJob(context.Context, FineTuningBinding, string) (openai.FineTuningJob, error)
	PauseFineTuningJob(context.Context, FineTuningBinding, string) (openai.FineTuningJob, error)
	ResumeFineTuningJob(context.Context, FineTuningBinding, string) (openai.FineTuningJob, error)
	ListFineTuningEvents(context.Context, FineTuningBinding, string, FineTuningListOptions) (openai.FineTuningEventList, error)
	ListFineTuningCheckpoints(context.Context, FineTuningBinding, string, FineTuningListOptions) (openai.FineTuningCheckpointList, error)
	DeleteFineTunedModel(context.Context, FineTuningBinding, string) (openai.ModelDeletion, error)
}

type VideoBinding struct {
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	Deployment string `json:"deployment"`
}

type ContainerBinding struct {
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	Deployment string `json:"deployment"`
}

type CachedContentBinding struct {
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	Deployment string `json:"deployment"`
}

type CachedContentProvider interface {
	CreateCachedContent(context.Context, modules.RequestContext, openai.ChatCompletionRequest, string, openai.GeminiCachedContentExpiration, func(context.Context, *modules.RequestContext) error) (openai.GeminiCachedContent, CachedContentBinding, error)
	RetrieveCachedContent(context.Context, CachedContentBinding, string) (openai.GeminiCachedContent, error)
	UpdateCachedContent(context.Context, CachedContentBinding, string, openai.GeminiCachedContentExpiration) (openai.GeminiCachedContent, error)
	DeleteCachedContent(context.Context, CachedContentBinding, string) error
}

type ContainerProvider interface {
	CreateContainer(context.Context, modules.RequestContext, openai.ContainerCreateRequest, func(context.Context, *modules.RequestContext) error) (openai.Container, ContainerBinding, error)
	RetrieveContainer(context.Context, ContainerBinding, string) (openai.Container, error)
	DeleteContainer(context.Context, ContainerBinding, string) (openai.ContainerDeletion, error)
}

type ContainerFileProvider interface {
	CreateContainerFile(context.Context, ContainerBinding, string, ContainerFileUpload) (openai.ContainerFile, error)
	ListContainerFiles(context.Context, ContainerBinding, string, ContainerFileListOptions) (openai.ContainerFileList, error)
	RetrieveContainerFile(context.Context, ContainerBinding, string, string) (openai.ContainerFile, error)
	DeleteContainerFile(context.Context, ContainerBinding, string, string) (openai.ContainerDeletion, error)
	DownloadContainerFile(context.Context, ContainerBinding, string, string) (ContainerFileContent, error)
}

type VideoProvider interface {
	CreateVideo(context.Context, modules.RequestContext, openai.VideoCreateRequest, func(context.Context, *modules.RequestContext) error) (openai.Video, VideoBinding, error)
	RetrieveVideo(context.Context, VideoBinding, string) (openai.Video, error)
	DeleteVideo(context.Context, VideoBinding, string) (openai.VideoDeletion, error)
	DownloadVideoContent(context.Context, VideoBinding, string, string) (VideoContent, error)
	RemixVideo(context.Context, modules.RequestContext, VideoBinding, string, openai.VideoRemixRequest, func(context.Context, *modules.RequestContext) error) (openai.Video, VideoBinding, error)
}

type VideoExtensionProvider interface {
	ExtendVideo(context.Context, modules.RequestContext, VideoBinding, string, openai.VideoExtendRequest, func(context.Context, *modules.RequestContext) error) (openai.Video, VideoBinding, error)
}

type RealtimeProvider interface {
	OpenRealtime(context.Context, modules.RequestContext, string) (RealtimeConnection, modules.RequestContext, error)
}

type ResponseResourceResolver interface {
	ResolveResponseResource(ctx context.Context, req modules.RequestContext, id string) (string, error)
}

type ResponseResourceProvider interface {
	ResponseResourceResolver
	RetrieveResponse(ctx context.Context, req modules.RequestContext, id string) (openai.ResponseResponse, error)
}

type ResponseCancellationProvider interface {
	ResponseResourceResolver
	CancelResponse(ctx context.Context, req modules.RequestContext, id string) (openai.ResponseResponse, error)
}

type ResponseInputItemsOptions struct {
	After   string
	Limit   int
	Order   string
	Include []string
}

type ResponseInputItemsProvider interface {
	ResponseResourceResolver
	ListResponseInputItems(ctx context.Context, req modules.RequestContext, id string, options ResponseInputItemsOptions) (openai.ResponseInputItemList, error)
}

type ResponseDeletionProvider interface {
	ResponseResourceResolver
	DeleteResponse(ctx context.Context, req modules.RequestContext, id string) (openai.ResponseDeletion, error)
}

type ResponseCompactProvider interface {
	CompactResponse(ctx context.Context, req modules.RequestContext) (openai.CompactedResponse, error)
}

type ResponseCompactClient interface {
	CompactResponse(ctx context.Context, request openai.ResponseCompactRequest) (openai.CompactedResponse, error)
}

type ResponseInputTokenCountProvider interface {
	CountResponseInputTokens(ctx context.Context, req modules.RequestContext) (openai.ResponseInputTokenCount, error)
}

type ResponseInputTokenCountClient interface {
	CountResponseInputTokens(ctx context.Context, request openai.ResponseInputTokenCountRequest) (openai.ResponseInputTokenCount, error)
}

type CompletionProvider interface {
	Completions(ctx context.Context, req modules.RequestContext) (openai.CompletionResponse, error)
}

type StreamingCompletionProvider interface {
	StreamCompletions(ctx context.Context, req modules.RequestContext, write CompletionStreamWriter) (openai.CompletionResponse, bool, error)
}

type CompletionClient interface {
	Completions(ctx context.Context, request openai.CompletionRequest) (openai.CompletionResponse, error)
}

type StreamingCompletionClient interface {
	StreamCompletions(ctx context.Context, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error)
}

type EmbeddingProvider interface {
	Embeddings(ctx context.Context, req modules.RequestContext) (openai.EmbeddingResponse, error)
}

type EmbeddingClient interface {
	Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error)
}

type RerankProvider interface {
	Rerank(ctx context.Context, req modules.RequestContext) (openai.RerankResponse, error)
}

type RerankClient interface {
	Rerank(ctx context.Context, request openai.RerankRequest) (openai.RerankResponse, error)
}

type ModerationProvider interface {
	Moderations(ctx context.Context, req modules.RequestContext) (openai.ModerationResponse, error)
}

type ModerationClient interface {
	Moderations(ctx context.Context, request openai.ModerationRequest) (openai.ModerationResponse, error)
}

type ImageGenerationProvider interface {
	GenerateImage(ctx context.Context, req modules.RequestContext) (openai.ImageGenerationResponse, error)
}

type ImageGenerationClient interface {
	GenerateImage(ctx context.Context, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error)
}

type imageUnitUsageClient interface {
	UsesImageUnitUsage() bool
}

type StreamingImageGenerationProvider interface {
	StreamGenerateImage(ctx context.Context, req modules.RequestContext, write ImageGenerationStreamWriter) (openai.ImageGenerationResponse, bool, error)
}

type StreamingImageGenerationClient interface {
	StreamGenerateImage(ctx context.Context, request openai.ImageGenerationRequest, write ImageGenerationStreamWriter) (openai.ImageGenerationResponse, error)
}

type ImageEditProvider interface {
	EditImage(ctx context.Context, req modules.RequestContext) (openai.ImageGenerationResponse, error)
}

type ImageEditClient interface {
	EditImage(ctx context.Context, request openai.ImageEditRequest) (openai.ImageGenerationResponse, error)
}

type StreamingImageEditProvider interface {
	StreamEditImage(ctx context.Context, req modules.RequestContext, write ImageGenerationStreamWriter) (openai.ImageGenerationResponse, bool, error)
}

type StreamingImageEditClient interface {
	StreamEditImage(ctx context.Context, request openai.ImageEditRequest, write ImageGenerationStreamWriter) (openai.ImageGenerationResponse, error)
}

type ImageVariationProvider interface {
	CreateImageVariation(ctx context.Context, req modules.RequestContext) (openai.ImageGenerationResponse, error)
}

type ImageVariationClient interface {
	CreateImageVariation(ctx context.Context, request openai.ImageVariationRequest) (openai.ImageGenerationResponse, error)
}

type AudioTranscriptionProvider interface {
	TranscribeAudio(ctx context.Context, req modules.RequestContext) (openai.AudioTranscriptionResponse, error)
}

type AudioTranscriptionClient interface {
	TranscribeAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error)
}

type StreamingAudioTranscriptionProvider interface {
	StreamTranscribeAudio(ctx context.Context, req modules.RequestContext, write AudioTranscriptionStreamWriter) (openai.AudioTranscriptionResponse, bool, error)
}

type StreamingAudioTranscriptionClient interface {
	StreamTranscribeAudio(ctx context.Context, request openai.AudioTranscriptionRequest, write AudioTranscriptionStreamWriter) (openai.AudioTranscriptionResponse, error)
}

type AudioTranslationProvider interface {
	TranslateAudio(ctx context.Context, req modules.RequestContext) (openai.AudioTranscriptionResponse, error)
}

type AudioTranslationClient interface {
	TranslateAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error)
}

type AudioTranscriptionDurationReserver interface {
	ReserveAudioMilliseconds(request openai.AudioTranscriptionRequest) (int, error)
}

type AudioTranslationDurationReserver interface {
	ReserveTranslationAudioMilliseconds(request openai.AudioTranscriptionRequest) (int, error)
}

type AudioSpeechProvider interface {
	GenerateSpeech(ctx context.Context, req modules.RequestContext) (openai.AudioSpeechResponse, error)
}

type AudioSpeechClient interface {
	GenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error)
}

type StreamingAudioSpeechProvider interface {
	StreamGenerateSpeech(ctx context.Context, req modules.RequestContext, write AudioSpeechStreamWriter) (openai.AudioSpeechResponse, bool, error)
}

type StreamingAudioSpeechClient interface {
	StreamGenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest, write AudioSpeechStreamWriter) (openai.AudioSpeechResponse, error)
}

type SearchProvider interface {
	Search(ctx context.Context, req modules.RequestContext) (openai.SearchResponse, error)
}

type SearchClient interface {
	Search(ctx context.Context, request openai.SearchRequest) (openai.SearchResponse, error)
}

type OCRProvider interface {
	OCR(ctx context.Context, req modules.RequestContext) (openai.OCRResponse, error)
}

type OCRClient interface {
	OCR(ctx context.Context, request openai.OCRRequest) (openai.OCRResponse, error)
}

type MCPClient interface {
	SupportsMCP() bool
}

type VisionClient interface {
	SupportsVision() bool
}

type ChatCompletionStreamWriter func(payload string) error
type CompletionStreamWriter func(payload string) error
type ResponseStreamWriter func(event string, payload string) error
type ImageGenerationStreamWriter func(payload string) error
type AudioTranscriptionStreamWriter func(payload string) error
type AudioSpeechStreamWriter func(payload string) error

type StreamingClient interface {
	StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error)
}

// MessagesClient lets an adapter preserve a provider's native Messages wire
// contract while the router keeps the shared chat policy and accounting path.
type MessagesClient interface {
	Messages(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
}

type StreamingMessagesClient interface {
	StreamMessages(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error)
}

type StreamingResponseClient interface {
	StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error)
}

var ErrStreamingUnsupported = errors.New("streaming unsupported")

type Config struct {
	Default                 string
	Endpoints               []config.ProviderEndpointConfig
	GuardrailPolicies       map[string]config.GuardrailPolicyConfig
	Modules                 modules.Pipeline
	CacheTTL                time.Duration
	CacheMaxBytes           int
	CacheStore              ExactCacheStore
	Catalog                 modelcatalog.Catalog
	CatalogRegistry         *modelcatalog.Registry
	Observer                ProviderObserver
	RoutingStrategy         string
	AdaptiveEWMAAlpha       float64
	SessionStore            SessionStore
	CircuitStore            CircuitStore
	AffinityTTL             time.Duration
	ResponseOwnershipTTL    time.Duration
	SemanticCacheTTL        time.Duration
	SemanticCacheThreshold  float64
	SemanticCacheMaxEntries int
	SemanticCacheMaxBytes   int
	SemanticEmbeddingURL    string
	SemanticEmbeddingAPIKey string
	SemanticEmbeddingModel  string
	CredentialEncryptionKey []byte
	ControlPlaneStore       ControlPlaneStore
	ControlPlaneRefresh     time.Duration
	DeploymentQuotaStore    DeploymentQuotaStore
	AsyncJobs               asyncstate.Store
}

type ProviderObserver interface {
	ObserveProvider(endpoint, providerType, operation, result string, duration time.Duration)
	ObserveCache(operation, result string)
}

type Endpoint struct {
	Name                  string
	ProviderID            string
	Type                  string
	Models                []string
	Priority              int
	DLPEnabled            bool
	OutputDLPEnabled      bool
	AVEnabled             bool
	Anonymization         string
	AnonymizationRules    []string
	MaxRetries            int
	RetryPolicy           map[string]int
	CooldownAfterFailures int
	Cooldown              time.Duration
	RequestTimeout        time.Duration
	Admission             *admissionController
	GuardrailPolicy       string
	GuardrailPolicyValid  bool
	ModelAliases          map[string]string
	Weight                int
	Capabilities          []string
	Shadow                bool
	MirrorPercentage      float64
	MirrorTimeout         time.Duration
	Provider              Client
	BaseURL               string
	CredentialID          string
	RoutingModel          string
	FallbackType          string
	FallbackStage         int
	RateLimitRPM          int
	RateLimitTPM          int
	ProviderRateLimitRPM  int
	ProviderRateLimitTPM  int
}

type Router struct {
	defaultProvider  string
	endpoints        []Endpoint
	endpointState    *endpointRegistry
	modules          modules.Pipeline
	health           *endpointHealthTracker
	routeCounter     *atomic.Uint64
	cache            responseCache
	catalog          *modelcatalog.Registry
	observer         ProviderObserver
	routingStrategy  string
	adaptive         *adaptiveRouter
	affinity         affinityStore
	ownership        responseOwnershipStore
	semantic         *semanticResponseCache
	deployments      *deploymentRegistry
	providers        *managedProviderRegistry
	credentials      *credentialVault
	modelGroups      *modelGroupRegistry
	controlPlane     *controlPlaneRuntime
	guardrails       *guardrailRegistry
	adminState       *adminStateRegistry
	deploymentHealth *deploymentHealthRegistry
	retry            retryScheduler
	deploymentQuotas DeploymentQuotaStore
	awsCredentials   *awsCredentialRegistry
	asyncJobs        asyncstate.Store
}

func New(cfg Config) Provider {
	result, err := NewWithError(cfg)
	if err != nil {
		fallback := cfg
		fallback.ControlPlaneStore = nil
		result, _ = NewWithError(fallback)
	}
	return result
}

func NewWithError(cfg Config) (Provider, error) {
	endpoints := make([]Endpoint, 0, len(cfg.Endpoints))
	initialDeployments := make(map[string]ModelDeployment)
	initialProviders := make(map[string]ManagedProvider)
	for _, endpoint := range cfg.Endpoints {
		provider := providerFor(endpoint)
		if provider == nil {
			continue
		}

		dlpEnabled := endpoint.DLPEnabled
		outputDLPEnabled := false
		avEnabled := endpoint.AVEnabled
		anonymization := ""
		var anonymizationRules []string
		policyValid := true
		if endpoint.GuardrailPolicy != "" {
			policy, found := cfg.GuardrailPolicies[endpoint.GuardrailPolicy]
			policyValid = found
			if found {
				dlpEnabled = policy.DLP
				outputDLPEnabled = policy.OutputDLP && policy.DLP
				avEnabled = policy.AV
				anonymization = policy.Anonymization
				anonymizationRules = append([]string(nil), policy.AnonymizationRules...)
			}
		}
		mirrorPercentage := endpoint.MirrorPercentage
		if endpoint.Shadow && mirrorPercentage == 0 {
			mirrorPercentage = 100
		}
		mirrorTimeout := time.Duration(endpoint.MirrorTimeoutMS) * time.Millisecond
		if endpoint.Shadow && mirrorTimeout <= 0 {
			mirrorTimeout = 5 * time.Second
		}
		endpoints = append(endpoints, Endpoint{
			Name:                  endpoint.Name,
			ProviderID:            endpoint.Name,
			Type:                  endpoint.Type,
			Models:                endpoint.Models,
			Priority:              endpoint.Priority,
			DLPEnabled:            dlpEnabled,
			OutputDLPEnabled:      outputDLPEnabled,
			AVEnabled:             avEnabled,
			Anonymization:         anonymization,
			AnonymizationRules:    anonymizationRules,
			MaxRetries:            endpoint.MaxRetries,
			CooldownAfterFailures: endpoint.CooldownAfterFailures,
			Cooldown:              time.Duration(endpoint.CooldownSeconds) * time.Second,
			Admission:             newAdmissionController(endpoint.MaxParallelRequests, endpoint.QueueCapacity, time.Duration(endpoint.QueueTimeoutMS)*time.Millisecond),
			GuardrailPolicy:       endpoint.GuardrailPolicy,
			GuardrailPolicyValid:  policyValid,
			ModelAliases:          endpoint.ModelAliases,
			Weight:                endpoint.Weight,
			Capabilities:          endpoint.Capabilities,
			Shadow:                endpoint.Shadow,
			MirrorPercentage:      mirrorPercentage,
			MirrorTimeout:         mirrorTimeout,
			Provider:              provider,
			BaseURL:               strings.TrimRight(endpoint.BaseURL, "/"),
			RateLimitRPM:          endpoint.RateLimitRPM,
			RateLimitTPM:          endpoint.RateLimitTPM,
		})
		enabled := endpoint.Enabled == nil || *endpoint.Enabled
		deploymentWeight := endpoint.Weight
		if deploymentWeight == 0 {
			deploymentWeight = 1
		}
		upstreamModel := ""
		if len(endpoint.Models) == 1 {
			upstreamModel = endpoint.Models[0]
		}
		authType := ""
		region := ""
		if endpoint.Type == "azure-openai" || endpoint.Type == "gemini" {
			authType = normalizeAzureAuthType(endpoint.AuthType)
		} else if endpoint.Type == "bedrock" {
			authType = strings.ToLower(strings.TrimSpace(endpoint.AuthType))
			if authType == "" || authType == "api_key" {
				authType = "bearer"
			}
			if authType == "aws_sigv4" {
				region = strings.ToLower(strings.TrimSpace(endpoint.Region))
			}
		}
		initialProviders[endpoint.Name] = ManagedProvider{ID: endpoint.Name, Type: endpoint.Type, BaseURL: strings.TrimRight(endpoint.BaseURL, "/"), APIVersion: strings.TrimSpace(endpoint.APIVersion), AuthType: authType, Region: region, Enabled: enabled}
		initialDeployments[endpoint.Name] = ModelDeployment{ID: endpoint.Name, ProviderID: endpoint.Name, ProviderType: endpoint.Type, UpstreamModel: upstreamModel, Models: append([]string(nil), endpoint.Models...), Capabilities: append([]string(nil), endpoint.Capabilities...), Priority: endpoint.Priority, Weight: deploymentWeight, GuardrailPolicy: endpoint.GuardrailPolicy, MaxRetries: endpoint.MaxRetries, CooldownAfterFailures: endpoint.CooldownAfterFailures, CooldownSeconds: endpoint.CooldownSeconds, MaxParallelRequests: endpoint.MaxParallelRequests, QueueCapacity: endpoint.QueueCapacity, QueueTimeoutMS: endpoint.QueueTimeoutMS, RateLimitRPM: endpoint.RateLimitRPM, RateLimitTPM: endpoint.RateLimitTPM, Enabled: enabled}
	}

	hasPrimary := false
	for _, endpoint := range endpoints {
		if !endpoint.Shadow {
			hasPrimary = true
			break
		}
	}
	if !hasPrimary {
		endpoints = append(endpoints, Endpoint{Name: "demo", ProviderID: "demo", Type: "demo", Provider: Demo{}})
	}

	sort.SliceStable(endpoints, func(i, j int) bool {
		return endpoints[i].Priority < endpoints[j].Priority
	})

	registry := cfg.CatalogRegistry
	if registry == nil {
		registry = modelcatalog.NewRegistry(cfg.Catalog, nil, time.Second)
	}
	deploymentQuotas := cfg.DeploymentQuotaStore
	if deploymentQuotas == nil {
		deploymentQuotas = NewMemoryDeploymentQuotaStore()
	}
	router := &Router{
		defaultProvider:  cfg.Default,
		endpoints:        endpoints,
		modules:          cfg.Modules,
		health:           newEndpointHealthTracker(cfg.CircuitStore),
		routeCounter:     &atomic.Uint64{},
		cache:            newResponseCache(cfg.CacheTTL, cfg.CacheMaxBytes, cfg.CacheStore),
		catalog:          registry,
		observer:         cfg.Observer,
		routingStrategy:  strings.ToLower(strings.TrimSpace(cfg.RoutingStrategy)),
		adaptive:         newAdaptiveRouter(cfg.AdaptiveEWMAAlpha),
		deploymentQuotas: deploymentQuotas,
		asyncJobs:        cfg.AsyncJobs,
		awsCredentials:   &awsCredentialRegistry{current: make(map[string]managedAWSCredentialSource)},
		affinity:         newAffinityStore(cfg.AffinityTTL, cfg.SessionStore),
		ownership:        newResponseOwnershipStore(cfg.ResponseOwnershipTTL, cfg.SessionStore),
		semantic: newSemanticResponseCache(semanticCacheConfig{
			ttl: cfg.SemanticCacheTTL, threshold: cfg.SemanticCacheThreshold,
			maxEntries: cfg.SemanticCacheMaxEntries, maxBytes: cfg.SemanticCacheMaxBytes,
			embedder: newOpenAIEmbedder(cfg.SemanticEmbeddingURL, cfg.SemanticEmbeddingAPIKey, cfg.SemanticEmbeddingModel),
		}),
	}
	router.deployments = &deploymentRegistry{}
	router.deployments.current.Store(&initialDeployments)
	router.endpointState = &endpointRegistry{}
	router.endpointState.current.Store(&endpoints)
	router.providers = &managedProviderRegistry{}
	router.providers.current.Store(&initialProviders)
	router.credentials = newCredentialVault(cfg.CredentialEncryptionKey)
	router.modelGroups = &modelGroupRegistry{}
	emptyModelGroups := map[string]ModelGroup{}
	router.modelGroups.current.Store(&emptyModelGroups)
	initialGuardrails := make(map[string]GuardrailPolicy, len(cfg.GuardrailPolicies))
	for name, policy := range cfg.GuardrailPolicies {
		initialGuardrails[name] = GuardrailPolicy{Name: name, DLP: policy.DLP, OutputDLP: policy.OutputDLP && policy.DLP, AV: policy.AV, Anonymization: policy.Anonymization, AnonymizationRules: append([]string(nil), policy.AnonymizationRules...), Enabled: true}
	}
	router.guardrails = &guardrailRegistry{}
	router.guardrails.current.Store(&initialGuardrails)
	emptyAdminState := json.RawMessage(nil)
	router.adminState = &adminStateRegistry{}
	router.adminState.current.Store(&emptyAdminState)
	router.deploymentHealth = newDeploymentHealthRegistry()
	router.retry = newRetryScheduler()
	if cfg.ControlPlaneStore != nil {
		// Read a legacy Redis-backed runtime catalog once before the control
		// plane becomes its authoritative, versioned owner.
		registry.SetAuthoritative(registry.Current(context.Background()))
		refresh := cfg.ControlPlaneRefresh
		if refresh <= 0 {
			refresh = DefaultControlPlaneRefreshInterval
		}
		router.controlPlane = &controlPlaneRuntime{store: cfg.ControlPlaneStore, refreshInterval: refresh}
		snapshot, found, err := cfg.ControlPlaneStore.Load(context.Background())
		if err != nil {
			return nil, err
		}
		if found && snapshot.Revision > 0 {
			if err := router.applyControlPlaneSnapshot(snapshot); err != nil {
				return nil, err
			}
			router.controlPlane.revision = snapshot.Revision
		} else {
			revision, err := cfg.ControlPlaneStore.Save(context.Background(), 0, router.controlPlaneSnapshot())
			if err != nil {
				return nil, err
			}
			router.controlPlane.revision = revision
		}
		router.controlPlane.nextRefresh.Store(time.Now().Add(refresh).UnixNano())
	}
	return router, nil
}

func (r Router) ChatCompletions(ctx context.Context, req modules.RequestContext) (openai.ChatCompletionResponse, error) {
	request := req.Request
	candidates := r.routeCandidates(ctx, req, request, requiredChatCapabilities(request, false)...)
	var err error
	candidates, err = bindChatAudioHistory(request, candidates)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if len(candidates) == 0 {
		return openai.ChatCompletionResponse{}, fmt.Errorf("no provider endpoint for provider=%q model=%q", request.Provider, request.Model)
	}

	var errs []error
	var lastAttempt *modules.RequestContext
	mirrored := false
	totalRetries := 0
	fallbackCount := 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		progress.enter(endpoint)
		if err := validateChatAdapter(endpoint.Provider, request); err != nil {
			return openai.ChatCompletionResponse{}, err
		}
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ChatCompletionResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			wrapped := fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			errs = append(errs, wrapped)
			progress.fail(err)
			continue
		}
		if err := audioAnonymizationError(request, attemptCtx); err != nil {
			r.modules.RunFailure(ctx, &attemptCtx, err)
			return openai.ChatCompletionResponse{}, err
		}

		started := time.Now()
		lastAttempt = &attemptCtx
		cacheKey := providerCacheKey("chat", attemptCtx)
		if payload, found, cacheErr := r.cacheGet(ctx, cacheKey); found {
			if response, ok := decodeCached[openai.ChatCompletionResponse](payload); ok {
				attemptCtx.Metadata["provider.cache.status"] = "hit"
				attemptCtx.Metadata["provider.cache.kind"] = "exact"
				attemptCtx.Metadata["provider.status"] = "ok"
				attemptCtx.Metadata["provider.latency_ms"] = "0"
				setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
				response.Usage = openai.Usage{}
				attemptCtx.Response = &response
				modules.DeanonymizeResponse(&attemptCtx, &response)
				if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
					return openai.ChatCompletionResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
				}
				return response, nil
			}
		} else if cacheErr != nil {
			attemptCtx.Metadata["provider.cache.status"] = "error"
			log.Printf("provider cache get failed: %v", cacheErr)
		}
		semanticScope, semanticVector := "", []float64(nil)
		if scope, text, eligible := semanticRequest(attemptCtx, endpoint); eligible && r.semantic != nil {
			vector, embedErr := r.semantic.embedder.embed(ctx, text)
			if embedErr != nil {
				if r.observer != nil {
					r.observer.ObserveCache("semantic_get", "error")
				}
				log.Printf("semantic cache embedding failed: %v", embedErr)
			} else {
				semanticScope, semanticVector = scope, vector
				if payload, found := r.semantic.lookup(scope, vector); found {
					if cached, ok := decodeCached[openai.ChatCompletionResponse](payload); ok {
						if r.observer != nil {
							r.observer.ObserveCache("semantic_get", "hit")
						}
						attemptCtx.Metadata["provider.cache.status"] = "hit"
						attemptCtx.Metadata["provider.cache.kind"] = "semantic"
						attemptCtx.Metadata["provider.status"] = "ok"
						attemptCtx.Metadata["provider.latency_ms"] = "0"
						setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
						cached.Usage = openai.Usage{}
						attemptCtx.Response = &cached
						modules.DeanonymizeResponse(&attemptCtx, &cached)
						if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
							return openai.ChatCompletionResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
						}
						return cached, nil
					}
				}
				if r.observer != nil {
					r.observer.ObserveCache("semantic_get", "miss")
				}
			}
		}
		if !mirrored {
			r.mirrorChat(ctx, req.RequestID, attemptCtx.Request, request.Model, requiredChatCapabilities(request, false)...)
			mirrored = true
		}
		response, retries, err := r.callChatForAPI(ctx, endpoint, attemptCtx.Request, req.Metadata["gateway.api_type"])
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			response.ProviderEndpoint = endpoint.Name
			attemptCtx.Metadata["provider.cache.status"] = "miss"
			var cachePayload []byte
			if !chatResponseHasNativeContent(response) {
				cachePayload, _ = json.Marshal(response)
			}
			mergeChatUsage(&response, attemptCtx.Usage)
			attemptCtx.Response = &response
			modules.DeanonymizeResponse(&attemptCtx, &response)
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.ChatCompletionResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			if len(cachePayload) > 0 {
				if cacheErr := r.cacheSet(ctx, cacheKey, cachePayload); cacheErr != nil {
					attemptCtx.Metadata["provider.cache.status"] = "error"
					log.Printf("provider cache set failed: %v", cacheErr)
				}
				if semanticScope != "" && len(semanticVector) > 0 {
					stored := r.semantic.set(semanticScope, semanticVector, cachePayload)
					if r.observer != nil {
						result := "skipped"
						if stored {
							result = "ok"
						}
						r.observer.ObserveCache("semantic_set", result)
					}
				}
			}
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.ChatCompletionResponse{}, joined
		}
		fallbackCount++
	}

	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.ChatCompletionResponse{}, joined
}

func (r Router) StreamChatCompletions(ctx context.Context, req modules.RequestContext, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	request := req.Request
	request.Stream = true
	candidates := r.routeCandidates(ctx, req, request, requiredChatCapabilities(request, true)...)
	var err error
	candidates, err = bindChatAudioHistory(request, candidates)
	if err != nil {
		return openai.ChatCompletionResponse{}, false, err
	}
	if len(candidates) == 0 {
		// No native stream is available. The handler's normal Chat path still
		// enforces all non-stream capabilities and can synthesize SSE on success.
		return openai.ChatCompletionResponse{}, false, nil
	}
	if outputDLPRequired(req, candidates) {
		// Output policies need the complete response before any bytes are sent.
		// The handler will use the regular path and synthesize SSE after scanning.
		return openai.ChatCompletionResponse{}, false, nil
	}
	if req.Metadata["gateway.api_type"] == "messages" && (request.WebSearchOptions != nil || request.WebFetchOptions != nil || request.AnthropicToolSearch != "") {
		return openai.ChatCompletionResponse{}, false, nil
	}

	var errs []error
	var lastAttempt *modules.RequestContext
	mirrored := false
	totalRetries := 0
	fallbackCount := 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		var streamCall func(context.Context, openai.ChatCompletionRequest, ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error)
		if req.Metadata["gateway.api_type"] == "messages" {
			if messagesProvider, ok := endpoint.Provider.(StreamingMessagesClient); ok {
				streamCall = messagesProvider.StreamMessages
			}
		}
		if streamCall == nil {
			if streamingProvider, ok := endpoint.Provider.(StreamingClient); ok {
				streamCall = streamingProvider.StreamChatCompletions
			}
		}
		if streamCall == nil {
			continue
		}
		progress.enter(endpoint)

		if err := validateChatAdapter(endpoint.Provider, request); err != nil {
			return openai.ChatCompletionResponse{}, false, err
		}
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		attemptCtx.Request.Stream = true
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ChatCompletionResponse{}, false, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			wrapped := fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			errs = append(errs, wrapped)
			progress.fail(err)
			continue
		}
		if err := audioAnonymizationError(request, attemptCtx); err != nil {
			r.modules.RunFailure(ctx, &attemptCtx, err)
			return openai.ChatCompletionResponse{}, false, err
		}
		lastAttempt = &attemptCtx
		if !mirrored {
			r.mirrorChat(ctx, req.RequestID, attemptCtx.Request, request.Model, requiredChatCapabilities(request, true)...)
			mirrored = true
		}

		started := time.Now()
		release, err := r.acquireEndpoint(ctx, endpoint, openai.ChatReserveTokens(attemptCtx.Request))
		if err != nil {
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			errs = append(errs, fmt.Errorf("%s/%s admission failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}
		if err := r.health.permit(ctx, endpoint); err != nil {
			release()
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			errs = append(errs, fmt.Errorf("%s/%s circuit denied call: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}
		var response openai.ChatCompletionResponse
		streamStarted := false
		firstTokenLatency := time.Duration(0)
		for retry := 0; ; retry++ {
			tracker := newStreamAttemptTracker(started)
			providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "chat.stream")
			response, err = streamCall(providerCtx, attemptCtx.Request, deanonymizingChatStreamWriter(attemptCtx.AnonymizationValues, tracker.chatWriter(write)))
			finishProviderCall(err)
			streamStarted, firstTokenLatency = tracker.state()
			if err == nil || errors.Is(err, ErrStreamingUnsupported) || streamStarted || ctx.Err() != nil || retry >= endpointRetryLimit(endpoint, err) || !retrySameEndpointWithPolicy(endpoint, err) {
				break
			}
			if waitErr := r.retry.beforeRetry(ctx, err, retry); waitErr != nil {
				err = waitErr
				break
			}
			totalRetries++
		}
		release()
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if streamStarted {
			setFirstTokenLatency(&attemptCtx, firstTokenLatency)
		}
		if errors.Is(err, ErrStreamingUnsupported) {
			r.health.success(ctx, endpoint)
			progress.fail(err)
			fallbackCount++
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				r.health.failure(ctx, endpoint, err)
			}
			wrapped := fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err)
			progress.fail(err)
			if streamStarted || ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
				r.modules.RunFailure(ctx, &attemptCtx, wrapped)
				return openai.ChatCompletionResponse{}, streamStarted, wrapped
			}
			errs = append(errs, wrapped)
			fallbackCount++
			continue
		}
		r.health.success(ctx, endpoint)

		mergeChatUsage(&response, attemptCtx.Usage)
		attemptCtx.Response = &response
		if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
			return openai.ChatCompletionResponse{}, true, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
		}
		modules.DeanonymizeResponse(&attemptCtx, &response)
		return response, true, nil
	}

	if len(errs) > 0 {
		joined := errors.Join(errs...)
		if lastAttempt != nil {
			r.modules.RunFailure(ctx, lastAttempt, joined)
		}
		return openai.ChatCompletionResponse{}, false, joined
	}
	return openai.ChatCompletionResponse{}, false, nil
}

func (r Router) Responses(ctx context.Context, req modules.RequestContext) (openai.ResponseResponse, error) {
	if req.ResponseRequest == nil {
		return openai.ResponseResponse{}, errors.New("missing response request")
	}
	if err := validateResponseOptions(*req.ResponseRequest); err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := r.validateResponseOwnership(req, *req.ResponseRequest); err != nil {
		return openai.ResponseResponse{}, err
	}
	if req.ResponseRequest.Background && interfaceIsNil(r.asyncJobs) {
		return openai.ResponseResponse{}, ErrBackgroundResponseStorageUnavailable
	}

	request := *req.ResponseRequest
	candidates, affinityErr := r.responseCandidates(ctx, req, request, requiredResponseCapabilities(request, false)...)
	if affinityErr != nil {
		return openai.ResponseResponse{}, affinityErr
	}
	if len(candidates) == 0 {
		if request.Background {
			return openai.ResponseResponse{}, ErrBackgroundResponsesUnsupported
		}
		return openai.ResponseResponse{}, fmt.Errorf("no provider endpoint for provider=%q model=%q", request.Provider, request.Model)
	}

	var errs []error
	var lastAttempt *modules.RequestContext
	mirrored := false
	totalRetries := 0
	fallbackCount := 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		progress.enter(endpoint)
		if err := validateResponseAdapter(endpoint.Provider, request); err != nil {
			return openai.ResponseResponse{}, err
		}
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ResponseResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			wrapped := fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			errs = append(errs, wrapped)
			progress.fail(err)
			continue
		}
		if err := r.validateResponseOwnership(attemptCtx, *attemptCtx.ResponseRequest); err != nil {
			return openai.ResponseResponse{}, err
		}

		started := time.Now()
		lastAttempt = &attemptCtx
		cacheKey := ""
		if !persistentResponseRequested(*attemptCtx.ResponseRequest) {
			cacheKey = providerCacheKey("responses", attemptCtx)
		}
		if payload, found, cacheErr := r.cacheGet(ctx, cacheKey); found {
			if response, ok := decodeCached[openai.ResponseResponse](payload); ok && cacheableResponsesResult(response) {
				attemptCtx.Metadata["provider.cache.status"] = "hit"
				attemptCtx.Metadata["provider.cache.kind"] = "exact"
				attemptCtx.Metadata["provider.status"] = "ok"
				attemptCtx.Metadata["provider.latency_ms"] = "0"
				setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
				response.Usage = openai.ResponseUsage{}
				attemptCtx.ResponsesResponse = &response
				r.rememberResponseAffinity(ctx, attemptCtx, response.ID, endpoint.Name)
				modules.DeanonymizeResponsesResponse(&attemptCtx, &response)
				if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
					return openai.ResponseResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
				}
				return response, nil
			}
		} else if cacheErr != nil {
			attemptCtx.Metadata["provider.cache.status"] = "error"
			log.Printf("provider cache get failed: %v", cacheErr)
		}
		if !mirrored && !persistentResponseRequested(*attemptCtx.ResponseRequest) {
			r.mirrorResponses(ctx, req.RequestID, *attemptCtx.ResponseRequest, request.Model, requiredResponseCapabilities(request, false)...)
			mirrored = true
		}
		response, retries, err := r.callResponses(ctx, endpoint, *attemptCtx.ResponseRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			if err := mergeResponseUsage(&response, attemptCtx.Usage); err != nil {
				r.modules.RunFailure(ctx, &attemptCtx, err)
				return openai.ResponseResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			attemptCtx.Metadata["provider.cache.status"] = "miss"
			cachePayload, _ := json.Marshal(response)
			attemptCtx.ResponsesResponse = &response
			if attemptCtx.ResponseRequest.Background && backgroundResponsePending(response) {
				if err := r.persistResponseOwnership(ctx, attemptCtx, *attemptCtx.ResponseRequest, request.Model, response.ID, endpoint); err != nil {
					r.compensateBackgroundResponse(ctx, attemptCtx, response.ID, request.Model, endpoint)
					r.modules.RunFailure(ctx, &attemptCtx, err)
					return openai.ResponseResponse{}, err
				}
				if err := r.enqueueBackgroundResponse(ctx, attemptCtx, response, endpoint); err != nil {
					r.compensateBackgroundResponse(ctx, attemptCtx, response.ID, request.Model, endpoint)
					r.modules.RunFailure(ctx, &attemptCtx, err)
					return openai.ResponseResponse{}, err
				}
				return response, nil
			}
			modules.DeanonymizeResponsesResponse(&attemptCtx, &response)
			r.rememberResponseAffinity(ctx, attemptCtx, response.ID, endpoint.Name)
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.ResponseResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			if len(cachePayload) > 0 && cacheableResponsesResult(response) {
				if cacheErr := r.cacheSet(ctx, cacheKey, cachePayload); cacheErr != nil {
					attemptCtx.Metadata["provider.cache.status"] = "error"
					log.Printf("provider cache set failed: %v", cacheErr)
				}
			}
			if err := r.persistResponseOwnership(ctx, attemptCtx, *attemptCtx.ResponseRequest, request.Model, response.ID, endpoint); err != nil {
				return openai.ResponseResponse{}, err
			}
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.ResponseResponse{}, joined
		}
		fallbackCount++
	}

	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.ResponseResponse{}, joined
}

func (r Router) Embeddings(ctx context.Context, req modules.RequestContext) (openai.EmbeddingResponse, error) {
	if req.EmbeddingRequest == nil {
		return openai.EmbeddingResponse{}, errors.New("missing embedding request")
	}
	request := *req.EmbeddingRequest
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "embeddings")
	if len(candidates) == 0 {
		return openai.EmbeddingResponse{}, fmt.Errorf("no embedding endpoint for provider=%q model=%q", request.Provider, request.Model)
	}

	var errs []error
	var lastAttempt *modules.RequestContext
	mirrored := false
	totalRetries := 0
	fallbackCount := 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		client, ok := endpoint.Provider.(EmbeddingClient)
		if !ok {
			continue
		}
		progress.enter(endpoint)
		if err := validateEmbeddingAdapter(endpoint.Provider, request); err != nil {
			return openai.EmbeddingResponse{}, err
		}
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		input, _ := openai.InspectEmbeddingInput(request.Input)
		if input.Tokenized() && attemptCtx.Metadata["provider.modules.dlp.enabled"] == "true" {
			return openai.EmbeddingResponse{}, rejectParameters(endpoint.Type, parameterCheck{"input", true})
		}
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.EmbeddingResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			wrapped := fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			errs = append(errs, wrapped)
			progress.fail(err)
			continue
		}

		started := time.Now()
		lastAttempt = &attemptCtx
		if !mirrored {
			r.mirrorEmbeddings(ctx, req.RequestID, *attemptCtx.EmbeddingRequest, request.Model)
			mirrored = true
		}
		response, retries, err := r.callEmbeddings(ctx, endpoint, client, *attemptCtx.EmbeddingRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			mergeEmbeddingUsage(&response, attemptCtx.Usage)
			attemptCtx.EmbeddingResponse = &response
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.EmbeddingResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.EmbeddingResponse{}, joined
		}
		fallbackCount++
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements embeddings"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.EmbeddingResponse{}, joined
}

func (r Router) Rerank(ctx context.Context, req modules.RequestContext) (openai.RerankResponse, error) {
	if req.RerankRequest == nil {
		return openai.RerankResponse{}, errors.New("missing rerank request")
	}
	request := *req.RerankRequest
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "rerank")
	if len(candidates) == 0 {
		return openai.RerankResponse{}, fmt.Errorf("no rerank endpoint for provider=%q model=%q", request.Provider, request.Model)
	}
	var errs []error
	var lastAttempt *modules.RequestContext
	mirrored := false
	totalRetries := 0
	fallbackCount := 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		client, ok := endpoint.Provider.(RerankClient)
		if !ok {
			continue
		}
		progress.enter(endpoint)
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.RerankResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			wrapped := fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			errs = append(errs, wrapped)
			progress.fail(err)
			continue
		}
		started := time.Now()
		lastAttempt = &attemptCtx
		if !mirrored {
			r.mirrorRerank(ctx, req.RequestID, *attemptCtx.RerankRequest, request.Model)
			mirrored = true
		}
		response, retries, err := r.callRerank(ctx, endpoint, client, *attemptCtx.RerankRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			if validationErr := validateRerankResponse(response, len(request.Documents)); validationErr != nil {
				err = validationErr
			} else {
				if request.ReturnDocuments != nil && *request.ReturnDocuments {
					for index := range response.Results {
						response.Results[index].Document = request.Documents[response.Results[index].Index]
					}
				}
				attemptCtx.RerankResponse = &response
				if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
					return openai.RerankResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
				}
				return response, nil
			}
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.RerankResponse{}, joined
		}
		fallbackCount++
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements rerank"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.RerankResponse{}, joined
}

func (r Router) Moderations(ctx context.Context, req modules.RequestContext) (openai.ModerationResponse, error) {
	if req.ModerationRequest == nil {
		return openai.ModerationResponse{}, errors.New("missing moderation request")
	}
	request := *req.ModerationRequest
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "moderation")
	if len(candidates) == 0 {
		return openai.ModerationResponse{}, fmt.Errorf("no moderation endpoint for provider=%q model=%q", request.Provider, request.Model)
	}
	_, err := openai.InspectModerationInput(request.Input)
	if err != nil {
		return openai.ModerationResponse{}, err
	}
	var errs []error
	var lastAttempt *modules.RequestContext
	mirrored := false
	totalRetries, fallbackCount := 0, 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		client, ok := endpoint.Provider.(ModerationClient)
		if !ok {
			continue
		}
		progress.enter(endpoint)
		if err := validateModerationAdapter(client, request); err != nil {
			return openai.ModerationResponse{}, err
		}
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ModerationResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			wrapped := fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			errs = append(errs, wrapped)
			progress.fail(err)
			continue
		}
		if attemptCtx.ModerationRequest == nil {
			return openai.ModerationResponse{}, fmt.Errorf("%s/%s modules removed moderation request", endpoint.Type, endpoint.Name)
		}
		attemptInfo, err := openai.InspectModerationInput(attemptCtx.ModerationRequest.Input)
		if err != nil {
			return openai.ModerationResponse{}, fmt.Errorf("%s/%s modules returned invalid moderation input: %w", endpoint.Type, endpoint.Name, err)
		}
		started := time.Now()
		lastAttempt = &attemptCtx
		if !mirrored {
			r.mirrorModerations(ctx, req.RequestID, *attemptCtx.ModerationRequest, request.Model)
			mirrored = true
		}
		response, retries, err := r.callModerations(ctx, endpoint, client, *attemptCtx.ModerationRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			if validationErr := validateModerationResponse(response, attemptInfo.ResultCount); validationErr != nil {
				err = validationErr
			} else {
				attemptCtx.ModerationResponse = &response
				if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
					return openai.ModerationResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
				}
				return response, nil
			}
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.ModerationResponse{}, joined
		}
		fallbackCount++
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements moderations"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.ModerationResponse{}, joined
}

func (r Router) GenerateImage(ctx context.Context, req modules.RequestContext) (openai.ImageGenerationResponse, error) {
	if req.ImageGenerationRequest == nil {
		return openai.ImageGenerationResponse{}, errors.New("missing image generation request")
	}
	request := *req.ImageGenerationRequest
	if message := request.Validate(); message != "" {
		return openai.ImageGenerationResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if request.Stream {
		return openai.ImageGenerationResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New("streaming image generation requires StreamGenerateImage")}
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "image_generation")
	if len(candidates) == 0 {
		return openai.ImageGenerationResponse{}, fmt.Errorf("no image generation endpoint for provider=%q model=%q", request.Provider, request.Model)
	}
	var errs []error
	var lastAttempt *modules.RequestContext
	totalRetries, fallbackCount := 0, 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		client, ok := endpoint.Provider.(ImageGenerationClient)
		if !ok {
			continue
		}
		progress.enter(endpoint)
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ImageGenerationResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.ImageGenerationRequest == nil {
			return openai.ImageGenerationResponse{}, fmt.Errorf("%s/%s modules removed image generation request", endpoint.Type, endpoint.Name)
		}
		started := time.Now()
		lastAttempt = &attemptCtx
		response, retries, err := r.callImageGeneration(ctx, endpoint, client, *attemptCtx.ImageGenerationRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			validationErr := validateImageGenerationResponse(response, *attemptCtx.ImageGenerationRequest)
			if unitClient, ok := client.(imageUnitUsageClient); ok && unitClient.UsesImageUnitUsage() {
				validationErr = validateImageGenerationUnitResponse(response, *attemptCtx.ImageGenerationRequest)
			}
			if validationErr != nil {
				err = validationErr
			} else {
				attemptCtx.ImageGenerationResponse = &response
				if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
					return openai.ImageGenerationResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
				}
				return response, nil
			}
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.ImageGenerationResponse{}, joined
		}
		fallbackCount++
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements image generation"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.ImageGenerationResponse{}, joined
}

func (r Router) StreamGenerateImage(ctx context.Context, req modules.RequestContext, write ImageGenerationStreamWriter) (openai.ImageGenerationResponse, bool, error) {
	if req.ImageGenerationRequest == nil {
		return openai.ImageGenerationResponse{}, true, errors.New("missing image generation request")
	}
	request := *req.ImageGenerationRequest
	request.Stream = true
	if message := request.Validate(); message != "" {
		return openai.ImageGenerationResponse{}, true, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "image_generation")
	if len(candidates) == 0 || outputDLPRequired(req, candidates) {
		return openai.ImageGenerationResponse{}, false, nil
	}
	var failures []error
	var lastAttempt *modules.RequestContext
	totalRetries, fallbackCount := 0, 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		failures = append(failures, progress.initialFailure)
		fallbackCount = 1
	}
	for index, endpoint := range candidates {
		client, ok := endpoint.Provider.(StreamingImageGenerationClient)
		if !ok || !progress.allows(endpoint) {
			continue
		}
		progress.enter(endpoint)
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			failures = append(failures, err)
			progress.fail(err)
			continue
		}
		attemptCtx.ImageGenerationRequest.Stream = true
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ImageGenerationResponse{}, false, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			failures = append(failures, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.ImageGenerationRequest == nil {
			return openai.ImageGenerationResponse{}, false, fmt.Errorf("%s/%s modules removed image generation request", endpoint.Type, endpoint.Name)
		}
		lastAttempt = &attemptCtx
		started := time.Now()
		release, err := r.acquireEndpoint(ctx, endpoint, openai.ImageGenerationReserveTokens(*attemptCtx.ImageGenerationRequest))
		if err != nil {
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			failures = append(failures, fmt.Errorf("%s/%s admission failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}
		if err := r.health.permit(ctx, endpoint); err != nil {
			release()
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			failures = append(failures, fmt.Errorf("%s/%s circuit denied call: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}
		var response openai.ImageGenerationResponse
		streamStarted := false
		firstTokenLatency := time.Duration(0)
		for retry := 0; ; retry++ {
			tracker := newStreamAttemptTracker(started)
			providerCtx, finish := r.startProviderCall(ctx, endpoint, "image_generation.stream")
			response, err = client.StreamGenerateImage(providerCtx, *attemptCtx.ImageGenerationRequest, tracker.imageWriter(write))
			if err == nil {
				err = validateImageGenerationResponse(response, *attemptCtx.ImageGenerationRequest)
			}
			finish(err)
			streamStarted, firstTokenLatency = tracker.state()
			if err == nil || errors.Is(err, ErrStreamingUnsupported) || streamStarted || ctx.Err() != nil || retry >= endpointRetryLimit(endpoint, err) || !retrySameEndpointWithPolicy(endpoint, err) {
				break
			}
			if waitErr := r.retry.beforeRetry(ctx, err, retry); waitErr != nil {
				err = waitErr
				break
			}
			totalRetries++
		}
		release()
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if streamStarted {
			setFirstTokenLatency(&attemptCtx, firstTokenLatency)
		}
		if errors.Is(err, ErrStreamingUnsupported) {
			r.health.success(ctx, endpoint)
			progress.fail(err)
			fallbackCount++
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				r.health.failure(ctx, endpoint, err)
			}
			wrapped := fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err)
			progress.fail(err)
			if streamStarted || ctx.Err() != nil || !progress.hasNext(candidates[index+1:]) {
				r.modules.RunFailure(ctx, &attemptCtx, wrapped)
				return openai.ImageGenerationResponse{}, streamStarted, wrapped
			}
			failures = append(failures, wrapped)
			fallbackCount++
			continue
		}
		r.health.success(ctx, endpoint)
		attemptCtx.ImageGenerationResponse = &response
		if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
			return openai.ImageGenerationResponse{}, true, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
		}
		return response, true, nil
	}
	if len(failures) > 0 {
		joined := errors.Join(failures...)
		if lastAttempt != nil {
			r.modules.RunFailure(ctx, lastAttempt, joined)
		}
		return openai.ImageGenerationResponse{}, false, joined
	}
	return openai.ImageGenerationResponse{}, false, nil
}

func (r Router) EditImage(ctx context.Context, req modules.RequestContext) (openai.ImageGenerationResponse, error) {
	if req.ImageEditRequest == nil {
		return openai.ImageGenerationResponse{}, errors.New("missing image edit request")
	}
	request := *req.ImageEditRequest
	if message := request.Validate(); message != "" {
		return openai.ImageGenerationResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if request.Stream {
		return openai.ImageGenerationResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New("streaming image edits require StreamEditImage")}
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "image_edit")
	if len(candidates) == 0 {
		return openai.ImageGenerationResponse{}, fmt.Errorf("no image edit endpoint for provider=%q model=%q", request.Provider, request.Model)
	}
	var errs []error
	var lastAttempt *modules.RequestContext
	totalRetries, fallbackCount := 0, 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		client, ok := endpoint.Provider.(ImageEditClient)
		if !ok {
			continue
		}
		progress.enter(endpoint)
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ImageGenerationResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.ImageEditRequest == nil {
			return openai.ImageGenerationResponse{}, fmt.Errorf("%s/%s modules removed image edit request", endpoint.Type, endpoint.Name)
		}
		started := time.Now()
		lastAttempt = &attemptCtx
		response, retries, err := r.callImageEdit(ctx, endpoint, client, *attemptCtx.ImageEditRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			if validationErr := validateImageGenerationResponse(response, attemptCtx.ImageEditRequest.GenerationRequest()); validationErr != nil {
				err = validationErr
			} else {
				attemptCtx.ImageGenerationResponse = &response
				if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
					return openai.ImageGenerationResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
				}
				return response, nil
			}
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.ImageGenerationResponse{}, joined
		}
		fallbackCount++
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements image edits"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.ImageGenerationResponse{}, joined
}

func (r Router) StreamEditImage(ctx context.Context, req modules.RequestContext, write ImageGenerationStreamWriter) (openai.ImageGenerationResponse, bool, error) {
	if req.ImageEditRequest == nil {
		return openai.ImageGenerationResponse{}, true, errors.New("missing image edit request")
	}
	request := *req.ImageEditRequest
	request.Stream = true
	if message := request.Validate(); message != "" {
		return openai.ImageGenerationResponse{}, true, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "image_edit")
	if len(candidates) == 0 || outputDLPRequired(req, candidates) {
		return openai.ImageGenerationResponse{}, false, nil
	}
	var failures []error
	var lastAttempt *modules.RequestContext
	totalRetries, fallbackCount := 0, 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		failures = append(failures, progress.initialFailure)
		fallbackCount = 1
	}
	for index, endpoint := range candidates {
		client, ok := endpoint.Provider.(StreamingImageEditClient)
		if !ok || !progress.allows(endpoint) {
			continue
		}
		progress.enter(endpoint)
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			failures = append(failures, err)
			progress.fail(err)
			continue
		}
		attemptCtx.ImageEditRequest.Stream = true
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ImageGenerationResponse{}, false, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			failures = append(failures, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.ImageEditRequest == nil {
			return openai.ImageGenerationResponse{}, false, fmt.Errorf("%s/%s modules removed image edit request", endpoint.Type, endpoint.Name)
		}
		lastAttempt = &attemptCtx
		started := time.Now()
		release, err := r.acquireEndpoint(ctx, endpoint, openai.ImageEditReserveTokens(*attemptCtx.ImageEditRequest))
		if err != nil {
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			failures = append(failures, fmt.Errorf("%s/%s admission failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}
		if err := r.health.permit(ctx, endpoint); err != nil {
			release()
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			failures = append(failures, fmt.Errorf("%s/%s circuit denied call: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}
		var response openai.ImageGenerationResponse
		streamStarted := false
		firstTokenLatency := time.Duration(0)
		for retry := 0; ; retry++ {
			tracker := newStreamAttemptTracker(started)
			providerCtx, finish := r.startProviderCall(ctx, endpoint, "image_edit.stream")
			response, err = client.StreamEditImage(providerCtx, *attemptCtx.ImageEditRequest, tracker.imageWriter(write))
			if err == nil {
				err = validateImageGenerationResponse(response, attemptCtx.ImageEditRequest.GenerationRequest())
			}
			finish(err)
			streamStarted, firstTokenLatency = tracker.state()
			if err == nil || errors.Is(err, ErrStreamingUnsupported) || streamStarted || ctx.Err() != nil || retry >= endpointRetryLimit(endpoint, err) || !retrySameEndpointWithPolicy(endpoint, err) {
				break
			}
			if waitErr := r.retry.beforeRetry(ctx, err, retry); waitErr != nil {
				err = waitErr
				break
			}
			totalRetries++
		}
		release()
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if streamStarted {
			setFirstTokenLatency(&attemptCtx, firstTokenLatency)
		}
		if errors.Is(err, ErrStreamingUnsupported) {
			r.health.success(ctx, endpoint)
			progress.fail(err)
			fallbackCount++
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				r.health.failure(ctx, endpoint, err)
			}
			wrapped := fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err)
			progress.fail(err)
			if streamStarted || ctx.Err() != nil || !progress.hasNext(candidates[index+1:]) {
				r.modules.RunFailure(ctx, &attemptCtx, wrapped)
				return openai.ImageGenerationResponse{}, streamStarted, wrapped
			}
			failures = append(failures, wrapped)
			fallbackCount++
			continue
		}
		r.health.success(ctx, endpoint)
		attemptCtx.ImageGenerationResponse = &response
		if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
			return openai.ImageGenerationResponse{}, true, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
		}
		return response, true, nil
	}
	if len(failures) > 0 {
		joined := errors.Join(failures...)
		if lastAttempt != nil {
			r.modules.RunFailure(ctx, lastAttempt, joined)
		}
		return openai.ImageGenerationResponse{}, false, joined
	}
	return openai.ImageGenerationResponse{}, false, nil
}

func (r Router) CreateImageVariation(ctx context.Context, req modules.RequestContext) (openai.ImageGenerationResponse, error) {
	if req.ImageVariationRequest == nil {
		return openai.ImageGenerationResponse{}, errors.New("missing image variation request")
	}
	request := *req.ImageVariationRequest
	if message := request.Validate(); message != "" {
		return openai.ImageGenerationResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "image_variation")
	if len(candidates) == 0 {
		return openai.ImageGenerationResponse{}, fmt.Errorf("no image variation endpoint for provider=%q model=%q", request.Provider, request.Model)
	}
	var errs []error
	var lastAttempt *modules.RequestContext
	totalRetries, fallbackCount := 0, 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		client, ok := endpoint.Provider.(ImageVariationClient)
		if !ok {
			continue
		}
		progress.enter(endpoint)
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ImageGenerationResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.ImageVariationRequest == nil {
			return openai.ImageGenerationResponse{}, fmt.Errorf("%s/%s modules removed image variation request", endpoint.Type, endpoint.Name)
		}
		started := time.Now()
		lastAttempt = &attemptCtx
		response, retries, err := r.callImageVariation(ctx, endpoint, client, *attemptCtx.ImageVariationRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			if validationErr := validateImageGenerationResponse(response, attemptCtx.ImageVariationRequest.GenerationRequest()); validationErr != nil {
				err = validationErr
			} else {
				attemptCtx.ImageGenerationResponse = &response
				if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
					return openai.ImageGenerationResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
				}
				return response, nil
			}
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.ImageGenerationResponse{}, joined
		}
		fallbackCount++
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements image variations"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.ImageGenerationResponse{}, joined
}

func validateRerankResponse(response openai.RerankResponse, documentCount int) error {
	if response.Meta != nil {
		if units := response.Meta.BilledUnits; units != nil && (units.SearchUnits != units.SearchUnits || units.SearchUnits < 0 || units.SearchUnits > 1.7976931348623157e308 || units.TotalTokens < 0) {
			return errors.New("provider returned invalid rerank billed units")
		}
		if tokens := response.Meta.Tokens; tokens != nil && (tokens.InputTokens < 0 || tokens.OutputTokens < 0) {
			return errors.New("provider returned invalid rerank token usage")
		}
	}
	seen := make(map[int]bool, len(response.Results))
	for _, result := range response.Results {
		if result.Index < 0 || result.Index >= documentCount || seen[result.Index] {
			return errors.New("provider returned invalid rerank result indices")
		}
		seen[result.Index] = true
		if result.RelevanceScore != result.RelevanceScore || result.RelevanceScore > 1.7976931348623157e308 || result.RelevanceScore < -1.7976931348623157e308 {
			return errors.New("provider returned invalid rerank relevance score")
		}
	}
	return nil
}

func (r Router) StreamResponses(ctx context.Context, req modules.RequestContext, write ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	if req.ResponseRequest == nil {
		return openai.ResponseResponse{}, false, errors.New("missing response request")
	}
	if err := validateResponseOptions(*req.ResponseRequest); err != nil {
		return openai.ResponseResponse{}, true, err
	}
	if err := r.validateResponseOwnership(req, *req.ResponseRequest); err != nil {
		return openai.ResponseResponse{}, true, err
	}

	request := *req.ResponseRequest
	request.Stream = true
	candidates, affinityErr := r.responseCandidates(ctx, req, request, requiredResponseCapabilities(request, true)...)
	if affinityErr != nil {
		if errors.Is(affinityErr, ErrResponseAffinityUnavailable) {
			return openai.ResponseResponse{}, true, affinityErr
		}
		// Let the handler retry the same pinned endpoint through the non-streaming
		// path. This preserves affinity for endpoints without native streaming.
		return openai.ResponseResponse{}, false, nil
	}
	if len(candidates) == 0 {
		return openai.ResponseResponse{}, false, nil
	}
	if outputDLPRequired(req, candidates) {
		return openai.ResponseResponse{}, false, nil
	}

	var errs []error
	var lastAttempt *modules.RequestContext
	mirrored := false
	totalRetries := 0
	fallbackCount := 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		streamingProvider, ok := endpoint.Provider.(StreamingResponseClient)
		if !ok {
			continue
		}
		progress.enter(endpoint)

		if err := validateResponseAdapter(endpoint.Provider, request); err != nil {
			return openai.ResponseResponse{}, false, err
		}
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		attemptCtx.ResponseRequest.Stream = true
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ResponseResponse{}, false, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			wrapped := fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			errs = append(errs, wrapped)
			progress.fail(err)
			continue
		}
		if err := r.validateResponseOwnership(attemptCtx, *attemptCtx.ResponseRequest); err != nil {
			return openai.ResponseResponse{}, true, err
		}
		lastAttempt = &attemptCtx
		if !mirrored && !persistentResponseRequested(*attemptCtx.ResponseRequest) {
			r.mirrorResponses(ctx, req.RequestID, *attemptCtx.ResponseRequest, request.Model, requiredResponseCapabilities(request, true)...)
			mirrored = true
		}

		started := time.Now()
		release, err := r.acquireEndpoint(ctx, endpoint, openai.ResponseReserveTokens(*attemptCtx.ResponseRequest))
		if err != nil {
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			errs = append(errs, fmt.Errorf("%s/%s admission failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}
		if err := r.health.permit(ctx, endpoint); err != nil {
			release()
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			errs = append(errs, fmt.Errorf("%s/%s circuit denied call: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}
		var response openai.ResponseResponse
		streamStarted := false
		firstTokenLatency := time.Duration(0)
		for retry := 0; ; retry++ {
			tracker := newStreamAttemptTracker(started)
			providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "responses.stream")
			response, err = streamingProvider.StreamResponses(providerCtx, *attemptCtx.ResponseRequest, deanonymizingResponseStreamWriter(attemptCtx.AnonymizationValues, tracker.responseWriter(write)))
			finishProviderCall(err)
			streamStarted, firstTokenLatency = tracker.state()
			if err == nil || errors.Is(err, ErrStreamingUnsupported) || streamStarted || ctx.Err() != nil || retry >= endpointRetryLimit(endpoint, err) || !retrySameEndpointWithPolicy(endpoint, err) {
				break
			}
			if waitErr := r.retry.beforeRetry(ctx, err, retry); waitErr != nil {
				err = waitErr
				break
			}
			totalRetries++
		}
		release()
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if streamStarted {
			setFirstTokenLatency(&attemptCtx, firstTokenLatency)
		}
		if errors.Is(err, ErrStreamingUnsupported) {
			r.health.success(ctx, endpoint)
			progress.fail(err)
			fallbackCount++
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				r.health.failure(ctx, endpoint, err)
			}
			wrapped := fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err)
			progress.fail(err)
			if streamStarted || ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
				r.modules.RunFailure(ctx, &attemptCtx, wrapped)
				return openai.ResponseResponse{}, streamStarted, wrapped
			}
			errs = append(errs, wrapped)
			fallbackCount++
			continue
		}
		r.health.success(ctx, endpoint)

		if err := mergeResponseUsage(&response, attemptCtx.Usage); err != nil {
			r.modules.RunFailure(ctx, &attemptCtx, err)
			return openai.ResponseResponse{}, true, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
		}
		attemptCtx.ResponsesResponse = &response
		modules.DeanonymizeResponsesResponse(&attemptCtx, &response)
		r.rememberResponseAffinity(ctx, attemptCtx, response.ID, endpoint.Name)
		if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
			return openai.ResponseResponse{}, true, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
		}
		if err := r.persistResponseOwnership(ctx, attemptCtx, *attemptCtx.ResponseRequest, request.Model, response.ID, endpoint); err != nil {
			return openai.ResponseResponse{}, true, err
		}
		return response, true, nil
	}

	if len(errs) > 0 {
		joined := errors.Join(errs...)
		if lastAttempt != nil {
			r.modules.RunFailure(ctx, lastAttempt, joined)
		}
		return openai.ResponseResponse{}, false, joined
	}
	return openai.ResponseResponse{}, false, nil
}

func terminalModuleError(err error) bool {
	return errors.Is(err, modules.ErrContentRejected) || errors.Is(err, modules.ErrGuardrailUnavailable) || errors.Is(err, modules.ErrBudgetExceeded) || errors.Is(err, modules.ErrBillingConflict)
}

func (r Router) Models() []openai.Model {
	catalog := r.catalog.Current(context.Background())
	seen := map[string]bool{}
	models := make([]openai.Model, 0)
	for _, endpoint := range r.runtimeEndpoints() {
		if endpoint.Shadow {
			continue
		}
		modelIDs := append([]string(nil), endpoint.Models...)
		for alias := range endpoint.ModelAliases {
			modelIDs = append(modelIDs, alias)
		}
		if len(modelIDs) == 0 {
			modelIDs = []string{endpoint.Name}
		}
		for _, modelID := range modelIDs {
			if modelID == "" || seen[modelID] {
				continue
			}
			lookupModels := []string{modelID}
			if upstream, found := endpoint.ModelAliases[modelID]; found {
				lookupModels = append(lookupModels, upstream)
			}
			if _, found := findEndpointCatalogEntry(catalog, endpoint, lookupModels...); !found && catalog.DenyUnknownModels() {
				continue
			}
			seen[modelID] = true
			owner := endpoint.ProviderID
			if owner == "" {
				owner = endpoint.Name
			}
			models = append(models, openai.Model{
				ID:      modelID,
				Object:  "model",
				OwnedBy: owner,
			})
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	return models
}

func providerAttemptContext(req modules.RequestContext, endpoint Endpoint) modules.RequestContext {
	attemptCtx := req
	attemptCtx.Request = req.Request
	attemptCtx.Request.Messages = append([]openai.Message(nil), req.Request.Messages...)
	for index := range attemptCtx.Request.Messages {
		attemptCtx.Request.Messages[index].Reasoning = append([]openai.ReasoningBlock(nil), req.Request.Messages[index].Reasoning...)
	}
	if req.CompletionRequest != nil {
		completionRequest := *req.CompletionRequest
		attemptCtx.CompletionRequest = &completionRequest
	}
	if req.ResponseRequest != nil {
		responseRequest := *req.ResponseRequest
		attemptCtx.ResponseRequest = &responseRequest
	}
	if req.EmbeddingRequest != nil {
		embeddingRequest := *req.EmbeddingRequest
		attemptCtx.EmbeddingRequest = &embeddingRequest
	}
	if req.RerankRequest != nil {
		rerankRequest, ok := cloneMirrorRequest(*req.RerankRequest)
		if ok {
			attemptCtx.RerankRequest = &rerankRequest
		}
	}
	if req.ModerationRequest != nil {
		moderationRequest, ok := cloneMirrorRequest(*req.ModerationRequest)
		if ok {
			attemptCtx.ModerationRequest = &moderationRequest
		}
	}
	if req.ImageGenerationRequest != nil {
		imageRequest := *req.ImageGenerationRequest
		attemptCtx.ImageGenerationRequest = &imageRequest
	}
	if req.ImageEditRequest != nil {
		imageEditRequest := *req.ImageEditRequest
		imageEditRequest.Images = append([]openai.ImageAttachment(nil), req.ImageEditRequest.Images...)
		if req.ImageEditRequest.Mask != nil {
			mask := *req.ImageEditRequest.Mask
			imageEditRequest.Mask = &mask
		}
		attemptCtx.ImageEditRequest = &imageEditRequest
	}
	if req.ImageVariationRequest != nil {
		imageVariationRequest := *req.ImageVariationRequest
		attemptCtx.ImageVariationRequest = &imageVariationRequest
	}
	if req.AudioTranscriptionRequest != nil {
		audioRequest := *req.AudioTranscriptionRequest
		audioRequest.TimestampGranularities = append([]string(nil), req.AudioTranscriptionRequest.TimestampGranularities...)
		audioRequest.Include = append([]string(nil), req.AudioTranscriptionRequest.Include...)
		audioRequest.Languages = append([]string(nil), req.AudioTranscriptionRequest.Languages...)
		audioRequest.Keywords = append([]string(nil), req.AudioTranscriptionRequest.Keywords...)
		audioRequest.KnownSpeakerNames = append([]string(nil), req.AudioTranscriptionRequest.KnownSpeakerNames...)
		audioRequest.KnownSpeakerReferences = append([]openai.AudioAttachment(nil), req.AudioTranscriptionRequest.KnownSpeakerReferences...)
		if req.AudioTranscriptionRequest.ChunkingStrategy != nil {
			strategy := *req.AudioTranscriptionRequest.ChunkingStrategy
			if strategy.PrefixPaddingMS != nil {
				value := *strategy.PrefixPaddingMS
				strategy.PrefixPaddingMS = &value
			}
			if strategy.SilenceDurationMS != nil {
				value := *strategy.SilenceDurationMS
				strategy.SilenceDurationMS = &value
			}
			if strategy.Threshold != nil {
				value := *strategy.Threshold
				strategy.Threshold = &value
			}
			audioRequest.ChunkingStrategy = &strategy
		}
		attemptCtx.AudioTranscriptionRequest = &audioRequest
	}
	if req.AudioSpeechRequest != nil {
		audioSpeechRequest := *req.AudioSpeechRequest
		attemptCtx.AudioSpeechRequest = &audioSpeechRequest
	}
	if req.SearchRequest != nil {
		searchRequest := *req.SearchRequest
		searchRequest.SearchDomainFilter = append([]string(nil), req.SearchRequest.SearchDomainFilter...)
		attemptCtx.SearchRequest = &searchRequest
	}
	if req.OCRRequest != nil {
		ocrRequest := *req.OCRRequest
		attemptCtx.OCRRequest = &ocrRequest
	}
	if req.SandboxRequest != nil {
		sandboxRequest := *req.SandboxRequest
		attemptCtx.SandboxRequest = &sandboxRequest
	}
	attemptCtx.Response = nil
	attemptCtx.CompletionResponse = nil
	attemptCtx.ResponsesResponse = nil
	attemptCtx.CompactedResponse = nil
	attemptCtx.EmbeddingResponse = nil
	attemptCtx.RerankResponse = nil
	attemptCtx.ModerationResponse = nil
	attemptCtx.ImageGenerationResponse = nil
	attemptCtx.AudioTranscriptionResponse = nil
	attemptCtx.AudioSpeechResponse = nil
	attemptCtx.SearchResponse = nil
	attemptCtx.OCRResponse = nil
	attemptCtx.Usage = nil
	attemptCtx.AnonymizationValues = nil
	attemptCtx.Metadata = cloneMetadata(req.Metadata)
	for key, value := range providerMetadata(endpoint) {
		attemptCtx.Metadata[key] = value
	}
	endpointDLP, endpointOutputDLP, endpointAV, endpointAnonymization, endpointPolicies := endpointPolicySettings(attemptCtx.Metadata, endpoint, attemptCtx.Request.Model)
	anonymizationSettings := []AnonymizationSetting{
		AnonymizationSetting{Profile: endpoint.GuardrailPolicy, Mode: endpoint.Anonymization, Rules: endpoint.AnonymizationRules},
		AnonymizationSetting{Profile: attemptCtx.Metadata["policy.modules.anonymizer.profiles"], Mode: attemptCtx.Metadata["policy.modules.anonymizer.mode"], Rules: splitMetadataList(attemptCtx.Metadata["policy.modules.anonymizer.rules"])},
	}
	anonymizationSettings = append(anonymizationSettings, endpointAnonymization...)
	mode, rules, profiles := ResolveAnonymization(anonymizationSettings...)
	attemptCtx.Metadata["provider.modules.anonymizer.mode"] = mode
	attemptCtx.Metadata["provider.modules.anonymizer.rules"] = strings.Join(rules, ",")
	attemptCtx.Metadata["provider.modules.anonymizer.profiles"] = strings.Join(profiles, ",")
	if attemptCtx.Metadata["policy.modules.dlp.enabled"] == "true" {
		attemptCtx.Metadata["provider.modules.dlp.enabled"] = "true"
	}
	if endpointDLP {
		attemptCtx.Metadata["provider.modules.dlp.enabled"] = "true"
	}
	if attemptCtx.Metadata["policy.modules.dlp.output_enabled"] == "true" {
		attemptCtx.Metadata["provider.modules.dlp.output_enabled"] = "true"
	}
	if endpointOutputDLP {
		attemptCtx.Metadata["provider.modules.dlp.output_enabled"] = "true"
	}
	if attemptCtx.Metadata["policy.modules.av.enabled"] == "true" {
		attemptCtx.Metadata["provider.modules.av.enabled"] = "true"
	}
	if endpointAV {
		attemptCtx.Metadata["provider.modules.av.enabled"] = "true"
	}
	if names := attemptCtx.Metadata["policy.guardrail.names"]; names != "" {
		attemptCtx.Metadata["provider.guardrail.attached_policies"] = names
		attemptCtx.Metadata["provider.guardrail.policy"] = combinePolicyNames(attemptCtx.Metadata["provider.guardrail.policy"], names)
	}
	if len(endpointPolicies) != 0 {
		names := strings.Join(endpointPolicies, ",")
		attemptCtx.Metadata["provider.guardrail.attached_policies"] = combinePolicyNames(attemptCtx.Metadata["provider.guardrail.attached_policies"], names)
		attemptCtx.Metadata["provider.guardrail.policy"] = combinePolicyNames(attemptCtx.Metadata["provider.guardrail.policy"], names)
	}
	originalModel := attemptCtx.Request.Model
	routingModel := endpointRoutingModel(endpoint, originalModel)
	if routingModel != originalModel {
		attemptCtx.Request.Model = routingModel
		if attemptCtx.ResponseRequest != nil {
			attemptCtx.ResponseRequest.Model = routingModel
		}
		if attemptCtx.CompletionRequest != nil {
			attemptCtx.CompletionRequest.Model = routingModel
		}
		if attemptCtx.EmbeddingRequest != nil {
			attemptCtx.EmbeddingRequest.Model = routingModel
		}
		if attemptCtx.RerankRequest != nil {
			attemptCtx.RerankRequest.Model = routingModel
		}
		if attemptCtx.ModerationRequest != nil {
			attemptCtx.ModerationRequest.Model = routingModel
		}
		if attemptCtx.ImageGenerationRequest != nil {
			attemptCtx.ImageGenerationRequest.Model = routingModel
		}
		if attemptCtx.ImageEditRequest != nil {
			attemptCtx.ImageEditRequest.Model = routingModel
		}
		if attemptCtx.ImageVariationRequest != nil {
			attemptCtx.ImageVariationRequest.Model = routingModel
		}
		if attemptCtx.AudioTranscriptionRequest != nil {
			attemptCtx.AudioTranscriptionRequest.Model = routingModel
		}
		if attemptCtx.AudioSpeechRequest != nil {
			attemptCtx.AudioSpeechRequest.Model = routingModel
		}
		if attemptCtx.SearchRequest != nil {
			attemptCtx.SearchRequest.SetRoutingModel(routingModel)
		}
		if attemptCtx.OCRRequest != nil {
			attemptCtx.OCRRequest.Model = routingModel
		}
		if attemptCtx.SandboxRequest != nil {
			attemptCtx.SandboxRequest.Model = routingModel
		}
		attemptCtx.Metadata["provider.original_model"] = originalModel
		attemptCtx.Metadata["provider.routed_model"] = routingModel
		attemptCtx.Metadata["provider.fallback_type"] = endpoint.FallbackType
	}
	requestedModel := attemptCtx.Request.Model
	if upstreamModel, found := endpoint.ModelAliases[requestedModel]; found {
		attemptCtx.Request.Model = upstreamModel
		if attemptCtx.ResponseRequest != nil {
			attemptCtx.ResponseRequest.Model = upstreamModel
		}
		if attemptCtx.CompletionRequest != nil {
			attemptCtx.CompletionRequest.Model = upstreamModel
		}
		if attemptCtx.EmbeddingRequest != nil {
			attemptCtx.EmbeddingRequest.Model = upstreamModel
		}
		if attemptCtx.RerankRequest != nil {
			attemptCtx.RerankRequest.Model = upstreamModel
		}
		if attemptCtx.ModerationRequest != nil {
			attemptCtx.ModerationRequest.Model = upstreamModel
		}
		if attemptCtx.ImageGenerationRequest != nil {
			attemptCtx.ImageGenerationRequest.Model = upstreamModel
		}
		if attemptCtx.ImageEditRequest != nil {
			attemptCtx.ImageEditRequest.Model = upstreamModel
		}
		if attemptCtx.ImageVariationRequest != nil {
			attemptCtx.ImageVariationRequest.Model = upstreamModel
		}
		if attemptCtx.AudioTranscriptionRequest != nil {
			attemptCtx.AudioTranscriptionRequest.Model = upstreamModel
		}
		if attemptCtx.AudioSpeechRequest != nil {
			attemptCtx.AudioSpeechRequest.Model = upstreamModel
		}
		if attemptCtx.SearchRequest != nil {
			attemptCtx.SearchRequest.SetRoutingModel(upstreamModel)
		}
		if attemptCtx.OCRRequest != nil {
			attemptCtx.OCRRequest.Model = upstreamModel
		}
		if attemptCtx.SandboxRequest != nil {
			attemptCtx.SandboxRequest.Model = upstreamModel
		}
		attemptCtx.Metadata["provider.requested_model"] = requestedModel
		attemptCtx.Metadata["provider.upstream_model"] = upstreamModel
	}
	return attemptCtx
}

func (r Router) applyCatalogPricing(ctx context.Context, req *modules.RequestContext, endpoint Endpoint, requestedModel string) {
	requestedModel = endpointRoutingModel(endpoint, requestedModel)
	models := []string{requestedModel}
	if upstream, found := endpoint.ModelAliases[requestedModel]; found {
		models = append(models, upstream)
	}
	catalog := r.catalog.Current(ctx)
	entry, found := findEndpointCatalogEntry(catalog, endpoint, models...)
	if !found || entry.Currency == "" {
		return
	}
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["model_catalog.version"] = catalog.Version
	req.Metadata["model_catalog.pricing_key"] = entry.Provider + "/" + entry.Model
	inputCost, outputCost := entry.InputCostPer1M, entry.OutputCostPer1M
	if req.Request.AnthropicInferenceGeo == "us" {
		inputCost *= 1.1
		outputCost *= 1.1
		req.Metadata["model_catalog.price_modifier"] = "inference_geo_us_1.1"
	}
	req.Metadata["model_catalog.input_cost_per_1m"] = strconv.FormatFloat(inputCost, 'g', -1, 64)
	req.Metadata["model_catalog.output_cost_per_1m"] = strconv.FormatFloat(outputCost, 'g', -1, 64)
	req.Metadata["model_catalog.training_cost_per_1m"] = strconv.FormatFloat(entry.TrainingCostPer1M, 'g', -1, 64)
	req.Metadata["model_catalog.search_cost_per_1k"] = strconv.FormatFloat(entry.SearchCostPer1K, 'g', -1, 64)
	req.Metadata["model_catalog.character_cost_per_1m"] = strconv.FormatFloat(entry.CharacterCostPer1M, 'g', -1, 64)
	req.Metadata["model_catalog.page_cost_per_1k"] = strconv.FormatFloat(entry.PageCostPer1K, 'g', -1, 64)
	req.Metadata["model_catalog.audio_cost_per_minute"] = strconv.FormatFloat(entry.AudioCostPerMinute, 'g', -1, 64)
	req.Metadata["model_catalog.video_cost_per_second"] = strconv.FormatFloat(entry.VideoCostPerSecond, 'g', -1, 64)
	req.Metadata["model_catalog.image_cost_per_unit"] = strconv.FormatFloat(entry.ImageCostPerUnit, 'g', -1, 64)
	req.Metadata["model_catalog.currency"] = entry.Currency
}

func cloneMetadata(metadata map[string]string) map[string]string {
	cloned := map[string]string{}
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func combinePolicyNames(values ...string) string {
	seen := map[string]struct{}{}
	names := make([]string, 0, len(values))
	for _, value := range values {
		for _, name := range strings.Split(value, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, found := seen[name]; found {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func splitMetadataList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func providerMetadata(endpoint Endpoint) map[string]string {
	return map[string]string{
		"provider.id":                         endpoint.ProviderID,
		"provider.endpoint.name":              endpoint.Name,
		"provider.endpoint.type":              endpoint.Type,
		"provider.modules.dlp.enabled":        boolString(endpoint.DLPEnabled),
		"provider.modules.dlp.output_enabled": boolString(endpoint.OutputDLPEnabled),
		"provider.modules.av.enabled":         boolString(endpoint.AVEnabled),
		"provider.guardrail.policy":           endpoint.GuardrailPolicy,
		"provider.guardrail.valid":            boolString(endpoint.GuardrailPolicy == "" || endpoint.GuardrailPolicyValid),
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func outputDLPRequired(req modules.RequestContext, endpoints []Endpoint) bool {
	if req.Metadata["policy.modules.dlp.output_enabled"] == "true" {
		return true
	}
	for _, endpoint := range endpoints {
		if endpoint.OutputDLPEnabled {
			return true
		}
	}
	return false
}

func chatResponseHasNativeContent(response openai.ChatCompletionResponse) bool {
	for _, choice := range response.Choices {
		if len(choice.Message.NativeContent) > 0 {
			return true
		}
	}
	return false
}

func (r Router) callChat(ctx context.Context, endpoint Endpoint, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, int, error) {
	return r.callChatForAPI(ctx, endpoint, request, "")
}

func (r Router) callChatForAPI(ctx context.Context, endpoint Endpoint, request openai.ChatCompletionRequest, apiType string) (openai.ChatCompletionResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.ChatReserveTokens(request))
	if err != nil {
		return openai.ChatCompletionResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.ChatCompletionResponse{}, 0, err
	}
	var response openai.ChatCompletionResponse
	err = nil
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "chat")
		if messagesProvider, ok := endpoint.Provider.(MessagesClient); apiType == "messages" && ok {
			response, err = messagesProvider.Messages(providerCtx, request)
		} else {
			response, err = endpoint.Provider.ChatCompletions(providerCtx, request)
		}
		finishProviderCall(err)
		if err == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, err) || !retrySameEndpointWithPolicy(endpoint, err) {
			r.health.failure(ctx, endpoint, err)
			return openai.ChatCompletionResponse{}, attempt, err
		}
		if waitErr := r.retry.beforeRetry(ctx, err, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.ChatCompletionResponse{}, attempt, waitErr
		}
	}
	return openai.ChatCompletionResponse{}, endpointMaxRetries(endpoint), err

}

func (r Router) callResponses(ctx context.Context, endpoint Endpoint, request openai.ResponseRequest) (openai.ResponseResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.ResponseReserveTokens(request))
	if err != nil {
		return openai.ResponseResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.ResponseResponse{}, 0, err
	}
	var response openai.ResponseResponse
	err = nil
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "responses")
		response, err = endpoint.Provider.Responses(providerCtx, request)
		finishProviderCall(err)
		if err == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, err) || !retrySameEndpointWithPolicy(endpoint, err) {
			r.health.failure(ctx, endpoint, err)
			return openai.ResponseResponse{}, attempt, err
		}
		if waitErr := r.retry.beforeRetry(ctx, err, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.ResponseResponse{}, attempt, waitErr
		}
	}
	return openai.ResponseResponse{}, endpointMaxRetries(endpoint), err
}

func (r Router) callEmbeddings(ctx context.Context, endpoint Endpoint, client EmbeddingClient, request openai.EmbeddingRequest) (openai.EmbeddingResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.EmbeddingInputTokenCount(request.Input))
	if err != nil {
		return openai.EmbeddingResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.EmbeddingResponse{}, 0, err
	}
	var response openai.EmbeddingResponse
	err = nil
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "embeddings")
		response, err = client.Embeddings(providerCtx, request)
		finishProviderCall(err)
		if err == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, err) || !retrySameEndpointWithPolicy(endpoint, err) {
			r.health.failure(ctx, endpoint, err)
			return openai.EmbeddingResponse{}, attempt, err
		}
		if waitErr := r.retry.beforeRetry(ctx, err, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.EmbeddingResponse{}, attempt, waitErr
		}
	}
	return openai.EmbeddingResponse{}, endpointMaxRetries(endpoint), err
}

func (r Router) callImageGeneration(ctx context.Context, endpoint Endpoint, client ImageGenerationClient, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.ImageGenerationReserveTokens(request))
	if err != nil {
		return openai.ImageGenerationResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.ImageGenerationResponse{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "image_generation")
		response, callErr := client.GenerateImage(providerCtx, request)
		finish(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.ImageGenerationResponse{}, attempt, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.ImageGenerationResponse{}, attempt, waitErr
		}
	}
	return openai.ImageGenerationResponse{}, endpointMaxRetries(endpoint), errors.New("image generation failed")
}

func (r Router) callImageEdit(ctx context.Context, endpoint Endpoint, client ImageEditClient, request openai.ImageEditRequest) (openai.ImageGenerationResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.ImageEditReserveTokens(request))
	if err != nil {
		return openai.ImageGenerationResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.ImageGenerationResponse{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "image_edit")
		response, callErr := client.EditImage(providerCtx, request)
		finish(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.ImageGenerationResponse{}, attempt, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.ImageGenerationResponse{}, attempt, waitErr
		}
	}
	return openai.ImageGenerationResponse{}, endpointMaxRetries(endpoint), errors.New("image edit failed")
}

func (r Router) callImageVariation(ctx context.Context, endpoint Endpoint, client ImageVariationClient, request openai.ImageVariationRequest) (openai.ImageGenerationResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.ImageVariationReserveTokens(request))
	if err != nil {
		return openai.ImageGenerationResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.ImageGenerationResponse{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "image_variation")
		response, callErr := client.CreateImageVariation(providerCtx, request)
		finish(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.ImageGenerationResponse{}, attempt, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.ImageGenerationResponse{}, attempt, waitErr
		}
	}
	return openai.ImageGenerationResponse{}, endpointMaxRetries(endpoint), errors.New("image variation failed")
}

func (r Router) callRerank(ctx context.Context, endpoint Endpoint, client RerankClient, request openai.RerankRequest) (openai.RerankResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.EstimateContextTokens(struct {
		Query     string
		Documents []any
	}{request.Query, request.Documents}))
	if err != nil {
		return openai.RerankResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.RerankResponse{}, 0, err
	}
	var response openai.RerankResponse
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "rerank")
		response, err = client.Rerank(providerCtx, request)
		finish(err)
		if err == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, err) || !retrySameEndpointWithPolicy(endpoint, err) {
			r.health.failure(ctx, endpoint, err)
			return openai.RerankResponse{}, attempt, err
		}
		if waitErr := r.retry.beforeRetry(ctx, err, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.RerankResponse{}, attempt, waitErr
		}
	}
	return openai.RerankResponse{}, endpointMaxRetries(endpoint), err
}

func (r Router) callModerations(ctx context.Context, endpoint Endpoint, client ModerationClient, request openai.ModerationRequest) (openai.ModerationResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.ModerationInputTokenCount(request.Input))
	if err != nil {
		return openai.ModerationResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.ModerationResponse{}, 0, err
	}
	var response openai.ModerationResponse
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "moderations")
		response, err = client.Moderations(providerCtx, request)
		finish(err)
		if err == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, err) || !retrySameEndpointWithPolicy(endpoint, err) {
			r.health.failure(ctx, endpoint, err)
			return openai.ModerationResponse{}, attempt, err
		}
		if waitErr := r.retry.beforeRetry(ctx, err, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.ModerationResponse{}, attempt, waitErr
		}
	}
	return openai.ModerationResponse{}, endpointMaxRetries(endpoint), err
}

type streamAttemptTracker struct {
	mu                sync.Mutex
	started           time.Time
	writeAttempted    bool
	firstTokenLatency time.Duration
}

func newStreamAttemptTracker(started time.Time) *streamAttemptTracker {
	return &streamAttemptTracker{started: started}
}

func (t *streamAttemptTracker) beforeWrite() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.writeAttempted {
		return
	}
	t.writeAttempted = true
	t.firstTokenLatency = time.Since(t.started)
}

func (t *streamAttemptTracker) state() (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.writeAttempted, t.firstTokenLatency
}

func (t *streamAttemptTracker) chatWriter(write ChatCompletionStreamWriter) ChatCompletionStreamWriter {
	return func(payload string) error {
		t.beforeWrite()
		return write(payload)
	}
}

func (t *streamAttemptTracker) completionWriter(write CompletionStreamWriter) CompletionStreamWriter {
	return func(payload string) error {
		t.beforeWrite()
		return write(payload)
	}
}

func (t *streamAttemptTracker) responseWriter(write ResponseStreamWriter) ResponseStreamWriter {
	return func(event, payload string) error {
		t.beforeWrite()
		return write(event, payload)
	}
}

func (t *streamAttemptTracker) imageWriter(write ImageGenerationStreamWriter) ImageGenerationStreamWriter {
	return func(payload string) error {
		t.beforeWrite()
		return write(payload)
	}
}

func (t *streamAttemptTracker) audioTranscriptionWriter(write AudioTranscriptionStreamWriter) AudioTranscriptionStreamWriter {
	return func(payload string) error {
		t.beforeWrite()
		return write(payload)
	}
}

func (t *streamAttemptTracker) audioSpeechWriter(write AudioSpeechStreamWriter) AudioSpeechStreamWriter {
	return func(payload string) error {
		t.beforeWrite()
		return write(payload)
	}
}

func setAttemptCounters(req *modules.RequestContext, retries, fallbacks int) {
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["provider.retry_count"] = strconv.Itoa(retries)
	req.Metadata["provider.fallback_count"] = strconv.Itoa(fallbacks)
}

func setFirstTokenLatency(req *modules.RequestContext, latency time.Duration) {
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["provider.first_token_latency_ms"] = strconv.FormatInt(latency.Milliseconds(), 10)
}

func (r Router) startProviderCall(ctx context.Context, endpoint Endpoint, operation string) (context.Context, func(error)) {
	started := time.Now()
	providerCtx := ctx
	cancel := func() {}
	if endpoint.RequestTimeout > 0 {
		providerCtx, cancel = context.WithTimeout(ctx, endpoint.RequestTimeout)
	}
	spanCtx, span := otel.Tracer("ai-gateway/provider").Start(providerCtx, "provider."+operation,
		trace.WithAttributes(
			attribute.String("ai.provider.endpoint", endpoint.Name),
			attribute.String("ai.provider.type", endpoint.Type),
			attribute.String("ai.operation", operation),
		))
	return spanCtx, func(err error) {
		defer cancel()
		duration := time.Since(started)
		result := "ok"
		if err != nil {
			result = string(failureClass(err))
			span.RecordError(err)
			span.SetStatus(codes.Error, result)
		}
		span.SetAttributes(attribute.String("ai.result", result))
		span.End()
		// Keep endpoint EWMAs for diagnostics under every strategy. Only the
		// adaptive strategy consumes them when ordering candidates.
		r.adaptive.observe(endpoint.Name, duration, err)
		if r.observer != nil {
			r.observer.ObserveProvider(endpoint.Name, endpoint.Type, operation, result, duration)
		}
	}
}

func setAttemptMetadata(req *modules.RequestContext, started time.Time, err error) {
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["provider.latency_ms"] = strconv.FormatInt(time.Since(started).Milliseconds(), 10)
	if err == nil {
		req.Metadata["provider.status"] = "ok"
		delete(req.Metadata, "provider.error")
		delete(req.Metadata, "provider.failure_class")
		return
	}
	req.Metadata["provider.status"] = "error"
	req.Metadata["provider.error"] = err.Error()
	req.Metadata["provider.failure_class"] = string(failureClass(err))
}

func mergeChatUsage(response *openai.ChatCompletionResponse, usage *openai.Usage) {
	if usage == nil || response.Usage.PromptTokens != 0 {
		return
	}
	response.Usage.PromptTokens = usage.PromptTokens
	response.Usage.TotalTokens += usage.PromptTokens
}

func mergeResponseUsage(response *openai.ResponseResponse, usage *openai.Usage) error {
	if err := validateResponseUsage(response.Usage); err != nil {
		return err
	}
	if usage == nil || response.InputTokensReported || response.Usage.InputTokens != 0 {
		return nil
	}
	prompt := usage.PromptTokens
	maxInt := int(^uint(0) >> 1)
	if prompt < 0 || prompt > maxInt-response.Usage.TotalTokens || prompt > maxInt-response.Usage.OutputTokens {
		return errors.New("invalid estimated Responses token usage")
	}
	response.Usage.InputTokens = prompt
	response.Usage.TotalTokens += prompt
	return nil
}

func mergeEmbeddingUsage(response *openai.EmbeddingResponse, usage *openai.Usage) {
	if usage == nil || response.UsageReported || response.Usage.PromptTokens != 0 {
		return
	}
	response.Usage.PromptTokens = usage.PromptTokens
	response.Usage.TotalTokens += usage.PromptTokens
}

func (r Router) candidates(ctx context.Context, request openai.ChatCompletionRequest, capabilities ...string) []Endpoint {
	return r.candidatesWithCounter(ctx, request, r.routeCounter, capabilities...)
}

func (r Router) candidatesWithCounter(ctx context.Context, request openai.ChatCompletionRequest, counter *atomic.Uint64, capabilities ...string) []Endpoint {
	catalog := r.catalog.Current(ctx)
	requestedProvider := strings.TrimSpace(request.Provider)
	filterByProvider := requestedProvider != ""
	if requestedProvider == "" && strings.TrimSpace(request.Model) == "" {
		requestedProvider = r.defaultProvider
		filterByProvider = requestedProvider != ""
	}

	var candidates []Endpoint
	group, grouped := r.modelGroup(request.Model)
	groupDeployments := map[string]bool{}
	if grouped {
		for _, id := range group.DeploymentIDs {
			groupDeployments[id] = true
		}
	}
	for _, endpoint := range r.runtimeEndpoints() {
		if endpoint.Shadow {
			continue
		}
		if grouped && !groupDeployments[endpoint.Name] {
			continue
		}
		if filterByProvider && requestedProvider != "auto" && requestedProvider != endpoint.Name && requestedProvider != endpoint.Type {
			continue
		}
		if !endpoint.supportsModel(request.Model) {
			continue
		}
		if !supportsCatalogCapabilities(catalog, endpoint, request.Model, capabilities...) {
			continue
		}
		if hasCapability(capabilities, "mcp") {
			mcpClient, ok := endpoint.Provider.(MCPClient)
			if !ok || !mcpClient.SupportsMCP() {
				continue
			}
		}
		if hasCapability(capabilities, "vision") {
			if !endpoint.AVEnabled {
				continue
			}
			visionClient, ok := endpoint.Provider.(VisionClient)
			if !ok || !visionClient.SupportsVision() {
				continue
			}
		}
		requestedOutputTokens := request.MaxTokens
		if requestedOutputTokens == nil {
			requestedOutputTokens = request.MaxCompletionTokens
		}
		if !supportsCatalogOutputLimit(catalog, endpoint, request.Model, requestedOutputTokens) {
			continue
		}
		if !r.health.available(ctx, endpoint) {
			continue
		}
		candidates = append(candidates, endpoint)
	}

	if grouped {
		groupRouter := r
		groupRouter.routingStrategy = group.Strategy
		groupRouter.routeCounter = counter
		ordered := groupRouter.weightedOrder(candidates)
		for index := range ordered {
			ordered[index].RetryPolicy = cloneRetryPolicy(group.RetryPolicy)
		}
		return ordered
	}
	r.routeCounter = counter
	return r.weightedOrder(candidates)
}

func (r Router) responseCandidates(ctx context.Context, req modules.RequestContext, request openai.ResponseRequest, capabilities ...string) ([]Endpoint, error) {
	chatRequest := openai.ChatCompletionRequest{
		Provider:  request.Provider,
		Model:     request.Model,
		MaxTokens: request.MaxOutputTokens,
	}
	if chatRequest.MaxTokens == nil {
		chatRequest.MaxTokens = request.MaxTokens
	}
	candidates := r.routeCandidates(ctx, req, chatRequest, capabilities...)
	if r.affinity == nil || request.PreviousResponse == "" {
		return candidates, nil
	}
	key := affinityKey(req, request.PreviousResponse)
	if key == "" {
		return candidates, nil
	}
	endpointName, found, err := r.affinity.get(ctx, key)
	if r.observer != nil {
		result := "miss"
		if err != nil {
			result = "error"
		} else if found {
			result = "hit"
		}
		r.observer.ObserveCache("affinity_get", result)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrResponseAffinityUnavailable, err)
	}
	if !found {
		return candidates, nil
	}
	for _, endpoint := range candidates {
		if endpoint.Name == endpointName {
			pinned := endpoint
			pinned.FallbackStage = 0
			// The previous response belongs to this endpoint. Model-group fallback
			// authorization does not establish shared provider-side session state.
			return []Endpoint{pinned}, nil
		}
	}
	return nil, fmt.Errorf("responses session endpoint %q is unavailable for previous_response_id", endpointName)
}

func (r Router) rememberResponseAffinity(ctx context.Context, req modules.RequestContext, responseID, endpoint string) {
	if r.affinity == nil || responseID == "" || endpoint == "" {
		return
	}
	key := affinityKey(req, responseID)
	if key == "" {
		return
	}
	err := r.affinity.set(ctx, key, endpoint)
	if r.observer != nil {
		result := "ok"
		if err != nil {
			result = "error"
		}
		r.observer.ObserveCache("affinity_set", result)
	}
	if err != nil {
		log.Print("responses affinity store failed")
	}
}

func requiredChatCapabilities(request openai.ChatCompletionRequest, stream bool) []string {
	required := []string{"chat"}
	if len(request.AnthropicSkills) > 0 {
		required = append(required, "skills")
	}
	if request.AnthropicThinking != nil {
		required = append(required, "thinking")
	}
	if request.AllowZeroMaxTokens {
		required = append(required, "zero_output")
	}
	if request.AnthropicInferenceGeo != "" {
		required = append(required, "inference_geo")
	}
	if len(request.AnthropicContextManagement) > 0 {
		required = append(required, "context_management")
	}
	toolResultError, documentCitations, documentMetadata := false, false, false
	for _, message := range request.Messages {
		if message.ToolResultError {
			toolResultError = true
		}
		if slices.Contains(message.AnthropicDocumentCitations, true) {
			documentCitations = true
		}
		if len(message.AnthropicDocumentMetadata) > 0 {
			documentMetadata = true
		}
	}
	if toolResultError {
		required = append(required, "tool_result_error")
	}
	if documentCitations {
		required = append(required, "document_citations")
	}
	if documentMetadata {
		required = append(required, "document_metadata")
	}
	if len(request.GeminiSafetySettings) > 0 {
		required = append(required, "gemini_safety_settings")
	}
	if request.BedrockInvoke {
		required = append(required, "bedrock_invoke")
	}
	if stream {
		required = append(required, "stream")
	}
	if len(request.Tools) > 0 || openai.ChatRequiresFunctionCapability(request) {
		required = append(required, "tools")
	}
	if request.AnthropicCodeExecution {
		required = append(required, "tools")
	}
	if request.AnthropicToolSearch != "" {
		required = append(required, "tool_search")
	}
	seenClientCapabilities := map[string]bool{}
	for _, tool := range request.AnthropicClientTools {
		capability := ""
		switch tool.Type {
		case "memory_20250818":
			capability = "memory_tool"
		case "bash_20250124":
			capability = "bash_tool"
		case "text_editor_20250124", "text_editor_20250728":
			capability = "text_editor_tool"
		}
		if capability != "" && !seenClientCapabilities[capability] {
			required = append(required, capability)
			seenClientCapabilities[capability] = true
		}
	}
	for _, toolset := range request.AnthropicClientToolsets {
		switch toolset.Type {
		case "computer_toolset_20260801":
			required = append(required, "computer_toolset")
		case "browser_toolset_20260801":
			required = append(required, "browser_toolset")
		}
	}
	if request.ResponseFormat != nil {
		required = append(required, "structured_output")
	}
	if openai.HasChatImages(request) {
		required = append(required, "vision")
	}
	if openai.HasChatAudioInput(request) {
		required = append(required, "audio_input")
	}
	if openai.HasChatFileInput(request) {
		required = append(required, "file_input")
	}
	if openai.HasChatTextDocuments(request) {
		required = append(required, "document_text")
	}
	if openai.HasChatVideoInput(request) {
		required = append(required, "video_input")
	}
	if request.WebSearchOptions != nil {
		required = append(required, "web_search")
	}
	if request.WebFetchOptions != nil {
		required = append(required, "web_fetch")
	}
	if request.GeminiCodeExecution {
		required = append(required, "gemini_code_execution")
	}
	if request.GeminiURLContext {
		required = append(required, "url_context")
	}
	if request.GeminiGoogleMaps {
		required = append(required, "google_maps")
	}
	if openai.ChatRequestsAudio(request) || openai.ChatHasAudioHistory(request) {
		required = append(required, "audio")
	}
	if count, _ := openai.ChatRequestPromptCacheBreakpoints(request); count > 0 {
		required = append(required, "prompt_cache")
	}
	for _, message := range request.Messages {
		if message.Prefix != nil && *message.Prefix {
			required = append(required, "assistant_prefill")
			break
		}
	}
	return required
}

func audioAnonymizationError(request openai.ChatCompletionRequest, attempt modules.RequestContext) error {
	if !openai.ChatRequestsAudio(request) || len(attempt.AnonymizationValues) == 0 {
		return nil
	}
	return &Error{Class: FailureContentPolicy, Provider: attempt.Metadata["provider.endpoint.name"], StatusCode: 400, UpstreamCode: "audio_anonymization_unsupported", Param: "audio", Err: errors.New("audio output cannot restore anonymized prompt values")}
}

func bindChatAudioHistory(request openai.ChatCompletionRequest, candidates []Endpoint) ([]Endpoint, error) {
	if !openai.ChatHasAudioHistory(request) {
		return candidates, nil
	}
	requested := strings.TrimSpace(request.Provider)
	if requested == "" || requested == "auto" {
		return nil, &Error{Class: FailureClientRequest, StatusCode: 400, UpstreamCode: "audio_history_requires_deployment", Param: "provider", Err: errors.New("messages.audio requires an exact deployment name in provider")}
	}
	bound := candidates[:0]
	for _, candidate := range candidates {
		if candidate.Name == requested && candidate.FallbackStage == 0 && candidate.RoutingModel == request.Model {
			bound = append(bound, candidate)
		}
	}
	if len(bound) != 1 {
		return nil, &Error{Class: FailureClientRequest, StatusCode: 400, UpstreamCode: "audio_history_requires_deployment", Param: "provider", Err: errors.New("messages.audio provider must identify one available deployment")}
	}
	return bound, nil
}

func requiredResponseCapabilities(request openai.ResponseRequest, stream bool) []string {
	required := []string{"responses"}
	if request.Background {
		required = append(required, "background_responses")
	}
	if stream {
		required = append(required, "stream")
	}
	if len(request.Tools) > 0 {
		required = append(required, "tools")
	}
	for _, tool := range request.Tools {
		if tool.Type == "mcp" {
			required = append(required, "mcp")
			break
		}
	}
	if request.Text != nil {
		required = append(required, "structured_output")
	}
	if openai.HasResponseImages(request) {
		required = append(required, "vision")
	}
	if openai.HasResponseAudio(request) {
		required = append(required, "audio")
	}
	if openai.HasResponseFiles(request) {
		required = append(required, "file_input")
	}
	return required
}

func hasCapability(capabilities []string, expected string) bool {
	for _, capability := range capabilities {
		if capability == expected {
			return true
		}
	}
	return false
}

func (e Endpoint) supportsModel(model string) bool {
	if _, found := e.ModelAliases[model]; found {
		return true
	}
	if len(e.Models) == 0 {
		return true
	}
	for _, supported := range e.Models {
		if supported == model {
			return true
		}
	}
	return false
}

func (e Endpoint) supportsCapabilities(required ...string) bool {
	if hasCapability(required, "chat") {
		if client, ok := e.Provider.(interface{ SupportsChat() bool }); ok && !client.SupportsChat() {
			return false
		}
	}
	if hasCapability(required, "responses") {
		if client, ok := e.Provider.(interface{ SupportsResponses() bool }); ok && !client.SupportsResponses() {
			return false
		}
	}
	if hasCapability(required, "rerank") {
		if client, ok := e.Provider.(interface{ SupportsRerank() bool }); ok && !client.SupportsRerank() {
			return false
		}
	}
	if hasCapability(required, "image_generation") {
		if client, ok := e.Provider.(interface{ SupportsImageGeneration() bool }); ok && !client.SupportsImageGeneration() {
			return false
		}
	}
	if hasCapability(required, "image_edit") {
		if client, ok := e.Provider.(interface{ SupportsImageEdit() bool }); ok && !client.SupportsImageEdit() {
			return false
		}
	}
	if hasCapability(required, "image_variation") {
		if client, ok := e.Provider.(interface{ SupportsImageVariation() bool }); ok && !client.SupportsImageVariation() {
			return false
		}
	}
	if hasCapability(required, "audio_transcription") {
		if client, ok := e.Provider.(interface{ SupportsAudioTranscription() bool }); ok && !client.SupportsAudioTranscription() {
			return false
		}
	}
	if hasCapability(required, "audio_translation") {
		if client, ok := e.Provider.(interface{ SupportsAudioTranslation() bool }); ok && !client.SupportsAudioTranslation() {
			return false
		}
	}
	if hasCapability(required, "audio_speech") {
		if client, ok := e.Provider.(interface{ SupportsAudioSpeech() bool }); ok && !client.SupportsAudioSpeech() {
			return false
		}
	}
	if hasCapability(required, "search") {
		if client, ok := e.Provider.(interface{ SupportsSearch() bool }); ok && !client.SupportsSearch() {
			return false
		}
	}
	if hasCapability(required, "ocr") {
		if client, ok := e.Provider.(interface{ SupportsOCR() bool }); ok && !client.SupportsOCR() {
			return false
		}
	}
	if hasCapability(required, "fine_tuning") {
		if _, ok := e.Provider.(FineTuningClient); !ok || e.Type != "openai" && e.Type != "openai-compatible" {
			return false
		}
	}
	if hasCapability(required, "video") {
		if _, ok := e.Provider.(VideoClient); !ok || e.Type != "openai" && e.Type != "openai-compatible" && e.Type != "xai" {
			return false
		}
	}
	if hasCapability(required, "container") {
		if _, ok := e.Provider.(ContainerClient); !ok || e.Type != "openai" && e.Type != "openai-compatible" {
			return false
		}
	}
	if hasCapability(required, "container_files") {
		if _, ok := e.Provider.(ContainerFileClient); !ok || e.Type != "openai" && e.Type != "openai-compatible" {
			return false
		}
	}
	if hasCapability(required, "container_network") {
		if _, ok := e.Provider.(ContainerClient); !ok || e.Type != "openai" && e.Type != "openai-compatible" {
			return false
		}
	}
	if hasCapability(required, "cached_content") {
		if _, ok := e.Provider.(GeminiCachedContentClient); !ok || e.Type != "gemini" {
			return false
		}
	}
	if hasCapability(required, "realtime") {
		if _, ok := e.Provider.(RealtimeClient); !ok || e.Type != "openai" && e.Type != "openai-compatible" {
			return false
		}
	}
	if hasCapability(required, "web_fetch") {
		client, ok := e.Provider.(interface{ SupportsWebFetch() bool })
		if !ok || !client.SupportsWebFetch() {
			return false
		}
	}
	if len(e.Capabilities) == 0 {
		return true
	}
	available := map[string]bool{}
	for _, capability := range e.Capabilities {
		available[capability] = true
	}
	for _, capability := range required {
		if !available[capability] {
			return false
		}
	}
	return true
}

func supportsCatalogCapabilities(catalog modelcatalog.Catalog, endpoint Endpoint, requestedModel string, required ...string) bool {
	// Catalog capabilities describe the model, not an expansion of deployment
	// or adapter support. Both must permit the operation.
	if !endpoint.supportsCapabilities(required...) {
		return false
	}
	models := []string{requestedModel}
	if upstream, found := endpoint.ModelAliases[requestedModel]; found {
		models = append(models, upstream)
	}
	entry, found := findEndpointCatalogEntry(catalog, endpoint, models...)
	if !found {
		if catalog.DenyUnknownModels() {
			return false
		}
		if requiresExplicitEndpointCapability(required) && !hasExplicitEndpointCapabilities(endpoint.Capabilities, required) {
			return false
		}
		return endpoint.supportsCapabilities(required...)
	}
	if entry.Capabilities == nil {
		if requiresExplicitEndpointCapability(required) && !hasExplicitEndpointCapabilities(endpoint.Capabilities, required) {
			return false
		}
		return endpoint.supportsCapabilities(required...)
	}
	available := make(map[string]bool, len(entry.Capabilities))
	for _, capability := range entry.Capabilities {
		available[capability] = true
	}
	for _, capability := range required {
		if !available[capability] {
			return false
		}
	}
	return true
}

func requiresExplicitEndpointCapability(required []string) bool {
	return hasCapability(required, "interactions") || hasCapability(required, "interaction_agents") || hasCapability(required, "interaction_environment_reuse") || hasCapability(required, "gemini_safety_settings") || hasCapability(required, "gemini_code_execution") || hasCapability(required, "url_context") || hasCapability(required, "google_maps") || hasCapability(required, "background_interactions") || hasCapability(required, "mcp") || hasCapability(required, "vision") || hasCapability(required, "rerank") || hasCapability(required, "moderation") || hasCapability(required, "image_generation") || hasCapability(required, "image_edit") || hasCapability(required, "image_variation") || hasCapability(required, "audio_transcription") || hasCapability(required, "audio_translation") || hasCapability(required, "audio_speech") || hasCapability(required, "ocr") || hasCapability(required, "search") || hasCapability(required, "fine_tuning") || hasCapability(required, "video") || hasCapability(required, "video_remix") || hasCapability(required, "video_extension") || hasCapability(required, "container") || hasCapability(required, "container_files") || hasCapability(required, "container_network") || hasCapability(required, "cached_content") || hasCapability(required, "video_input") || hasCapability(required, "realtime") || hasCapability(required, "web_search") || hasCapability(required, "web_fetch") || hasCapability(required, "tool_search") || hasCapability(required, "thinking") || hasCapability(required, "zero_output") || hasCapability(required, "inference_geo") || hasCapability(required, "context_management") || hasCapability(required, "tool_result_error") || hasCapability(required, "document_citations") || hasCapability(required, "document_metadata") || hasCapability(required, "document_text") || hasCapability(required, "audio") || hasCapability(required, "audio_input") || hasCapability(required, "prompt_cache") || hasCapability(required, "assistant_prefill") || hasCapability(required, "background_responses") || hasCapability(required, "file_input") || hasCapability(required, "bedrock_invoke")
}

func hasExplicitEndpointCapabilities(available []string, required []string) bool {
	for _, capability := range []string{"interactions", "interaction_agents", "interaction_environment_reuse", "gemini_safety_settings", "gemini_code_execution", "url_context", "google_maps", "background_interactions", "mcp", "vision", "rerank", "moderation", "image_generation", "image_edit", "image_variation", "audio_transcription", "audio_translation", "audio_speech", "ocr", "search", "fine_tuning", "video", "video_remix", "video_extension", "container", "container_files", "container_network", "cached_content", "video_input", "realtime", "web_search", "web_fetch", "tool_search", "thinking", "zero_output", "inference_geo", "context_management", "tool_result_error", "document_citations", "document_metadata", "document_text", "audio", "audio_input", "prompt_cache", "assistant_prefill", "background_responses", "file_input", "bedrock_invoke"} {
		if hasCapability(required, capability) && !hasCapability(available, capability) {
			return false
		}
	}
	return true
}

func supportsCatalogOutputLimit(catalog modelcatalog.Catalog, endpoint Endpoint, requestedModel string, requested *int) bool {
	if requested == nil || *requested <= 0 {
		return true
	}
	models := []string{requestedModel}
	if upstream, found := endpoint.ModelAliases[requestedModel]; found {
		models = append(models, upstream)
	}
	entry, found := findEndpointCatalogEntry(catalog, endpoint, models...)
	return !found || entry.MaxOutputTokens <= 0 || *requested <= entry.MaxOutputTokens
}

func findEndpointCatalogEntry(catalog modelcatalog.Catalog, endpoint Endpoint, models ...string) (modelcatalog.Model, bool) {
	return catalog.FindForProviders([]string{endpoint.Name, endpoint.ProviderID, endpoint.Type}, models...)
}

func (r Router) weightedOrder(candidates []Endpoint) []Endpoint {
	if len(candidates) < 2 || r.routeCounter == nil {
		return candidates
	}
	ordered := make([]Endpoint, 0, len(candidates))
	for start := 0; start < len(candidates); {
		end := start + 1
		for end < len(candidates) && candidates[end].Priority == candidates[start].Priority {
			end++
		}
		group := candidates[start:end]
		total := 0
		for _, endpoint := range group {
			weight := endpoint.Weight
			if weight <= 0 {
				weight = 1
			}
			total += weight
		}
		slot := int((r.routeCounter.Add(1) - 1) % uint64(total))
		selected := 0
		for index, endpoint := range group {
			weight := endpoint.Weight
			if weight <= 0 {
				weight = 1
			}
			if slot < weight {
				selected = index
				break
			}
			slot -= weight
		}
		selectedGroup := make([]Endpoint, 0, len(group))
		selectedGroup = append(selectedGroup, group[selected])
		selectedGroup = append(selectedGroup, group[:selected]...)
		selectedGroup = append(selectedGroup, group[selected+1:]...)
		if r.routingStrategy == "adaptive" {
			selectedGroup = r.adaptive.order(selectedGroup)
		}
		ordered = append(ordered, selectedGroup...)
		start = end
	}
	return ordered
}

func providerFor(endpoint config.ProviderEndpointConfig) Client {
	switch endpoint.Type {
	case "ollama":
		return NewOllama(endpoint.BaseURL, endpoint.Stream)
	case "openai":
		client := NewOpenAICompatibleWithRerankPath(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream, endpoint.RerankPath)
		client.errorProvider = "openai"
		return client
	case "openai-compatible":
		return NewOpenAICompatibleWithRerankPath(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream, endpoint.RerankPath)
	case "openrouter":
		return NewOpenRouter(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream, endpoint.RerankPath)
	case "azure-openai":
		return NewAzureOpenAI(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream, endpoint.APIVersion, endpoint.AuthType)
	case "gemini":
		return NewGeminiWithAuth(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream, endpoint.AuthType)
	case "anthropic":
		return NewAnthropic(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream)
	case "cohere":
		return NewCohere(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream)
	case "mistral":
		return NewMistral(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream)
	case "voyage":
		return NewVoyage(endpoint.BaseURL, endpoint.APIKey)
	case "bedrock":
		return NewBedrockWithAuth(endpoint.BaseURL, endpoint.APIKey, endpoint.AuthType, endpoint.Region)
	case "groq":
		return NewGroq(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream)
	case "deepseek":
		return NewDeepSeek(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream)
	case "cerebras":
		return NewCerebras(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream)
	case "nvidia-nim":
		return NewNVIDIANIM(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream)
	case "together":
		return NewTogether(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream)
	case "xai":
		return NewXAI(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream)
	case "opensandbox":
		return NewOpenSandbox(endpoint.BaseURL, endpoint.APIKey)
	case "demo":
		return Demo{}
	default:
		return nil
	}
}
