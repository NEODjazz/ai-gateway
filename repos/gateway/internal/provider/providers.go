package provider

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"ai-gateway-gateway/internal/config"
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
	Type         string   `json:"type"`
	Operations   []string `json:"operations"`
	Capabilities []string `json:"capabilities"`
}

var managedProviderTypes = []string{"demo", "ollama", "openai", "openai-compatible", "openrouter", "azure-openai", "anthropic", "gemini", "cohere", "mistral", "voyage", "bedrock", "groq", "deepseek"}

var managedOperationCapabilities = []string{
	"chat", "responses", "embeddings", "rerank", "moderation",
	"image_generation", "image_edit", "image_variation",
	"audio_transcription", "audio_translation", "audio_speech", "ocr", "search", "stream",
}

var managedFeatureCapabilities = []string{
	"tools", "structured_output", "mcp", "vision", "web_search",
	"web_fetch", "audio", "prompt_cache", "assistant_prefill",
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
		capabilities := append([]string(nil), operations...)
		for _, capability := range managedFeatureCapabilities {
			if supportsManagedAdapterCapability(endpoint, capability) {
				capabilities = append(capabilities, capability)
			}
		}
		profiles = append(profiles, ProviderCapabilityProfile{Type: providerType, Operations: operations, Capabilities: capabilities})
	}
	return profiles
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
