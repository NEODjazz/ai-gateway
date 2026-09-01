package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/provider"
)

func TestAdminPlansAndAtomicallyAppliesModelOnboarding(t *testing.T) {
	initial, _ := modelcatalog.Parse(`{"version":"before","models":[]}`)
	registry := modelcatalog.NewRegistry(initial, nil, time.Second)
	runtime := provider.New(provider.Config{CatalogRegistry: registry})
	controller := runtime.(provider.ProviderController)
	if _, err := controller.CreateProvider(provider.ManagedProvider{ID: "managed", Type: "demo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime).WithModelRegistry(registry))
	body := `{"catalog":{"version":"onboard-v1","models":[{"provider":"managed","model":"public","capabilities":["chat"]}]},"deployments":[{"id":"managed-public","provider_id":"managed","upstream_model":"upstream","models":["public"],"capabilities":["chat"],"weight":1,"enabled":true}],"model_groups":[{"id":"public","deployment_ids":["managed-public"],"strategy":"weighted","enabled":true}]}`
	planResponse := httptest.NewRecorder()
	handler.ServeHTTP(planResponse, httptest.NewRequest(http.MethodPost, "/admin/v1/model-onboarding/plan", strings.NewReader(body)))
	if planResponse.Code != http.StatusOK {
		t.Fatalf("plan status=%d body=%s", planResponse.Code, planResponse.Body.String())
	}
	if registry.Current(t.Context()).Version != "before" {
		t.Fatal("plan mutated the runtime catalog")
	}
	var plan provider.ModelOnboardingPlan
	if err := json.Unmarshal(planResponse.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	applyBody := strings.Replace(body, `{"catalog"`, `{"expected_revision":`+jsonNumber(plan.Revision)+`,"catalog"`, 1)
	applyResponse := httptest.NewRecorder()
	handler.ServeHTTP(applyResponse, httptest.NewRequest(http.MethodPost, "/admin/v1/model-onboarding/apply", strings.NewReader(applyBody)))
	if applyResponse.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", applyResponse.Code, applyResponse.Body.String())
	}
	if registry.Current(t.Context()).Version != "onboard-v1" {
		t.Fatal("apply did not update the runtime catalog")
	}
	if deployments := runtime.(provider.DeploymentController).ListModelDeployments(t.Context()); len(deployments) != 1 || deployments[0].ID != "managed-public" {
		t.Fatalf("unexpected deployments: %+v", deployments)
	}
	if groups := runtime.(provider.ModelGroupController).ListModelGroups(t.Context()); len(groups) != 1 || groups[0].ID != "public" {
		t.Fatalf("unexpected groups: %+v", groups)
	}
}

func TestModelOnboardingApplyRequiresPlanRevision(t *testing.T) {
	initial, _ := modelcatalog.Parse(`{"version":"before","models":[]}`)
	registry := modelcatalog.NewRegistry(initial, nil, time.Second)
	runtime := provider.New(provider.Config{CatalogRegistry: registry})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime).WithModelRegistry(registry))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/admin/v1/model-onboarding/apply", strings.NewReader(`{"catalog":{"version":"v2","models":[]},"deployments":[]}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "expected_revision") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func jsonNumber(value int64) string {
	payload, _ := json.Marshal(value)
	return string(payload)
}
