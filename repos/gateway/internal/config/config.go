package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"ai-gateway-gateway/internal/modelcatalog"
)

type Config struct {
	HTTP         HTTPConfig
	Cache        CacheConfig
	Redis        RedisConfig
	Modules      ModuleConfig
	Provider     ProviderConfig
	Catalog      modelcatalog.Catalog
	Telemetry    TelemetryConfig
	Management   ManagementConfig
	APIDocs      APIDocsConfig
	AdminUI      AdminUIConfig
	Guardrails   GuardrailMonitorConfig
	Files        FileConfig
	VectorStores VectorStoreConfig
	A2ATasks     A2ATaskConfig
	InitErr      error
}

type FileConfig struct {
	MaxBytes        int64
	OwnerQuotaBytes int64
}

type VectorStoreConfig struct {
	OwnerQuota int
	FileQuota  int
}

type A2ATaskConfig struct {
	OwnerQuota int
	TTL        time.Duration
}

type GuardrailMonitorConfig struct {
	Capacity int
	TTL      time.Duration
}

type APIDocsConfig struct {
	Enabled         bool
	TryItOutEnabled bool
}

type AdminUIConfig struct {
	Enabled bool
}

type ManagementConfig struct {
	AuthURL       string
	Secret        string
	BillingURL    string
	BillingSecret string
}

type CacheConfig struct {
	TTLSeconds int
	MaxBytes   int
	Semantic   SemanticCacheConfig
}

type SemanticCacheConfig struct {
	TTLSeconds      int
	Threshold       float64
	MaxEntries      int
	MaxBytes        int
	EmbeddingURL    string
	EmbeddingAPIKey string
	EmbeddingModel  string
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	Prefix   string
}

type HTTPConfig struct {
	Addr string
}

type TelemetryConfig struct {
	ServiceName string
	Version     string
	Endpoint    string
	SampleRatio float64
}

type ModuleConfig struct {
	Auth       FeatureConfig
	Anonymizer FeatureConfig
	Billing    FeatureConfig
	DLP        FeatureConfig
	AV         FeatureConfig
}

type FeatureConfig struct {
	Required bool
	URL      string
	Secret   string
}

type ProviderConfig struct {
	Default              string
	Endpoints            []ProviderEndpointConfig
	GuardrailPolicies    map[string]GuardrailPolicyConfig
	RoutingStrategy      string
	AdaptiveEWMAAlpha    float64
	AffinityTTL          time.Duration
	ResponseOwnershipTTL time.Duration
	CredentialKey        string
	ControlPlaneDSN      string
	ControlPlaneRefresh  time.Duration
}

type GuardrailPolicyConfig struct {
	DLP       bool `json:"dlp"`
	OutputDLP bool `json:"output_dlp,omitempty"`
	AV        bool `json:"av"`
}

type ProviderEndpointConfig struct {
	Name                  string            `json:"name"`
	Type                  string            `json:"type"`
	BaseURL               string            `json:"base_url"`
	APIKey                string            `json:"api_key,omitempty"`
	Models                []string          `json:"models,omitempty"`
	Enabled               *bool             `json:"enabled,omitempty"`
	Priority              int               `json:"priority,omitempty"`
	Stream                bool              `json:"stream,omitempty"`
	DLPEnabled            bool              `json:"dlp_enabled,omitempty"`
	AVEnabled             bool              `json:"av_enabled,omitempty"`
	MaxRetries            int               `json:"max_retries,omitempty"`
	CooldownAfterFailures int               `json:"cooldown_after_failures,omitempty"`
	CooldownSeconds       int               `json:"cooldown_seconds,omitempty"`
	MaxParallelRequests   int               `json:"max_parallel_requests,omitempty"`
	QueueCapacity         int               `json:"queue_capacity,omitempty"`
	QueueTimeoutMS        int               `json:"queue_timeout_ms,omitempty"`
	RateLimitRPM          int               `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM          int               `json:"rate_limit_tpm,omitempty"`
	GuardrailPolicy       string            `json:"guardrail_policy,omitempty"`
	ModelAliases          map[string]string `json:"model_aliases,omitempty"`
	Weight                int               `json:"weight,omitempty"`
	Capabilities          []string          `json:"capabilities,omitempty"`
	Shadow                bool              `json:"shadow,omitempty"`
	MirrorPercentage      float64           `json:"mirror_percentage,omitempty"`
	MirrorTimeoutMS       int               `json:"mirror_timeout_ms,omitempty"`
	RerankPath            string            `json:"rerank_path,omitempty"`
	APIVersion            string            `json:"api_version,omitempty"`
	AuthType              string            `json:"auth_type,omitempty"`
	Region                string            `json:"region,omitempty"`
}

func Load() Config {
	catalog, catalogErr := modelcatalog.Parse(os.Getenv("MODEL_CATALOG_JSON"))
	providerEndpoints := loadProviderEndpoints()
	providerAdmissionErr := validateProviderAdmission(providerEndpoints)
	semanticTTL := envInt("SEMANTIC_CACHE_TTL_SECONDS", 0)
	semanticThreshold := envFloat("SEMANTIC_CACHE_THRESHOLD", 0.95)
	semanticURL := strings.TrimSpace(os.Getenv("SEMANTIC_CACHE_EMBEDDING_URL"))
	semanticModel := strings.TrimSpace(os.Getenv("SEMANTIC_CACHE_EMBEDDING_MODEL"))
	guardrailMonitorCapacity := envInt("GUARDRAIL_MONITOR_CAPACITY", 1000)
	guardrailMonitorTTLSeconds := envInt("GUARDRAIL_MONITOR_TTL_SECONDS", 604800)
	fileMaxBytes := envInt64("FILE_MAX_BYTES", 32<<20)
	fileOwnerQuotaBytes := envInt64("FILE_OWNER_QUOTA_BYTES", 1<<30)
	vectorStoreOwnerQuota := envInt("VECTOR_STORE_OWNER_QUOTA", 1000)
	vectorStoreFileQuota := envInt("VECTOR_STORE_FILE_QUOTA", 10000)
	a2aTaskOwnerQuota := envInt("A2A_TASK_OWNER_QUOTA", 1000)
	a2aTaskTTLSeconds := envInt("A2A_TASK_TTL_SECONDS", 2_592_000)
	var semanticErr error
	var guardrailMonitorErr error
	var fileErr error
	var vectorStoreErr error
	var a2aTaskErr error
	controlPlaneDSN := strings.TrimSpace(os.Getenv("PROVIDER_CONTROL_PLANE_POSTGRES_DSN"))
	credentialKey := os.Getenv("PROVIDER_CREDENTIAL_ENCRYPTION_KEY")
	var controlPlaneErr error
	if controlPlaneDSN != "" && len(credentialKey) < 16 {
		controlPlaneErr = errors.New("provider credential encryption key must be at least 16 characters when control plane persistence is enabled")
	}
	if semanticTTL > 0 && (semanticURL == "" || semanticModel == "") {
		semanticErr = errors.New("semantic cache embedding url and model are required when enabled")
	}
	if semanticTTL > 0 && (semanticThreshold <= 0 || semanticThreshold > 1) {
		semanticErr = errors.Join(semanticErr, errors.New("semantic cache threshold must be in (0,1]"))
	}
	if guardrailMonitorCapacity < 1 || guardrailMonitorCapacity > 10000 {
		guardrailMonitorErr = errors.New("guardrail monitor capacity must be between 1 and 10000")
	}
	if guardrailMonitorTTLSeconds < 1 {
		guardrailMonitorErr = errors.Join(guardrailMonitorErr, errors.New("guardrail monitor ttl must be positive"))
	}
	if fileMaxBytes < 1 || fileMaxBytes > 512<<20 {
		fileErr = errors.New("file max bytes must be between 1 and 536870912")
	}
	if fileOwnerQuotaBytes < fileMaxBytes {
		fileErr = errors.Join(fileErr, errors.New("file owner quota bytes must be at least file max bytes"))
	}
	if vectorStoreOwnerQuota < 1 || vectorStoreOwnerQuota > 100000 {
		vectorStoreErr = errors.New("vector store owner quota must be between 1 and 100000")
	}
	if vectorStoreFileQuota < 1 || vectorStoreFileQuota > 100000 {
		vectorStoreErr = errors.Join(vectorStoreErr, errors.New("vector store file quota must be between 1 and 100000"))
	}
	if a2aTaskOwnerQuota < 1 || a2aTaskOwnerQuota > 100000 {
		a2aTaskErr = errors.New("A2A task owner quota must be between 1 and 100000")
	}
	if a2aTaskTTLSeconds < 60 || a2aTaskTTLSeconds > 31_536_000 {
		a2aTaskErr = errors.Join(a2aTaskErr, errors.New("A2A task ttl must be between 60 and 31536000 seconds"))
	}
	return Config{
		HTTP: HTTPConfig{
			Addr: env("HTTP_ADDR", ":8080"),
		},
		Cache: CacheConfig{
			TTLSeconds: envInt("EXACT_CACHE_TTL_SECONDS", 0),
			MaxBytes:   envInt("EXACT_CACHE_MAX_BYTES", 1_048_576),
			Semantic: SemanticCacheConfig{
				TTLSeconds:      semanticTTL,
				Threshold:       semanticThreshold,
				MaxEntries:      envInt("SEMANTIC_CACHE_MAX_ENTRIES", 100),
				MaxBytes:        envInt("SEMANTIC_CACHE_MAX_BYTES", 1_048_576),
				EmbeddingURL:    semanticURL,
				EmbeddingAPIKey: os.Getenv("SEMANTIC_CACHE_EMBEDDING_API_KEY"),
				EmbeddingModel:  semanticModel,
			},
		},
		Redis: RedisConfig{
			Addr: env("REDIS_ADDR", ""), Password: os.Getenv("REDIS_PASSWORD"),
			DB: envInt("REDIS_DB", 0), Prefix: env("REDIS_PREFIX", "ai-gateway"),
		},
		Provider: ProviderConfig{
			Default:              env("DEFAULT_PROVIDER", env("PROVIDER_TYPE", "demo")),
			Endpoints:            providerEndpoints,
			GuardrailPolicies:    loadGuardrailPolicies(),
			RoutingStrategy:      env("ROUTING_STRATEGY", "weighted"),
			AdaptiveEWMAAlpha:    envFloat("ADAPTIVE_ROUTING_EWMA_ALPHA", 0.2),
			AffinityTTL:          time.Duration(envInt("RESPONSES_AFFINITY_TTL_SECONDS", 3600)) * time.Second,
			ResponseOwnershipTTL: time.Duration(envInt("RESPONSES_OWNERSHIP_TTL_SECONDS", 2_592_000)) * time.Second,
			CredentialKey:        credentialKey,
			ControlPlaneDSN:      controlPlaneDSN,
			ControlPlaneRefresh:  time.Duration(envInt("PROVIDER_CONTROL_PLANE_REFRESH_SECONDS", 1)) * time.Second,
		},
		Catalog: catalog,
		Telemetry: TelemetryConfig{
			ServiceName: env("OTEL_SERVICE_NAME", "ai-gateway"),
			Version:     env("AI_GATEWAY_VERSION", "dev"),
			Endpoint:    os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"),
			SampleRatio: envFloat("OTEL_TRACE_SAMPLE_RATIO", 1),
		},
		Management: ManagementConfig{
			AuthURL:       env("MANAGEMENT_AUTH_URL", env("AUTH_URL", "")),
			Secret:        os.Getenv("MANAGEMENT_SHARED_SECRET"),
			BillingURL:    env("BILLING_MANAGEMENT_URL", env("BILLING_URL", "")),
			BillingSecret: os.Getenv("BILLING_MANAGEMENT_SHARED_SECRET"),
		},
		APIDocs: APIDocsConfig{
			Enabled:         envBool("API_DOCS_ENABLED", false),
			TryItOutEnabled: envBool("API_DOCS_TRY_IT_OUT_ENABLED", false),
		},
		AdminUI: AdminUIConfig{Enabled: envBool("ADMIN_UI_ENABLED", true)},
		Guardrails: GuardrailMonitorConfig{
			Capacity: guardrailMonitorCapacity,
			TTL:      time.Duration(guardrailMonitorTTLSeconds) * time.Second,
		},
		Files:        FileConfig{MaxBytes: fileMaxBytes, OwnerQuotaBytes: fileOwnerQuotaBytes},
		VectorStores: VectorStoreConfig{OwnerQuota: vectorStoreOwnerQuota, FileQuota: vectorStoreFileQuota},
		A2ATasks:     A2ATaskConfig{OwnerQuota: a2aTaskOwnerQuota, TTL: time.Duration(a2aTaskTTLSeconds) * time.Second},
		InitErr:      errors.Join(catalogErr, semanticErr, providerAdmissionErr, controlPlaneErr, guardrailMonitorErr, fileErr, vectorStoreErr, a2aTaskErr),
		Modules: ModuleConfig{
			Auth: FeatureConfig{
				Required: envBool("AUTH_REQUIRED", true),
				URL:      env("AUTH_URL", ""),
			},
			Anonymizer: FeatureConfig{
				Required: envBool("ANONYMIZER_REQUIRED", false),
				URL:      env("ANONYMIZER_URL", ""),
			},
			Billing: FeatureConfig{
				Required: envBool("BILLING_REQUIRED", false),
				URL:      env("BILLING_URL", ""),
				Secret:   os.Getenv("BILLING_SHARED_SECRET"),
			},
			DLP: FeatureConfig{
				Required: envBool("DLP_REQUIRED", true),
				URL:      env("DLP_URL", ""),
			},
			AV: FeatureConfig{
				Required: envBool("AV_REQUIRED", true),
				URL:      env("AV_URL", ""),
			},
		},
	}
}

func validateProviderAdmission(endpoints []ProviderEndpointConfig) error {
	var result error
	for _, endpoint := range endpoints {
		name := endpoint.Name
		if name == "" {
			name = endpoint.Type
		}
		if endpoint.MaxParallelRequests < 0 || endpoint.QueueCapacity < 0 || endpoint.QueueTimeoutMS < 0 || endpoint.RateLimitRPM < 0 || endpoint.RateLimitTPM < 0 {
			result = errors.Join(result, fmt.Errorf("provider %q admission values must not be negative", name))
		}
		if endpoint.RateLimitRPM > 10000000 || endpoint.RateLimitTPM > 1000000000 {
			result = errors.Join(result, fmt.Errorf("provider %q rate limits exceed supported bounds", name))
		}
		if endpoint.QueueCapacity > 0 && endpoint.MaxParallelRequests <= 0 {
			result = errors.Join(result, fmt.Errorf("provider %q queue requires max_parallel_requests", name))
		}
		if endpoint.MirrorPercentage < 0 || endpoint.MirrorPercentage > 100 || endpoint.MirrorTimeoutMS < 0 {
			result = errors.Join(result, fmt.Errorf("provider %q mirror values are invalid", name))
		}
		if endpoint.Shadow && endpoint.MaxParallelRequests <= 0 {
			result = errors.Join(result, fmt.Errorf("shadow provider %q requires max_parallel_requests", name))
		}
		if endpoint.RerankPath != "" && (!strings.HasPrefix(endpoint.RerankPath, "/") || strings.ContainsAny(endpoint.RerankPath, "?#") || strings.Contains(endpoint.RerankPath, "..")) {
			result = errors.Join(result, fmt.Errorf("provider %q rerank_path must be an absolute path without query, fragment, or traversal", name))
		}
		if endpoint.Type == "azure-openai" {
			if parsed, err := url.Parse(endpoint.BaseURL); err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				result = errors.Join(result, fmt.Errorf("provider %q base_url must not contain query or fragment", name))
			}
			if !validAzureAPIVersion(endpoint.APIVersion) {
				result = errors.Join(result, fmt.Errorf("provider %q has invalid api_version", name))
			}
			authType := strings.ToLower(strings.TrimSpace(endpoint.AuthType))
			if authType != "" && authType != "api_key" && authType != "entra" {
				result = errors.Join(result, fmt.Errorf("provider %q auth_type must be api_key or entra", name))
			}
		} else if endpoint.Type == "gemini" {
			authType := strings.ToLower(strings.TrimSpace(endpoint.AuthType))
			if authType != "" && authType != "api_key" && authType != "gcp_adc" {
				result = errors.Join(result, fmt.Errorf("provider %q auth_type must be api_key or gcp_adc", name))
			}
			if endpoint.APIVersion != "" || endpoint.Region != "" {
				result = errors.Join(result, fmt.Errorf("provider %q api_version or region is unsupported for Gemini", name))
			}
		} else if endpoint.Type == "bedrock" {
			if parsed, err := url.Parse(endpoint.BaseURL); err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				result = errors.Join(result, fmt.Errorf("provider %q base_url must not contain query or fragment", name))
			}
			authType := strings.ToLower(strings.TrimSpace(endpoint.AuthType))
			if authType == "" {
				authType = "bearer"
			}
			if authType != "bearer" && authType != "aws_sigv4" {
				result = errors.Join(result, fmt.Errorf("provider %q auth_type must be bearer or aws_sigv4", name))
			}
			if authType == "aws_sigv4" && !validAWSRegion(endpoint.Region) {
				result = errors.Join(result, fmt.Errorf("provider %q region is required for aws_sigv4", name))
			}
			if authType == "aws_sigv4" && endpoint.APIKey != "" && !validAWSCredentialJSON(endpoint.APIKey) {
				result = errors.Join(result, fmt.Errorf("provider %q has invalid aws_sigv4 credential", name))
			}
		} else if endpoint.APIVersion != "" || endpoint.AuthType != "" || endpoint.Region != "" {
			result = errors.Join(result, fmt.Errorf("provider %q api_version, auth_type or region is unsupported for this type", name))
		}
		if endpoint.QueueCapacity > 0 && endpoint.QueueTimeoutMS <= 0 {
			result = errors.Join(result, fmt.Errorf("provider %q queue requires queue_timeout_ms", name))
		}
	}
	return result
}

func validAWSCredentialJSON(raw string) bool {
	var value struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		SessionToken    string `json:"session_token,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&value) == nil && decoder.Decode(&struct{}{}) == io.EOF && strings.TrimSpace(value.AccessKeyID) != "" && len(value.AccessKeyID) <= 128 && value.SecretAccessKey != "" && len(value.SecretAccessKey) <= 256 && len(value.SessionToken) <= 4096
}

func validAWSRegion(value string) bool {
	value = strings.TrimSpace(value)
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

func validAzureAPIVersion(value string) bool {
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

func loadGuardrailPolicies() map[string]GuardrailPolicyConfig {
	policies := map[string]GuardrailPolicyConfig{}
	raw := strings.TrimSpace(os.Getenv("GUARDRAIL_POLICIES_JSON"))
	if raw == "" {
		return policies
	}
	if err := json.Unmarshal([]byte(raw), &policies); err != nil {
		return map[string]GuardrailPolicyConfig{}
	}
	return policies
}

func loadProviderEndpoints() []ProviderEndpointConfig {
	raw := os.Getenv("PROVIDERS_JSON")
	if strings.TrimSpace(raw) != "" {
		var endpoints []ProviderEndpointConfig
		if err := json.Unmarshal([]byte(raw), &endpoints); err == nil {
			return normalizeProviderEndpoints(endpoints)
		}
	}

	providerType := env("PROVIDER_TYPE", "demo")
	switch providerType {
	case "ollama":
		return []ProviderEndpointConfig{
			{
				Name:    "ollama-local",
				Type:    "ollama",
				BaseURL: env("OLLAMA_URL", "http://127.0.0.1:11434"),
				Enabled: boolPtr(true),
			},
		}
	default:
		return []ProviderEndpointConfig{
			{
				Name:    "demo",
				Type:    "demo",
				Enabled: boolPtr(true),
			},
		}
	}
}

func normalizeProviderEndpoints(endpoints []ProviderEndpointConfig) []ProviderEndpointConfig {
	normalized := make([]ProviderEndpointConfig, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Name == "" {
			endpoint.Name = endpoint.Type
		}
		if endpoint.Type == "" {
			endpoint.Type = endpoint.Name
		}
		if endpoint.APIKey == "" {
			endpoint.APIKey = env(providerAPIKeyEnvName(endpoint.Name), "")
		}
		// JSON bool defaults to false, but endpoints should be enabled unless explicitly disabled
		// by providing enabled:false together with a name/type in a future richer schema.
		normalized = append(normalized, endpoint)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].Priority < normalized[j].Priority
	})
	return normalized
}

func boolPtr(value bool) *bool {
	return &value
}

func providerAPIKeyEnvName(name string) string {
	var builder strings.Builder
	for _, value := range name {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			builder.WriteRune(unicode.ToUpper(value))
			continue
		}
		builder.WriteByte('_')
	}
	return "PROVIDER_API_KEY_" + builder.String()
}

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value == "true" || value == "1" || value == "yes"
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envFloat(key string, fallback float64) float64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}
