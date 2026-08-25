package provider

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
)

type ModelDeployment struct {
	ID              string   `json:"id"`
	ProviderID      string   `json:"provider_id"`
	ProviderType    string   `json:"provider_type"`
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
	UpdateModelDeployment(string, ModelDeployment) (ModelDeployment, error)
}

var ErrDeploymentNotFound = errors.New("model deployment not found")
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
			for _, endpoint := range r.endpoints {
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
	if deployment.Priority < 0 || deployment.Weight < 0 || len(deployment.Models) > 128 || !validDeploymentStrings(deployment.Models) || !validDeploymentStrings(deployment.Capabilities) || len(deployment.GuardrailPolicy) > 128 {
		return ModelDeployment{}, ErrInvalidDeployment
	}
	deployment.ID = id
	deployment.ProviderType = existing.ProviderType
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
	return deployment, nil
}

func (r Router) runtimeEndpoints() []Endpoint {
	if r.deployments == nil {
		return r.endpoints
	}
	current := r.deployments.current.Load()
	if current == nil {
		return r.endpoints
	}
	result := make([]Endpoint, 0, len(r.endpoints))
	for _, endpoint := range r.endpoints {
		deployment, found := (*current)[endpoint.Name]
		if found && !deployment.Enabled {
			continue
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

func validDeploymentStrings(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return false
		}
	}
	return true
}
