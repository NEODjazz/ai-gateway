package config

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"ai-gateway-gateway/internal/modelcatalog"
)

type Config struct {
	HTTP      HTTPConfig
	Cache     CacheConfig
	Redis     RedisConfig
	Modules   ModuleConfig
	Provider  ProviderConfig
	Catalog   modelcatalog.Catalog
	Telemetry TelemetryConfig
	InitErr   error
}

type CacheConfig struct {
	TTLSeconds int
	MaxBytes   int
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
}

type ProviderConfig struct {
	Default           string
	Endpoints         []ProviderEndpointConfig
	GuardrailPolicies map[string]GuardrailPolicyConfig
}

type GuardrailPolicyConfig struct {
	DLP bool `json:"dlp"`
	AV  bool `json:"av"`
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
	GuardrailPolicy       string            `json:"guardrail_policy,omitempty"`
	ModelAliases          map[string]string `json:"model_aliases,omitempty"`
	Weight                int               `json:"weight,omitempty"`
	Capabilities          []string          `json:"capabilities,omitempty"`
}

func Load() Config {
	catalog, catalogErr := modelcatalog.Parse(os.Getenv("MODEL_CATALOG_JSON"))
	return Config{
		HTTP: HTTPConfig{
			Addr: env("HTTP_ADDR", ":8080"),
		},
		Cache: CacheConfig{
			TTLSeconds: envInt("EXACT_CACHE_TTL_SECONDS", 0),
			MaxBytes:   envInt("EXACT_CACHE_MAX_BYTES", 1_048_576),
		},
		Redis: RedisConfig{
			Addr: env("REDIS_ADDR", ""), Password: os.Getenv("REDIS_PASSWORD"),
			DB: envInt("REDIS_DB", 0), Prefix: env("REDIS_PREFIX", "ai-gateway"),
		},
		Provider: ProviderConfig{
			Default:           env("DEFAULT_PROVIDER", env("PROVIDER_TYPE", "demo")),
			Endpoints:         loadProviderEndpoints(),
			GuardrailPolicies: loadGuardrailPolicies(),
		},
		Catalog: catalog,
		Telemetry: TelemetryConfig{
			ServiceName: env("OTEL_SERVICE_NAME", "ai-gateway"),
			Version:     env("AI_GATEWAY_VERSION", "dev"),
			Endpoint:    os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"),
			SampleRatio: envFloat("OTEL_TRACE_SAMPLE_RATIO", 1),
		},
		InitErr: catalogErr,
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
