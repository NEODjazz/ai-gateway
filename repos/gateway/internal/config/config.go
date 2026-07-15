package config

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"unicode"
)

type Config struct {
	HTTP     HTTPConfig
	Modules  ModuleConfig
	Provider ProviderConfig
}

type HTTPConfig struct {
	Addr string
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
	Default   string
	Endpoints []ProviderEndpointConfig
}

type ProviderEndpointConfig struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	BaseURL    string   `json:"base_url"`
	APIKey     string   `json:"api_key,omitempty"`
	Models     []string `json:"models,omitempty"`
	Enabled    *bool    `json:"enabled,omitempty"`
	Priority   int      `json:"priority,omitempty"`
	Stream     bool     `json:"stream,omitempty"`
	DLPEnabled bool     `json:"dlp_enabled,omitempty"`
	AVEnabled  bool     `json:"av_enabled,omitempty"`
}

func Load() Config {
	return Config{
		HTTP: HTTPConfig{
			Addr: env("HTTP_ADDR", ":8080"),
		},
		Provider: ProviderConfig{
			Default:   env("DEFAULT_PROVIDER", env("PROVIDER_TYPE", "demo")),
			Endpoints: loadProviderEndpoints(),
		},
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
