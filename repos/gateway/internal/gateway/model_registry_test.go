package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modelcatalog"
)

func TestAdminCanReplaceRuntimeModelCatalog(t *testing.T) {
	initial, _ := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	registry := modelcatalog.NewRegistry(initial, nil, time.Second)
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithModelRegistry(registry)
	request := httptest.NewRequest(http.MethodPut, "/admin/v1/model-catalog", strings.NewReader(`{"version":"v2","unknown_model_policy":"deny","models":[{"provider":"ollama","model":"qwen","capabilities":["chat"],"input_cost_per_1m":1,"currency":"USD"}]}`))
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || registry.Current(request.Context()).Version != "v2" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRuntimeModelCatalogRequiresAdminAndValidPricing(t *testing.T) {
	initial, _ := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	registry := modelcatalog.NewRegistry(initial, nil, time.Second)
	for _, tc := range []struct {
		role, body string
		status     int
	}{
		{"developer", `{"version":"v2","models":[]}`, http.StatusForbidden},
		{"admin", `{"version":"v2","models":[{"provider":"p","model":"m","input_cost_per_1m":1}]}`, http.StatusBadRequest},
	} {
		handler := NewHandler(modulesPipeline(tc.role), modelsProvider{}).WithModelRegistry(registry)
		response := httptest.NewRecorder()
		Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/admin/v1/model-catalog", strings.NewReader(tc.body)))
		if response.Code != tc.status {
			t.Fatalf("role=%s status=%d body=%s", tc.role, response.Code, response.Body.String())
		}
	}
}
