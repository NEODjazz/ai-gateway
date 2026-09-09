package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/provider"
)

func TestAdminTestsDeploymentAndReturnsHealthHistory(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"must not escape","code":"invalid_api_key"}}`))
	}))
	defer upstream.Close()
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "remote", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"model"}}}})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))

	probe := httptest.NewRecorder()
	handler.ServeHTTP(probe, httptest.NewRequest(http.MethodPost, "/admin/v1/model-deployments/remote/test", nil))
	if probe.Code != http.StatusOK || strings.Contains(probe.Body.String(), "must not escape") {
		t.Fatalf("unsafe deployment probe: %d %s", probe.Code, probe.Body.String())
	}
	var check provider.DeploymentHealthCheck
	if err := json.Unmarshal(probe.Body.Bytes(), &check); err != nil {
		t.Fatal(err)
	}
	if check.Status != "unavailable" || check.FailureClass != "authentication" || check.HTTPStatus != http.StatusUnauthorized || check.UpstreamCode != "invalid_api_key" {
		t.Fatalf("unexpected check: %+v", check)
	}

	history := httptest.NewRecorder()
	handler.ServeHTTP(history, httptest.NewRequest(http.MethodGet, "/admin/v1/model-deployments/remote/health?limit=10", nil))
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"failure_class":"authentication"`) || strings.Contains(history.Body.String(), "must not escape") {
		t.Fatalf("unexpected history: %d %s", history.Code, history.Body.String())
	}
}

func TestAdminBatchTestsDeploymentsAndListsLatestHealth(t *testing.T) {
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "first", Type: "demo", Models: []string{"m1"}}, {Name: "second", Type: "demo", Models: []string{"m2"}}}})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))
	batch := httptest.NewRecorder()
	handler.ServeHTTP(batch, httptest.NewRequest(http.MethodPost, "/admin/v1/model-deployments/health-checks", strings.NewReader(`{"deployment_ids":["first","second"]}`)))
	if batch.Code != http.StatusOK || !strings.Contains(batch.Body.String(), `"deployment_id":"first"`) || !strings.Contains(batch.Body.String(), `"deployment_id":"second"`) || !strings.Contains(batch.Body.String(), `"errors":[]`) {
		t.Fatalf("unexpected batch checks: %d %s", batch.Code, batch.Body.String())
	}
	latest := httptest.NewRecorder()
	handler.ServeHTTP(latest, httptest.NewRequest(http.MethodGet, "/admin/v1/model-deployments/health?ids=first,second", nil))
	if latest.Code != http.StatusOK || strings.Count(latest.Body.String(), `"status":"available"`) != 2 {
		t.Fatalf("unexpected latest checks: %d %s", latest.Code, latest.Body.String())
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/admin/v1/model-deployments/health?ids=first,missing", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("unknown deployment accepted: %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestAdminModelDeploymentLifecycle(t *testing.T) {
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "safe-endpoint", Type: "demo", Models: []string{"m1"}}}})
	handler := NewHandler(modulesPipeline("admin"), runtime)
	list := httptest.NewRecorder()
	Routes(handler).ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/model-deployments", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "base_url") || strings.Contains(list.Body.String(), "api_key") {
		t.Fatalf("unsafe deployment list: status=%d body=%s", list.Code, list.Body.String())
	}
	for _, capabilities := range []string{`["embedding"]`, `["chat","chat"]`} {
		invalid := httptest.NewRecorder()
		body := `{"models":["m2"],"capabilities":` + capabilities + `,"enabled":true}`
		Routes(handler).ServeHTTP(invalid, httptest.NewRequest(http.MethodPut, "/admin/v1/model-deployments/safe-endpoint", strings.NewReader(body)))
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid deployment capabilities accepted: capabilities=%s status=%d body=%s", capabilities, invalid.Code, invalid.Body.String())
		}
	}
	update := httptest.NewRecorder()
	Routes(handler).ServeHTTP(update, httptest.NewRequest(http.MethodPut, "/admin/v1/model-deployments/safe-endpoint", strings.NewReader(`{"models":["m2"],"capabilities":["chat"],"priority":2,"weight":3,"enabled":true}`)))
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"models":["m2"]`) {
		t.Fatalf("update failed: status=%d body=%s", update.Code, update.Body.String())
	}
}

func TestModelDeploymentServerQueryFiltersSortsAndPages(t *testing.T) {
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "azure-z", Type: "demo", Models: []string{"chat"}, Priority: 2}, {Name: "local-a", Type: "demo", Models: []string{"chat"}, Priority: 1}}})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/model-deployments?model=chat&sort=priority&order=asc&limit=1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"total":2`) || !strings.Contains(response.Body.String(), `"id":"local-a"`) || strings.Contains(response.Body.String(), `"id":"azure-z"`) {
		t.Fatalf("unexpected deployment page: %d %s", response.Code, response.Body.String())
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
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/admin/v1/model-deployments", strings.NewReader(`{"id":"managed-deployment","provider_id":"managed-demo","models":["managed-model"],"capabilities":["chat"],"priority":1,"weight":2,"request_timeout_ms":2500,"max_retries":2,"cooldown_after_failures":3,"cooldown_seconds":30,"max_parallel_requests":4,"queue_capacity":5,"queue_timeout_ms":750,"rate_limit_rpm":120,"rate_limit_tpm":64000,"enabled":true}`)))
	if create.Code != http.StatusCreated || !strings.Contains(create.Body.String(), `"provider_id":"managed-demo"`) || !strings.Contains(create.Body.String(), `"max_retries":2`) || !strings.Contains(create.Body.String(), `"max_parallel_requests":4`) || !strings.Contains(create.Body.String(), `"rate_limit_rpm":120`) || !strings.Contains(create.Body.String(), `"rate_limit_tpm":64000`) {
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
