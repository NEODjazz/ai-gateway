package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/provider"
)

func TestAIHubAndCostRecommendations(t *testing.T) {
	catalog, _ := modelcatalog.Parse(`{"version":"test","models":[{"provider":"demo-a","model":"m1","input_cost_per_1m":2,"output_cost_per_1m":2,"currency":"USD"},{"provider":"cheap-a","model":"m1","input_cost_per_1m":1,"output_cost_per_1m":1,"currency":"USD"},{"provider":"demo-a","model":"m2"}]}`)
	registry := modelcatalog.NewRegistry(catalog, nil, 0)
	enabled := true
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "demo-a", Type: "demo", Models: []string{"m1", "m2"}, Enabled: &enabled}, {Name: "cheap-a", Type: "demo", Models: []string{"m1"}, Enabled: &enabled}}})
	handler := NewHandler(modulesPipeline("admin"), runtime).WithModelRegistry(registry)
	hub := httptest.NewRecorder()
	Routes(handler).ServeHTTP(hub, httptest.NewRequest(http.MethodGet, "/admin/v1/ai-hub/models", nil))
	if hub.Code != http.StatusOK || strings.Contains(hub.Body.String(), "base_url") || !strings.Contains(hub.Body.String(), `"available":true`) {
		t.Fatalf("unsafe hub: status=%d body=%s", hub.Code, hub.Body.String())
	}
	recommendations := httptest.NewRecorder()
	Routes(handler).ServeHTTP(recommendations, httptest.NewRequest(http.MethodGet, "/admin/v1/cost-optimization/recommendations", nil))
	body := recommendations.Body.String()
	if recommendations.Code != http.StatusOK || !strings.Contains(body, `"type":"cheaper_alternative"`) || !strings.Contains(body, `"recommended_provider":"cheap-a"`) || !strings.Contains(body, `"usage_projection":false`) {
		t.Fatalf("recommendations status=%d body=%s", recommendations.Code, body)
	}
}
