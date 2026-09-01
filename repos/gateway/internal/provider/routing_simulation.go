package provider

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type RoutingSimulationRequest struct {
	Model           string   `json:"model"`
	Provider        string   `json:"provider,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	FailureClass    string   `json:"failure_class,omitempty"`
}

type RoutingSimulationCandidate struct {
	DeploymentID string         `json:"deployment_id"`
	ProviderID   string         `json:"provider_id"`
	ProviderType string         `json:"provider_type"`
	Priority     int            `json:"priority"`
	Weight       int            `json:"weight"`
	RetryPolicy  map[string]int `json:"retry_policy,omitempty"`
	Model        string         `json:"model"`
	FallbackType string         `json:"fallback_type,omitempty"`
	Stage        int            `json:"stage"`
	Order        int            `json:"order"`
}

type RoutingSimulation struct {
	Strategy     string                       `json:"strategy"`
	Selected     string                       `json:"selected,omitempty"`
	Reason       string                       `json:"reason"`
	FailureClass string                       `json:"failure_class,omitempty"`
	FallbackType string                       `json:"fallback_type,omitempty"`
	Candidates   []RoutingSimulationCandidate `json:"candidates"`
}

type RoutingSimulationController interface {
	SimulateRouting(context.Context, RoutingSimulationRequest) RoutingSimulation
}

func (r Router) SimulateRouting(ctx context.Context, input RoutingSimulationRequest) RoutingSimulation {
	strategy := r.routingStrategy
	if strategy == "" {
		strategy = "priority_weighted"
	}
	request := openai.ChatCompletionRequest{Model: strings.TrimSpace(input.Model), Provider: strings.TrimSpace(input.Provider)}
	if input.MaxOutputTokens > 0 {
		request.MaxCompletionTokens = &input.MaxOutputTokens
	}
	simulation := r
	simulation.routeCounter = &atomic.Uint64{}
	if r.routeCounter != nil {
		simulation.routeCounter.Store(r.routeCounter.Load())
	}
	if group, found := r.modelGroup(request.Model); found {
		strategy = group.Strategy
	}
	endpoints := simulation.routeCandidates(ctx, modules.RequestContext{}, request, input.Capabilities...)
	result := RoutingSimulation{Strategy: strategy, FailureClass: strings.TrimSpace(input.FailureClass), Reason: "no eligible healthy deployment", Candidates: []RoutingSimulationCandidate{}}
	for index, endpoint := range endpoints {
		result.Candidates = append(result.Candidates, RoutingSimulationCandidate{DeploymentID: endpoint.Name, ProviderID: endpoint.ProviderID, ProviderType: endpoint.Type, Priority: endpoint.Priority, Weight: endpoint.Weight, RetryPolicy: cloneRetryPolicy(endpoint.RetryPolicy), Model: endpointRoutingModel(endpoint, request.Model), FallbackType: endpoint.FallbackType, Stage: endpoint.FallbackStage, Order: index + 1})
	}
	if result.FailureClass == "" {
		for _, candidate := range result.Candidates {
			if candidate.Stage == 0 {
				result.Selected = candidate.DeploymentID
				result.Reason = "eligible healthy deployments ordered by priority, then configured routing strategy"
				break
			}
		}
		return result
	}
	failure := &Error{Class: FailureClass(result.FailureClass), Provider: "simulation", Err: errors.New("simulated provider failure")}
	result.FallbackType = fallbackTypeForFailure(failure)
	if result.FallbackType == "" {
		result.Reason = "failure class is terminal and does not activate model fallback"
		return result
	}
	for _, candidate := range result.Candidates {
		if candidate.FallbackType == result.FallbackType {
			result.Selected = candidate.DeploymentID
			result.Reason = "simulated failure activates the configured " + result.FallbackType + " fallback chain"
			return result
		}
	}
	result.Reason = "no eligible deployment in the configured " + result.FallbackType + " fallback chain"
	return result
}
