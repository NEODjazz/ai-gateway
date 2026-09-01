package gateway

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

type fallbackResolverProvider struct {
	modelsProvider
	targets []string
}

func (p fallbackResolverProvider) ModelFallbackTargets(context.Context, string) []string {
	return append([]string(nil), p.targets...)
}

func TestPrepareModelFallbacksIntersectsKeyAccessGroupAndTagModelGrants(t *testing.T) {
	registry := NewAccessRegistry()
	if _, err := registry.PutTag("regulated", TagDefinition{AllowedModels: []string{"primary", "allowed-fallback"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(modules.NewPipeline(nil), fallbackResolverProvider{targets: []string{"allowed-fallback", "key-denied", "group-denied", "tag-denied"}}).WithAccessRegistry(registry)
	req := modules.RequestContext{AllowedModels: []string{"primary", "allowed-fallback", "group-denied", "tag-denied"}, AccessGroupsEvaluated: true, AccessGroupModels: []string{"primary", "allowed-fallback", "tag-denied"}, Tags: []string{"regulated"}}
	if !handler.prepareModelFallbacks(httptest.NewRecorder(), t.Context(), &req, "primary") {
		t.Fatal("fallback policy preparation failed")
	}
	if !req.FallbackPolicyEvaluated || !reflect.DeepEqual(req.AllowedFallbackModels, []string{"allowed-fallback"}) {
		t.Fatalf("fallback grants were not intersected: %+v", req.AllowedFallbackModels)
	}
}
