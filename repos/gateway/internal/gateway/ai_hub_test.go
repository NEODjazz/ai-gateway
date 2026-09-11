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
	catalog, _ := modelcatalog.Parse(`{"version":"test","models":[{"provider":"demo-a","model":"m1","input_cost_per_1m":2,"output_cost_per_1m":2,"training_cost_per_1m":5,"search_cost_per_1k":10,"currency":"USD"},{"provider":"cheap-a","model":"m1","input_cost_per_1m":1,"output_cost_per_1m":1,"currency":"USD"},{"provider":"demo-a","model":"m2"}]}`)
	registry := modelcatalog.NewRegistry(catalog, nil, 0)
	enabled := true
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "demo-a", Type: "demo", Models: []string{"m1", "m2"}, Enabled: &enabled}, {Name: "cheap-a", Type: "demo", Models: []string{"m1"}, Enabled: &enabled}}})
	handler := NewHandler(modulesPipeline("admin"), runtime).WithModelRegistry(registry)
	hub := httptest.NewRecorder()
	Routes(handler).ServeHTTP(hub, httptest.NewRequest(http.MethodGet, "/admin/v1/ai-hub/models", nil))
	if hub.Code != http.StatusOK || strings.Contains(hub.Body.String(), "base_url") || !strings.Contains(hub.Body.String(), `"available":true`) || !strings.Contains(hub.Body.String(), `"training_cost_per_1m":5`) || !strings.Contains(hub.Body.String(), `"search_cost_per_1k":10`) {
		t.Fatalf("unsafe hub: status=%d body=%s", hub.Code, hub.Body.String())
	}
	recommendations := httptest.NewRecorder()
	Routes(handler).ServeHTTP(recommendations, httptest.NewRequest(http.MethodGet, "/admin/v1/cost-optimization/recommendations", nil))
	body := recommendations.Body.String()
	if recommendations.Code != http.StatusOK || !strings.Contains(body, `"type":"cheaper_alternative"`) || !strings.Contains(body, `"recommended_provider":"cheap-a"`) || !strings.Contains(body, `"usage_projection":false`) {
		t.Fatalf("recommendations status=%d body=%s", recommendations.Code, body)
	}
}

func TestAIHubJoinsCatalogByManagedProviderID(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"test","models":[{"provider":"azure-open-ai","model":"gpt-5.6-luna","input_cost_per_1m":0.2,"output_cost_per_1m":20,"currency":"USD"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	runtime := provider.New(provider.Config{})
	providerController := runtime.(provider.ProviderController)
	if _, err := providerController.CreateProvider(provider.ManagedProvider{ID: "azure-open-ai", Type: "demo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	deploymentController := runtime.(provider.DeploymentController)
	if _, err := deploymentController.CreateModelDeployment(provider.ModelDeployment{ID: "luna-deployment", ProviderID: "azure-open-ai", Models: []string{"gpt-5.6-luna"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(modulesPipeline("admin"), runtime).WithModelRegistry(modelcatalog.NewRegistry(catalog, nil, 0))
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/ai-hub/models", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"provider":"azure-open-ai"`) || !strings.Contains(body, `"deployments":["luna-deployment"]`) || !strings.Contains(body, `"available":true`) {
		t.Fatalf("managed provider catalog was not joined: status=%d body=%s", response.Code, body)
	}
}
