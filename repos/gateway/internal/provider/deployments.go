package provider

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync/atomic"

	"ai-gateway-gateway/internal/config"
)

type ModelDeployment struct {
	ID              string   `json:"id"`
	ProviderID      string   `json:"provider_id"`
	CredentialID    string   `json:"credential_id,omitempty"`
	ProviderType    string   `json:"provider_type"`
	UpstreamModel   string   `json:"upstream_model,omitempty"`
	Models          []string `json:"models"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Priority        int      `json:"priority"`
	Weight          int      `json:"weight"`
	GuardrailPolicy string   `json:"guardrail_policy,omitempty"`
	Enabled         bool     `json:"enabled"`
	RuntimeState    string   `json:"runtime_state"`
	LatencyEWMAms   float64  `json:"latency_ewma_ms,omitempty"`
	FailureEWMA     float64  `json:"failure_ewma,omitempty"`
}

type DeploymentController interface {
	ListModelDeployments(context.Context) []ModelDeployment
	CreateModelDeployment(ModelDeployment) (ModelDeployment, error)
	UpdateModelDeployment(string, ModelDeployment) (ModelDeployment, error)
	DeleteModelDeployment(string) error
}

var ErrDeploymentNotFound = errors.New("model deployment not found")
var ErrDeploymentExists = errors.New("model deployment already exists")
var ErrInvalidDeployment = errors.New("invalid model deployment")

type deploymentRegistry struct {
	current atomic.Pointer[map[string]ModelDeployment]
}

func (r *Router) ListModelDeployments(ctx context.Context) []ModelDeployment {
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
	if deployment.CredentialID == "" {
		deployment.CredentialID = existing.CredentialID
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
	return deployment, nil
}

func (r *Router) CreateModelDeployment(deployment ModelDeployment) (ModelDeployment, error) {
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
	return deployment, nil
}

func (r *Router) DeleteModelDeployment(id string) error {
	if r == nil || r.deployments == nil {
		return ErrDeploymentNotFound
	}
	id = strings.TrimSpace(id)
	current := r.deployments.current.Load()
	if _, found := (*current)[id]; !found {
		return ErrDeploymentNotFound
	}
	next := make(map[string]ModelDeployment, len(*current)-1)
	for key, value := range *current {
		if key != id {
			next[key] = value
		}
	}
	r.deployments.current.Store(&next)
	r.removeRuntimeEndpoint(id)
	return nil
}

func (r Router) runtimeEndpoints() []Endpoint {
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
				endpoint.AVEnabled = policy.AV
			}
		}
		result = append(result, endpoint)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Priority < result[j].Priority })
	return result
}

func (r *Router) validateDeployment(deployment ModelDeployment) error {
	if strings.TrimSpace(deployment.ID) == "" || len(deployment.ID) > 128 || strings.TrimSpace(deployment.ProviderID) == "" || len(deployment.ProviderID) > 128 || len(deployment.CredentialID) > 128 || len(deployment.UpstreamModel) > 256 || deployment.Priority < 0 || deployment.Weight < 0 || len(deployment.Models) == 0 || len(deployment.Models) > 128 || !validDeploymentStrings(deployment.Models) || !validDeploymentStrings(deployment.Capabilities) || len(deployment.GuardrailPolicy) > 128 {
		return ErrInvalidDeployment
	}
	if deployment.CredentialID != "" {
		if _, err := r.credentialSecret(deployment.CredentialID); err != nil {
			return ErrInvalidDeployment
		}
	}
	return nil
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
	secret, err := r.credentialSecret(deployment.CredentialID)
	if err != nil {
		return Endpoint{}, err
	}
	client := providerFor(config.ProviderEndpointConfig{Type: managed.Type, BaseURL: managed.BaseURL, APIKey: secret})
	if client == nil {
		return Endpoint{}, ErrInvalidDeployment
	}
	aliases := map[string]string{}
	if deployment.UpstreamModel != "" {
		for _, model := range deployment.Models {
			aliases[model] = deployment.UpstreamModel
		}
	}
	return Endpoint{Name: deployment.ID, Type: managed.Type, Models: append([]string(nil), deployment.Models...), Capabilities: append([]string(nil), deployment.Capabilities...), Priority: deployment.Priority, Weight: deployment.Weight, GuardrailPolicy: deployment.GuardrailPolicy, GuardrailPolicyValid: true, ModelAliases: aliases, Provider: client, Admission: newAdmissionController(0, 0, 0)}, nil
}

func (r *Router) configuredEndpoints() []Endpoint {
	if r == nil {
		return nil
	}
	if current := r.endpointState.Load(); current != nil {
		return append([]Endpoint(nil), (*current)...)
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
	r.endpointState.Store(&next)
}

func (r *Router) removeRuntimeEndpoint(id string) {
	current := r.configuredEndpoints()
	next := make([]Endpoint, 0, len(current))
	for _, item := range current {
		if item.Name != id {
			next = append(next, item)
		}
	}
	r.endpointState.Store(&next)
}

func validDeploymentStrings(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return false
		}
	}
	return true
}
