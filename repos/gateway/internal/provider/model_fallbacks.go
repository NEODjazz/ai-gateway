package provider

import (
	"context"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
	"sync/atomic"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func (r Router) routeCandidates(ctx context.Context, req modules.RequestContext, request openai.ChatCompletionRequest, capabilities ...string) []Endpoint {
	primary := r.candidates(ctx, request, capabilities...)
	for index := range primary {
		primary[index].RoutingModel = request.Model
	}
	group, found := r.modelGroup(request.Model)
	if !found || len(group.Fallbacks) == 0 {
		return primary
	}
	result := append([]Endpoint(nil), primary...)
	stage := 0
	for _, fallbackType := range fallbackTypes {
		for _, target := range group.Fallbacks[fallbackType] {
			if !fallbackTargetAllowed(req, target) {
				continue
			}
			fallbackRequest := request
			fallbackRequest.Model = target
			fallbackCandidates := r.candidatesWithCounter(ctx, fallbackRequest, fallbackRouteCounter(req.RequestID, target, stage+1), capabilities...)
			if len(fallbackCandidates) == 0 {
				continue
			}
			stage++
			for index := range fallbackCandidates {
				fallbackCandidates[index].RoutingModel = target
				fallbackCandidates[index].FallbackType = fallbackType
				fallbackCandidates[index].FallbackStage = stage
			}
			result = append(result, fallbackCandidates...)
		}
	}
	return result
}

func fallbackRouteCounter(requestID, target string, stage int) *atomic.Uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(requestID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(target))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.Itoa(stage)))
	counter := &atomic.Uint64{}
	counter.Store(hash.Sum64())
	return counter
}

func fallbackTargetAllowed(req modules.RequestContext, target string) bool {
	if !req.FallbackPolicyEvaluated {
		return true
	}
	for _, allowed := range req.AllowedFallbackModels {
		if allowed == target {
			return true
		}
	}
	return false
}

type routeProgress struct {
	stage              int
	activeFallbackType string
	lastFailure        error
	initialFailure     error
}

func newRouteProgress(candidates []Endpoint) routeProgress {
	for _, candidate := range candidates {
		if candidate.FallbackStage == 0 {
			return routeProgress{}
		}
	}
	err := &Error{Class: FailureUnavailable, Provider: "router", Err: errors.New("no eligible healthy deployment for primary model")}
	return routeProgress{lastFailure: err, initialFailure: err}
}

func (p routeProgress) allows(candidate Endpoint) bool {
	if candidate.FallbackStage == p.stage {
		return p.lastFailure == nil || tryNextEndpoint(p.lastFailure)
	}
	if p.lastFailure == nil {
		return false
	}
	if p.activeFallbackType != "" && candidate.FallbackType == p.activeFallbackType {
		return fallbackChainContinues(p.activeFallbackType, p.lastFailure)
	}
	return fallbackTypeForFailure(p.lastFailure) == candidate.FallbackType
}

func (p *routeProgress) enter(candidate Endpoint) {
	if candidate.FallbackStage == p.stage {
		return
	}
	p.stage = candidate.FallbackStage
	p.activeFallbackType = candidate.FallbackType
	p.lastFailure = nil
}

func (p *routeProgress) fail(err error) {
	p.lastFailure = err
}

func (p routeProgress) hasNext(candidates []Endpoint) bool {
	for _, candidate := range candidates {
		if p.allows(candidate) {
			return true
		}
	}
	return false
}

func fallbackChainContinues(fallbackType string, err error) bool {
	trigger := fallbackTypeForFailure(err)
	return trigger == fallbackType || (fallbackType != "" && trigger == FallbackGeneral)
}

func fallbackTypeForFailure(err error) string {
	switch failureClass(err) {
	case FailureContextLength:
		return FallbackContextWindow
	case FailureContentPolicy:
		return FallbackContentPolicy
	case FailureRateLimit, FailureTimeout, FailureUnavailable, FailureUnknown:
		return FallbackGeneral
	default:
		return ""
	}
}

func endpointRoutingModel(endpoint Endpoint, original string) string {
	if model := strings.TrimSpace(endpoint.RoutingModel); model != "" {
		return model
	}
	return original
}
