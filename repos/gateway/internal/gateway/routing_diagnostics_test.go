package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/provider"
)

type diagnosticsProvider struct{ modelsProvider }

func (diagnosticsProvider) Diagnostics(context.Context) provider.RoutingDiagnostics {
	return provider.RoutingDiagnostics{Strategy: "adaptive", Endpoints: []provider.EndpointDiagnostics{{Name: "safe-endpoint", Type: "ollama", State: "available", Models: []string{"m1"}}}}
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
