package gateway

import (
	"context"
	"net/http"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

func (h Handler) prepareModelFallbacks(w http.ResponseWriter, ctx context.Context, req *modules.RequestContext, model string) bool {
	req.FallbackPolicyEvaluated = true
	resolver, ok := h.provider.(provider.ModelFallbackResolver)
	if !ok {
		return h.applyPolicyAttachments(w, req, model)
	}
	targets := resolver.ModelFallbackTargets(ctx, model)
	for _, target := range targets {
		if !modelAllowed(target, req.AllowedModels) {
			continue
		}
		if h.access != nil {
			if allowed, _ := h.access.TagModelAllowed(req.Tags, target); !allowed {
				continue
			}
		}
		req.AllowedFallbackModels = append(req.AllowedFallbackModels, target)
	}
	models := append([]string{model}, req.AllowedFallbackModels...)
	return h.applyPolicyAttachmentsForModels(w, req, models)
}
