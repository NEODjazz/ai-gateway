package provider

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
)

type ManagedProvider struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	BaseURL string `json:"base_url,omitempty"`
	Enabled bool   `json:"enabled"`
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
	next := cloneProviders(*current)
	next[id] = provider
	r.providers.current.Store(&next)
	if deployments := r.deployments.current.Load(); deployments != nil {
		for _, deployment := range *deployments {
			if deployment.ProviderID == id {
				if endpoint, buildErr := r.endpointForDeployment(deployment); buildErr == nil {
					r.replaceRuntimeEndpoint(deployment.ID, endpoint)
				}
			}
		}
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
	return r.persistControlMutation(context.Background(), previous)
}

func normalizeManagedProvider(input ManagedProvider) (ManagedProvider, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if input.ID == "" || len(input.ID) > 128 || !validProviderType(input.Type) || len(input.BaseURL) > 2048 {
		return ManagedProvider{}, ErrInvalidProvider
	}
	if input.Type != "demo" {
		parsed, err := url.ParseRequestURI(input.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return ManagedProvider{}, ErrInvalidProvider
		}
	}
	return input, nil
}

func validProviderType(value string) bool {
	switch value {
	case "demo", "ollama", "openai", "openai-compatible", "anthropic", "gemini", "cohere":
		return true
	default:
		return false
	}
}

func cloneProviders(current map[string]ManagedProvider) map[string]ManagedProvider {
	next := make(map[string]ManagedProvider, len(current))
	for key, value := range current {
		next[key] = value
	}
	return next
}
