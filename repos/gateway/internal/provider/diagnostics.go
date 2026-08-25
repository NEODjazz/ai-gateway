package provider

import (
	"context"
	"time"
)

type EndpointDiagnostics struct {
	Name                 string     `json:"name"`
	Type                 string     `json:"type"`
	Models               []string   `json:"models"`
	Capabilities         []string   `json:"capabilities,omitempty"`
	Priority             int        `json:"priority"`
	Weight               int        `json:"weight"`
	State                string     `json:"state"`
	CooldownUntil        *time.Time `json:"cooldown_until,omitempty"`
	ConsecutiveFailures  int        `json:"consecutive_failures"`
	Samples              int        `json:"samples"`
	LatencyEWMAms        float64    `json:"latency_ewma_ms"`
	FailureEWMA          float64    `json:"failure_ewma"`
	MaxParallelRequests  int        `json:"max_parallel_requests,omitempty"`
	InFlightRequests     int        `json:"in_flight_requests"`
	QueueCapacity        int64      `json:"queue_capacity,omitempty"`
	QueuedRequests       int64      `json:"queued_requests"`
	MaxRetries           int        `json:"max_retries"`
	GuardrailPolicy      string     `json:"guardrail_policy,omitempty"`
	GuardrailPolicyValid bool       `json:"guardrail_policy_valid"`
	DLPEnabled           bool       `json:"dlp_enabled"`
	AVEnabled            bool       `json:"av_enabled"`
	Shadow               bool       `json:"shadow"`
	MirrorPercentage     float64    `json:"mirror_percentage,omitempty"`
}

type RoutingDiagnostics struct {
	Strategy  string                `json:"strategy"`
	Endpoints []EndpointDiagnostics `json:"endpoints"`
}

type DiagnosticsProvider interface {
	Diagnostics(context.Context) RoutingDiagnostics
}

func (r Router) Diagnostics(ctx context.Context) RoutingDiagnostics {
	strategy := r.routingStrategy
	if strategy == "" {
		strategy = "priority_weighted"
	}
	result := RoutingDiagnostics{Strategy: strategy, Endpoints: make([]EndpointDiagnostics, 0, len(r.endpoints))}
	for _, endpoint := range r.endpoints {
		available := r.health.available(ctx, endpoint)
		health := r.health.localState(endpoint.Name)
		adaptive := r.adaptive.snapshot(endpoint.Name)
		state := "available"
		now := time.Now()
		if r.health != nil && r.health.now != nil {
			now = r.health.now()
		}
		if !available {
			state = "cooling_down"
		} else if !health.probeUntil.IsZero() && now.Before(health.probeUntil) {
			state = "half_open"
		}
		var cooldownUntil *time.Time
		if !health.cooldownUntil.IsZero() && now.Before(health.cooldownUntil) {
			value := health.cooldownUntil.UTC()
			cooldownUntil = &value
		}
		diagnostic := EndpointDiagnostics{
			Name: endpoint.Name, Type: endpoint.Type, Models: append([]string{}, endpoint.Models...), Capabilities: append([]string(nil), endpoint.Capabilities...),
			Priority: endpoint.Priority, Weight: endpoint.Weight, State: state, CooldownUntil: cooldownUntil, ConsecutiveFailures: health.failures,
			Samples: adaptive.samples, LatencyEWMAms: adaptive.latencyEWMA, FailureEWMA: adaptive.failureEWMA,
			MaxRetries: endpoint.MaxRetries, GuardrailPolicy: endpoint.GuardrailPolicy, GuardrailPolicyValid: endpoint.GuardrailPolicyValid,
			DLPEnabled: endpoint.DLPEnabled, AVEnabled: endpoint.AVEnabled, Shadow: endpoint.Shadow, MirrorPercentage: endpoint.MirrorPercentage,
		}
		if endpoint.Admission != nil {
			diagnostic.MaxParallelRequests = cap(endpoint.Admission.slots)
			diagnostic.InFlightRequests = len(endpoint.Admission.slots)
			diagnostic.QueueCapacity = endpoint.Admission.queueCapacity
			diagnostic.QueuedRequests = endpoint.Admission.queued.Load()
		}
		result.Endpoints = append(result.Endpoints, diagnostic)
	}
	return result
}
