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
