package provider

import (
	"context"
	"strings"
	"sync/atomic"

	"ai-gateway-gateway/internal/openai"
)

type RoutingSimulationRequest struct {
	Model           string   `json:"model"`
	Provider        string   `json:"provider,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
}

type RoutingSimulationCandidate struct {
	DeploymentID string         `json:"deployment_id"`
	ProviderID   string         `json:"provider_id"`
	ProviderType string         `json:"provider_type"`
	Priority     int            `json:"priority"`
	Weight       int            `json:"weight"`
	RetryPolicy  map[string]int `json:"retry_policy,omitempty"`
	Order        int            `json:"order"`
}

type RoutingSimulation struct {
	Strategy   string                       `json:"strategy"`
	Selected   string                       `json:"selected,omitempty"`
	Reason     string                       `json:"reason"`
	Candidates []RoutingSimulationCandidate `json:"candidates"`
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
	endpoints := simulation.candidates(ctx, request, input.Capabilities...)
	result := RoutingSimulation{Strategy: strategy, Reason: "no eligible healthy deployment", Candidates: []RoutingSimulationCandidate{}}
	for index, endpoint := range endpoints {
		result.Candidates = append(result.Candidates, RoutingSimulationCandidate{DeploymentID: endpoint.Name, ProviderID: endpoint.ProviderID, ProviderType: endpoint.Type, Priority: endpoint.Priority, Weight: endpoint.Weight, RetryPolicy: cloneRetryPolicy(endpoint.RetryPolicy), Order: index + 1})
	}
	if len(result.Candidates) > 0 {
		result.Selected = result.Candidates[0].DeploymentID
		result.Reason = "eligible healthy deployments ordered by priority, then configured routing strategy"
	}
	return result
}
