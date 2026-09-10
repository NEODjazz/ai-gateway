package config

import (
	"testing"
	"time"
)

func TestNormalizeProviderEndpointsLoadsAPIKeyFromEnv(t *testing.T) {
	t.Setenv("PROVIDER_API_KEY_OPENROUTER_KEY_A", "secret-from-env")

	endpoints := normalizeProviderEndpoints([]ProviderEndpointConfig{
		{
			Name: "openrouter-key-a",
			Type: "openai-compatible",
		},
	})

	if len(endpoints) != 1 {
		t.Fatalf("expected one endpoint, got %d", len(endpoints))
	}
	if endpoints[0].APIKey != "secret-from-env" {
		t.Fatalf("expected api key from env, got %q", endpoints[0].APIKey)
	}
}

func TestNormalizeProviderEndpointsKeepsExplicitAPIKey(t *testing.T) {
	t.Setenv("PROVIDER_API_KEY_OPENROUTER_KEY_A", "secret-from-env")

	endpoints := normalizeProviderEndpoints([]ProviderEndpointConfig{
		{
			Name:   "openrouter-key-a",
			Type:   "openai-compatible",
			APIKey: "explicit-secret",
		},
	})

	if endpoints[0].APIKey != "explicit-secret" {
		t.Fatalf("expected explicit api key, got %q", endpoints[0].APIKey)
	}
}

func TestValidateProviderAdmissionRejectsUnsafeRerankPath(t *testing.T) {
	for _, path := range []string{"rerank", "/../rerank", "/rerank?token=secret", "/rerank#fragment"} {
		if err := validateProviderAdmission([]ProviderEndpointConfig{{Name: "reranker", RerankPath: path}}); err == nil {
			t.Fatalf("unsafe rerank path %q accepted", path)
		}
	}
	if err := validateProviderAdmission([]ProviderEndpointConfig{{Name: "reranker", RerankPath: "/rerank"}}); err != nil {
		t.Fatalf("safe rerank path rejected: %v", err)
	}
}

func TestValidateAzureOpenAIConfiguration(t *testing.T) {
	valid := ProviderEndpointConfig{Name: "azure", Type: "azure-openai", APIVersion: "2025-04-01-preview", AuthType: "entra"}
	if err := validateProviderAdmission([]ProviderEndpointConfig{valid}); err != nil {
		t.Fatalf("valid Azure configuration rejected: %v", err)
	}
	for _, endpoint := range []ProviderEndpointConfig{
		{Name: "azure", Type: "azure-openai", APIVersion: "2025-13-01"},
		{Name: "azure", Type: "azure-openai", AuthType: "basic"},
		{Name: "azure", Type: "azure-openai", BaseURL: "https://example.test?secret=value"},
		{Name: "other", Type: "openai-compatible", APIVersion: "2025-04-01-preview"},
	} {
		if err := validateProviderAdmission([]ProviderEndpointConfig{endpoint}); err == nil {
			t.Fatalf("invalid Azure configuration accepted: %+v", endpoint)
		}
	}
}

func TestValidateBedrockSigV4Configuration(t *testing.T) {
	valid := ProviderEndpointConfig{Name: "bedrock", Type: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", AuthType: "aws_sigv4", Region: "us-east-1", APIKey: `{"access_key_id":"AKID","secret_access_key":"secret","session_token":"token"}`}
	if err := validateProviderAdmission([]ProviderEndpointConfig{valid}); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	for _, endpoint := range []ProviderEndpointConfig{
		{Name: "bedrock", Type: "bedrock", BaseURL: valid.BaseURL, AuthType: "aws_sigv4", APIKey: valid.APIKey},
		{Name: "bedrock", Type: "bedrock", BaseURL: valid.BaseURL, AuthType: "aws_sigv4", Region: "US_EAST_1", APIKey: valid.APIKey},
		{Name: "bedrock", Type: "bedrock", BaseURL: valid.BaseURL + "?token=x", AuthType: "aws_sigv4", Region: "us-east-1", APIKey: valid.APIKey},
		{Name: "bedrock", Type: "bedrock", BaseURL: valid.BaseURL, AuthType: "aws_sigv4", Region: "us-east-1", APIKey: `{}`},
	} {
		if err := validateProviderAdmission([]ProviderEndpointConfig{endpoint}); err == nil {
			t.Fatalf("invalid configuration accepted: %+v", endpoint)
		}
	}
}

func TestLoadRoutingAndCacheConfiguration(t *testing.T) {
	t.Setenv("EXACT_CACHE_TTL_SECONDS", "120")
	t.Setenv("EXACT_CACHE_MAX_BYTES", "2048")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("REDIS_DB", "2")
	t.Setenv("REDIS_PREFIX", "tenant-gateway")
	t.Setenv("ROUTING_STRATEGY", "adaptive")
	t.Setenv("ADAPTIVE_ROUTING_EWMA_ALPHA", "0.35")
	t.Setenv("RESPONSES_AFFINITY_TTL_SECONDS", "7200")
	t.Setenv("RESPONSES_OWNERSHIP_TTL_SECONDS", "2592000")
	t.Setenv("MANAGEMENT_AUTH_URL", "http://auth:8082")
	t.Setenv("MANAGEMENT_SHARED_SECRET", "internal-secret")
	t.Setenv("BILLING_MANAGEMENT_URL", "http://billing:8083")
	t.Setenv("BILLING_MANAGEMENT_SHARED_SECRET", "billing-secret")
	t.Setenv("BILLING_SHARED_SECRET", "billing-usage-secret")
	t.Setenv("SEMANTIC_CACHE_TTL_SECONDS", "600")
	t.Setenv("SEMANTIC_CACHE_THRESHOLD", "0.97")
	t.Setenv("SEMANTIC_CACHE_MAX_ENTRIES", "25")
	t.Setenv("SEMANTIC_CACHE_MAX_BYTES", "4096")
	t.Setenv("SEMANTIC_CACHE_EMBEDDING_URL", "http://embedding:8080/v1")
	t.Setenv("SEMANTIC_CACHE_EMBEDDING_API_KEY", "embedding-secret")
	t.Setenv("SEMANTIC_CACHE_EMBEDDING_MODEL", "text-embedding")
	t.Setenv("GUARDRAIL_POLICIES_JSON", `{"strict":{"dlp":true,"av":true}}`)
	t.Setenv("GUARDRAIL_MONITOR_CAPACITY", "750")
	t.Setenv("GUARDRAIL_MONITOR_TTL_SECONDS", "86400")
	t.Setenv("PROVIDERS_JSON", `[{"name":"group-a","type":"demo","model_aliases":{"fast":"upstream-fast"},"weight":3,"capabilities":["chat"],"max_parallel_requests":4,"queue_capacity":8,"queue_timeout_ms":250,"rate_limit_rpm":120,"rate_limit_tpm":64000,"shadow":true,"mirror_percentage":12.5,"mirror_timeout_ms":900}]`)
	cfg := Load()
	if cfg.Cache.TTLSeconds != 120 || cfg.Cache.MaxBytes != 2048 || !cfg.Provider.GuardrailPolicies["strict"].DLP || !cfg.Provider.GuardrailPolicies["strict"].AV {
		t.Fatalf("unexpected cache/policy config: %+v", cfg)
	}
	if cfg.Guardrails.Capacity != 750 || cfg.Guardrails.TTL != 24*time.Hour {
		t.Fatalf("unexpected guardrail monitor config: %+v", cfg.Guardrails)
	}
	if cfg.Redis.Addr != "redis:6379" || cfg.Redis.DB != 2 || cfg.Redis.Prefix != "tenant-gateway" {
		t.Fatalf("unexpected redis config: %+v", cfg.Redis)
	}
	if cfg.Provider.RoutingStrategy != "adaptive" || cfg.Provider.AdaptiveEWMAAlpha != 0.35 || cfg.Provider.AffinityTTL != 2*time.Hour || cfg.Provider.ResponseOwnershipTTL != 30*24*time.Hour {
		t.Fatalf("unexpected adaptive routing config: %+v", cfg.Provider)
	}
	if cfg.Management.AuthURL != "http://auth:8082" || cfg.Management.Secret != "internal-secret" || cfg.Management.BillingURL != "http://billing:8083" || cfg.Management.BillingSecret != "billing-secret" {
		t.Fatalf("unexpected management config: %+v", cfg.Management)
	}
	if cfg.Modules.Billing.Secret != "billing-usage-secret" {
		t.Fatal("unexpected billing service secret")
	}
	semantic := cfg.Cache.Semantic
	if semantic.TTLSeconds != 600 || semantic.Threshold != 0.97 || semantic.MaxEntries != 25 || semantic.MaxBytes != 4096 || semantic.EmbeddingURL != "http://embedding:8080/v1" || semantic.EmbeddingAPIKey != "embedding-secret" || semantic.EmbeddingModel != "text-embedding" {
		t.Fatalf("unexpected semantic cache config: %+v", semantic)
	}
	endpoint := cfg.Provider.Endpoints[0]
	if endpoint.ModelAliases["fast"] != "upstream-fast" || endpoint.Weight != 3 || len(endpoint.Capabilities) != 1 || endpoint.MaxParallelRequests != 4 || endpoint.QueueCapacity != 8 || endpoint.QueueTimeoutMS != 250 || endpoint.RateLimitRPM != 120 || endpoint.RateLimitTPM != 64000 || !endpoint.Shadow || endpoint.MirrorPercentage != 12.5 || endpoint.MirrorTimeoutMS != 900 {
		t.Fatalf("unexpected routing config: %+v", endpoint)
	}
}

func TestLoadRejectsInvalidGuardrailMonitorConfiguration(t *testing.T) {
	for _, test := range []struct {
		name     string
		capacity string
		ttl      string
	}{
		{name: "zero capacity", capacity: "0", ttl: "60"},
		{name: "excessive capacity", capacity: "10001", ttl: "60"},
		{name: "zero ttl", capacity: "100", ttl: "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GUARDRAIL_MONITOR_CAPACITY", test.capacity)
			t.Setenv("GUARDRAIL_MONITOR_TTL_SECONDS", test.ttl)
			if cfg := Load(); cfg.InitErr == nil {
				t.Fatal("invalid guardrail monitor configuration was accepted")
			}
		})
	}
}

func TestLoadRejectsInvalidProviderAdmissionConfiguration(t *testing.T) {
	t.Setenv("PROVIDERS_JSON", `[{"name":"broken","type":"demo","queue_capacity":2,"queue_timeout_ms":100}]`)
	if cfg := Load(); cfg.InitErr == nil {
		t.Fatal("queue without max_parallel_requests was accepted")
	}
	t.Setenv("PROVIDERS_JSON", `[{"name":"broken","type":"demo","max_parallel_requests":1,"queue_capacity":2}]`)
	if cfg := Load(); cfg.InitErr == nil {
		t.Fatal("queue without queue_timeout_ms was accepted")
	}
}

func TestLoadRejectsOutOfRangeProviderRateLimits(t *testing.T) {
	for _, raw := range []string{
		`[{"name":"broken","type":"demo","rate_limit_rpm":10000001}]`,
		`[{"name":"broken","type":"demo","rate_limit_tpm":1000000001}]`,
	} {
		t.Setenv("PROVIDERS_JSON", raw)
		if cfg := Load(); cfg.InitErr == nil {
			t.Fatalf("out-of-range rate limit was accepted: %s", raw)
		}
	}
}

func TestLoadRequiresStableCredentialKeyForPersistentControlPlane(t *testing.T) {
	t.Setenv("PROVIDER_CONTROL_PLANE_POSTGRES_DSN", "postgres://gateway@postgres/gateway")
	t.Setenv("PROVIDER_CREDENTIAL_ENCRYPTION_KEY", "short")
	if cfg := Load(); cfg.InitErr == nil {
		t.Fatal("persistent control plane accepted a short encryption key")
	}
	t.Setenv("PROVIDER_CREDENTIAL_ENCRYPTION_KEY", "stable-key-at-least-16-characters")
	t.Setenv("PROVIDER_CONTROL_PLANE_REFRESH_SECONDS", "3")
	cfg := Load()
	if cfg.InitErr != nil || cfg.Provider.ControlPlaneDSN == "" || cfg.Provider.ControlPlaneRefresh != 3*time.Second {
		t.Fatalf("persistent control plane config was not loaded: %+v err=%v", cfg.Provider, cfg.InitErr)
	}
}

func TestLoadRejectsInvalidMirrorConfiguration(t *testing.T) {
	for _, raw := range []string{`[{"name":"broken","type":"demo","shadow":true,"max_parallel_requests":1,"mirror_percentage":101}]`, `[{"name":"broken","type":"demo","shadow":true,"max_parallel_requests":1,"mirror_timeout_ms":-1}]`, `[{"name":"broken","type":"demo","shadow":true}]`} {
		t.Setenv("PROVIDERS_JSON", raw)
		if cfg := Load(); cfg.InitErr == nil {
			t.Fatalf("invalid mirror config accepted: %s", raw)
		}
	}
}

func TestLoadModelCatalog(t *testing.T) {
	t.Setenv("MODEL_CATALOG_JSON", `{"version":"v1","unknown_model_policy":"deny","models":[{"provider":"demo","model":"model","capabilities":["chat"]}]}`)
	cfg := Load()
	if cfg.InitErr != nil || cfg.Catalog.Version != "v1" || !cfg.Catalog.DenyUnknownModels() {
		t.Fatalf("model catalog was not loaded: catalog=%+v err=%v", cfg.Catalog, cfg.InitErr)
	}
	t.Setenv("MODEL_CATALOG_JSON", `{"models":[{"provider":"demo","model":"model"}]}`)
	if cfg := Load(); cfg.InitErr == nil {
		t.Fatal("invalid model catalog did not fail configuration")
	}
}

func TestLoadRejectsIncompleteSemanticCacheConfiguration(t *testing.T) {
	t.Setenv("SEMANTIC_CACHE_TTL_SECONDS", "60")
	t.Setenv("SEMANTIC_CACHE_EMBEDDING_URL", "")
	t.Setenv("SEMANTIC_CACHE_EMBEDDING_MODEL", "")
	if cfg := Load(); cfg.InitErr == nil {
		t.Fatal("enabled semantic cache without embedder was accepted")
	}
	t.Setenv("SEMANTIC_CACHE_EMBEDDING_URL", "http://embedding/v1")
	t.Setenv("SEMANTIC_CACHE_EMBEDDING_MODEL", "embed-model")
	t.Setenv("SEMANTIC_CACHE_THRESHOLD", "1.1")
	if cfg := Load(); cfg.InitErr == nil {
		t.Fatal("invalid semantic threshold was accepted")
	}
}

func TestLoadTelemetryConfiguration(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "gateway-test")
	t.Setenv("AI_GATEWAY_VERSION", "v1.2.3")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector:4318/v1/traces")
	t.Setenv("OTEL_TRACE_SAMPLE_RATIO", "0.25")
	cfg := Load()
	if cfg.Telemetry.ServiceName != "gateway-test" || cfg.Telemetry.Version != "v1.2.3" ||
		cfg.Telemetry.Endpoint != "http://collector:4318/v1/traces" || cfg.Telemetry.SampleRatio != 0.25 {
		t.Fatalf("unexpected telemetry config: %+v", cfg.Telemetry)
	}
}

func TestLoadAPIDocsConfiguration(t *testing.T) {
	if cfg := Load(); cfg.APIDocs.Enabled || cfg.APIDocs.TryItOutEnabled {
		t.Fatalf("API docs must be disabled by default: %+v", cfg.APIDocs)
	}
	t.Setenv("API_DOCS_ENABLED", "true")
	t.Setenv("API_DOCS_TRY_IT_OUT_ENABLED", "true")
	if cfg := Load(); !cfg.APIDocs.Enabled || !cfg.APIDocs.TryItOutEnabled {
		t.Fatalf("API docs environment was not loaded: %+v", cfg.APIDocs)
	}
}

func TestLoadAdminUIConfiguration(t *testing.T) {
	if cfg := Load(); !cfg.AdminUI.Enabled {
		t.Fatal("admin UI should be enabled by default")
	}
	t.Setenv("ADMIN_UI_ENABLED", "false")
	if cfg := Load(); cfg.AdminUI.Enabled {
		t.Fatal("admin UI environment override was ignored")
	}
}
