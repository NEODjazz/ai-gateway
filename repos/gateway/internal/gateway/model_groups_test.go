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

func TestAdminModelGroupLifecyclePublishesPublicModel(t *testing.T) {
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "primary", Type: "demo", Models: []string{"upstream-a"}, Priority: 1, Weight: 3},
		{Name: "fallback", Type: "demo", Models: []string{"upstream-b"}, Priority: 2, Weight: 1},
	}})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))
	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/admin/v1/model-groups", strings.NewReader(`{"id":"public-chat","deployment_ids":["primary","fallback"],"strategy":"weighted","retry_policy":{"rate_limit":2},"enabled":true}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("group create failed: %d %s", create.Code, create.Body.String())
	}
	if !strings.Contains(create.Body.String(), `"deployment_ids":["primary","fallback"]`) || !strings.Contains(create.Body.String(), `"retry_policy":{"rate_limit":2}`) {
		t.Fatalf("ordered membership or retry policy missing from response: %s", create.Body.String())
	}
	models := httptest.NewRecorder()
	handler.ServeHTTP(models, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if models.Code != http.StatusOK || !strings.Contains(models.Body.String(), "public-chat") {
		t.Fatalf("group model is not published: %d %s", models.Code, models.Body.String())
	}
	blockedDelete := httptest.NewRecorder()
	handler.ServeHTTP(blockedDelete, httptest.NewRequest(http.MethodDelete, "/admin/v1/model-deployments/primary", nil))
	if blockedDelete.Code != http.StatusConflict {
		t.Fatalf("referenced deployment delete was not blocked: %d %s", blockedDelete.Code, blockedDelete.Body.String())
	}
	update := httptest.NewRecorder()
	handler.ServeHTTP(update, httptest.NewRequest(http.MethodPut, "/admin/v1/model-groups/public-chat", strings.NewReader(`{"deployment_ids":["fallback","primary"],"strategy":"adaptive","enabled":true}`)))
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"deployment_ids":["fallback","primary"]`) || !strings.Contains(update.Body.String(), `"strategy":"adaptive"`) {
		t.Fatalf("group update failed: %d %s", update.Code, update.Body.String())
	}
	remove := httptest.NewRecorder()
	handler.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/admin/v1/model-groups/public-chat", nil))
	if remove.Code != http.StatusNoContent {
		t.Fatalf("group delete failed: %d %s", remove.Code, remove.Body.String())
	}
}

func TestAdminModelGroupRejectsUnknownDeployment(t *testing.T) {
	runtime := provider.New(provider.Config{})
	response := httptest.NewRecorder()
	Routes(NewHandler(modulesPipeline("admin"), runtime)).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/admin/v1/model-groups", strings.NewReader(`{"id":"invalid","deployment_ids":["missing"],"strategy":"weighted","enabled":true}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown deployment accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestAdminModelGroupRoutingSettingsAreReadAndUpdatedAtomically(t *testing.T) {
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "primary", Type: "demo", Models: []string{"public"}, Priority: 0, Weight: 2},
		{Name: "fallback", Type: "demo", Models: []string{"public"}, Priority: 1, Weight: 1},
	}})
	controller := runtime.(provider.ModelGroupController)
	if _, err := controller.CreateModelGroup(provider.ModelGroup{ID: "public", DeploymentIDs: []string{"primary", "fallback"}, Strategy: "weighted", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/admin/v1/model-groups/public/routing-settings", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("routing settings get failed: %d %s", get.Code, get.Body.String())
	}
	var settings provider.ModelGroupRoutingSettings
	if err := json.Unmarshal(get.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	updateBody := `{"expected_revision":0,"deployment_ids":["fallback","primary"],"strategy":"adaptive","retry_policy":{"rate_limit":2},"enabled":true,"deployments":[{"id":"fallback","priority":0,"weight":3,"request_timeout_ms":2000},{"id":"primary","priority":1,"weight":1,"max_retries":2}]}`
	update := httptest.NewRecorder()
	handler.ServeHTTP(update, httptest.NewRequest(http.MethodPut, "/admin/v1/model-groups/public/routing-settings", strings.NewReader(updateBody)))
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"deployment_ids":["fallback","primary"]`) || !strings.Contains(update.Body.String(), `"strategy":"adaptive"`) {
		t.Fatalf("routing settings update failed: %d %s", update.Code, update.Body.String())
	}
	missingRevision := httptest.NewRecorder()
	handler.ServeHTTP(missingRevision, httptest.NewRequest(http.MethodPut, "/admin/v1/model-groups/public/routing-settings", strings.NewReader(`{"deployment_ids":["primary"],"strategy":"weighted","enabled":true,"deployments":[]}`)))
	if missingRevision.Code != http.StatusBadRequest {
		t.Fatalf("missing revision accepted: %d %s", missingRevision.Code, missingRevision.Body.String())
	}
	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, httptest.NewRequest(http.MethodPut, "/admin/v1/model-groups/public/routing-settings", strings.NewReader(strings.Replace(updateBody, `"expected_revision":0`, `"expected_revision":1`, 1))))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "revision_conflict") {
		t.Fatalf("stale routing update accepted: %d %s", stale.Code, stale.Body.String())
	}
}
