package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/provider"
)

type diagnosticsProvider struct{ modelsProvider }

func (diagnosticsProvider) Diagnostics(context.Context) provider.RoutingDiagnostics {
	return provider.RoutingDiagnostics{Strategy: "adaptive", Endpoints: []provider.EndpointDiagnostics{{Name: "safe-endpoint", Type: "ollama", State: "available", Models: []string{"m1"}}}}
}

func TestAdminRoutingSimulationReturnsEligibleOrder(t *testing.T) {
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "demo-route", Type: "demo", Models: []string{"model"}}}})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/admin/v1/routing/simulate", strings.NewReader(`{"model":"model","capabilities":["chat"]}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"selected":"demo-route"`) || !strings.Contains(response.Body.String(), `"order":1`) {
		t.Fatalf("unexpected simulation: %d %s", response.Code, response.Body.String())
	}
}

func TestAdminRoutingSimulationRejectsUnknownFailureClass(t *testing.T) {
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "demo-route", Type: "demo", Models: []string{"model"}}}})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/admin/v1/routing/simulate", strings.NewReader(`{"model":"model","failure_class":"secret_failure"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown failure class accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestAdminRoutingDiagnosticsReturnsSafeProjection(t *testing.T) {
	handler := NewHandler(modulesPipeline("admin"), diagnosticsProvider{})
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/routing/diagnostics", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"safe-endpoint"`) || !strings.Contains(response.Body.String(), `"strategy":"adaptive"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"base_url", "api_key", "authorization", "secret"} {
		if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
			t.Fatalf("diagnostics exposed %q: %s", forbidden, response.Body.String())
		}
	}
}
