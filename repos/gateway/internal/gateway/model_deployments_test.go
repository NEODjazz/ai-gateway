package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/provider"
)

func TestAdminModelDeploymentLifecycle(t *testing.T) {
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "safe-endpoint", Type: "demo", Models: []string{"m1"}}}})
	handler := NewHandler(modulesPipeline("admin"), runtime)
	list := httptest.NewRecorder()
	Routes(handler).ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/model-deployments", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "base_url") || strings.Contains(list.Body.String(), "api_key") {
		t.Fatalf("unsafe deployment list: status=%d body=%s", list.Code, list.Body.String())
	}
	update := httptest.NewRecorder()
	Routes(handler).ServeHTTP(update, httptest.NewRequest(http.MethodPut, "/admin/v1/model-deployments/safe-endpoint", strings.NewReader(`{"models":["m2"],"capabilities":["chat"],"priority":2,"weight":3,"enabled":true}`)))
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"models":["m2"]`) {
		t.Fatalf("update failed: status=%d body=%s", update.Code, update.Body.String())
	}
}

func TestAdminCreatesAndDeletesRoutableModelDeployment(t *testing.T) {
	runtime := provider.New(provider.Config{})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))
	createProvider := httptest.NewRecorder()
	handler.ServeHTTP(createProvider, httptest.NewRequest(http.MethodPost, "/admin/v1/providers", strings.NewReader(`{"id":"managed-demo","type":"demo","enabled":true}`)))
	if createProvider.Code != http.StatusCreated {
		t.Fatalf("provider create failed: %d %s", createProvider.Code, createProvider.Body.String())
	}
	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/admin/v1/model-deployments", strings.NewReader(`{"id":"managed-deployment","provider_id":"managed-demo","models":["managed-model"],"capabilities":["chat"],"priority":1,"weight":2,"enabled":true}`)))
	if create.Code != http.StatusCreated || !strings.Contains(create.Body.String(), `"provider_id":"managed-demo"`) {
		t.Fatalf("deployment create failed: %d %s", create.Code, create.Body.String())
	}
	models := httptest.NewRecorder()
	handler.ServeHTTP(models, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if models.Code != http.StatusOK || !strings.Contains(models.Body.String(), "managed-model") {
		t.Fatalf("new deployment is not routable: %d %s", models.Code, models.Body.String())
	}
	remove := httptest.NewRecorder()
	handler.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/admin/v1/model-deployments/managed-deployment", nil))
	if remove.Code != http.StatusNoContent {
		t.Fatalf("deployment delete failed: %d %s", remove.Code, remove.Body.String())
	}
}
