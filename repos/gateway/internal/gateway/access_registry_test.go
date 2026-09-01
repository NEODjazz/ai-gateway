package gateway

import (
	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessRegistryProjectsAndPermissionTemplates(t *testing.T) {
	registry := NewAccessRegistry()
	project, err := registry.PutProject("payments", Project{Name: "Payments", TeamID: "team-payments", Tags: []string{"production", "production"}, Enabled: true})
	if err != nil || len(project.Tags) != 1 {
		t.Fatalf("unexpected project: %#v err=%v", project, err)
	}
	group, err := registry.PutGroup("payments-read", AccessGroup{
		Name: "Payments read", ProjectID: "payments", AllowedModels: []string{"gpt-*", "gpt-*"}, AllowedTools: []string{"toolset:ledger"}, Tags: []string{"read-only"}, Enabled: true,
	})
	if err != nil || len(group.AllowedModels) != 1 {
		t.Fatalf("unexpected access group: %#v err=%v", group, err)
	}
	if _, err := registry.PutProject("payments", Project{Name: "Payments", Enabled: false}); !errors.Is(err, errAccessEntryInUse) {
		t.Fatalf("referenced project was disabled: %v", err)
	}
	if err := registry.DeleteProject("payments"); !errors.Is(err, errAccessEntryInUse) {
		t.Fatalf("project deletion should be protected, got %v", err)
	}
	if err := registry.DeleteGroup("payments-read"); err != nil {
		t.Fatal(err)
	}
	if err := registry.DeleteProject("payments"); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyAttachmentMatchingRequiresEveryConfiguredDimension(t *testing.T) {
	registry := NewAccessRegistry()
	_, err := registry.PutPolicyAttachment("healthcare", PolicyAttachment{
		PolicyName: "strict", Scope: "specific", Teams: []string{"care-*"}, Keys: []string{"clinical-*"}, Models: []string{"gpt-5.*"}, Tags: []string{"hipaa"},
	})
	if err != nil {
		t.Fatal(err)
	}
	matched := registry.MatchingPolicyAttachments(PolicyMatchContext{TeamID: "care-a", CredentialID: "vk-1", CredentialAlias: "clinical-prod", Model: "gpt-5.6", Tags: []string{"hipaa"}})
	if len(matched) != 1 || matched[0].PolicyName != "strict" {
		t.Fatalf("expected attachment match, got %+v", matched)
	}
	if got := registry.MatchingPolicyAttachments(PolicyMatchContext{TeamID: "care-a", CredentialAlias: "clinical-prod", Model: "gpt-5.6", Tags: []string{"public"}}); len(got) != 0 {
		t.Fatalf("attachment matched without the required tag: %+v", got)
	}
	if _, err := registry.PutPolicyAttachment("invalid-global", PolicyAttachment{PolicyName: "strict", Scope: "*", Teams: []string{"care-a"}}); err == nil {
		t.Fatal("global attachment with specific selectors was accepted")
	}
}

func TestTagDefinitionsIntersectModelGrants(t *testing.T) {
	registry := NewAccessRegistry()
	tag, err := registry.PutTag("environment:prod", TagDefinition{Description: "Production traffic", AllowedModels: []string{"gpt-*", "gpt-*"}, Enabled: true})
	if err != nil || len(tag.AllowedModels) != 1 {
		t.Fatalf("unexpected tag: %+v err=%v", tag, err)
	}
	_, _ = registry.PutTag("regulated", TagDefinition{AllowedModels: []string{"gpt-5.*"}, Enabled: true})
	if allowed, _ := registry.TagModelAllowed([]string{"environment:prod", "regulated"}, "gpt-5.6"); !allowed {
		t.Fatal("intersection rejected a model allowed by every tag")
	}
	if allowed, name := registry.TagModelAllowed([]string{"environment:prod", "regulated"}, "gpt-4.1"); allowed || name != "regulated" {
		t.Fatalf("intersection allowed a restricted model: allowed=%v tag=%q", allowed, name)
	}
	if allowed, _ := registry.TagModelAllowed([]string{"legacy-unregistered"}, "any-model"); !allowed {
		t.Fatal("unregistered metadata tag broke backward compatibility")
	}
	_, _ = registry.PutTag("disabled", TagDefinition{Enabled: false})
	if allowed, name := registry.TagModelAllowed([]string{"disabled"}, "gpt-5.6"); allowed || name != "disabled" {
		t.Fatalf("disabled tag did not fail closed: allowed=%v tag=%q", allowed, name)
	}
}

func TestTagManagementAPIAndInferenceEnforcement(t *testing.T) {
	registry := NewAccessRegistry()
	audit := &recordingAuditClient{}
	adminRouter := Routes(NewHandler(modulesPipeline("admin"), &chatProvider{}).WithAccessRegistry(registry).WithAudit(audit))

	put := httptest.NewRecorder()
	adminRouter.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/admin/v1/tags/regulated", strings.NewReader(`{"description":"Approved models","allowed_models":["gpt-*"],"enabled":true}`)))
	if put.Code != http.StatusOK || !strings.Contains(put.Body.String(), `"name":"regulated"`) {
		t.Fatalf("put tag status=%d body=%s", put.Code, put.Body.String())
	}
	list := httptest.NewRecorder()
	adminRouter.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/tags", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"allowed_models":["gpt-*"]`) {
		t.Fatalf("list tags status=%d body=%s", list.Code, list.Body.String())
	}
	inferenceRouter := Routes(NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tags: []string{"regulated"}}}), &chatProvider{}).WithAccessRegistry(registry))
	blocked := httptest.NewRecorder()
	inferenceRouter.ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude","messages":[{"role":"user","content":"hello"}]}`)))
	if blocked.Code != http.StatusForbidden || !strings.Contains(blocked.Body.String(), `"code":"tag_model_not_allowed"`) || strings.Contains(blocked.Body.String(), "regulated") {
		t.Fatalf("tag policy was not safely enforced: status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	allowed := httptest.NewRecorder()
	inferenceRouter.ServeHTTP(allowed, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}]}`)))
	if allowed.Code != http.StatusOK {
		t.Fatalf("allowed tagged request status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	remove := httptest.NewRecorder()
	adminRouter.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/admin/v1/tags/regulated", nil))
	if remove.Code != http.StatusNoContent || len(registry.Tags()) != 0 {
		t.Fatalf("delete tag status=%d body=%s", remove.Code, remove.Body.String())
	}
}

func TestTagRestrictionsFilterModelDiscovery(t *testing.T) {
	registry := NewAccessRegistry()
	_, _ = registry.PutTag("regulated", TagDefinition{AllowedModels: []string{"gpt-*"}, Enabled: true})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tags: []string{"regulated"}}}), modelsProvider{}).WithAccessRegistry(registry))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"data":[]`) {
		t.Fatalf("tag-restricted model was discoverable: status=%d body=%s", response.Code, response.Body.String())
	}
	_, _ = registry.PutTag("regulated", TagDefinition{AllowedModels: []string{"test-*"}, Enabled: true})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"test-model"`) {
		t.Fatalf("allowed tagged model was hidden: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPolicyAttachmentsAdminAPIAndRequestEvaluation(t *testing.T) {
	runtime := provider.New(provider.Config{GuardrailPolicies: map[string]config.GuardrailPolicyConfig{"strict": {DLP: true}}})
	registry := NewAccessRegistry()
	audit := &recordingAuditClient{}
	handler := NewHandler(modulesPipeline("admin"), runtime).WithAccessRegistry(registry).WithAudit(audit)
	router := Routes(handler)

	put := httptest.NewRecorder()
	router.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/admin/v1/policy-attachments/clinical", strings.NewReader(`{"policy_name":"strict","scope":"specific","teams":["care-*"],"models":["gpt-5.*"]}`)))
	if put.Code != http.StatusOK {
		t.Fatalf("put attachment status=%d body=%s", put.Code, put.Body.String())
	}

	req := modules.RequestContext{TeamID: "care-a"}
	if !handler.applyPolicyAttachments(httptest.NewRecorder(), &req, "gpt-5.6") {
		t.Fatal("matching policy attachment was rejected")
	}
	if req.Metadata["policy.guardrail.required"] != "true" || req.Metadata["policy.modules.dlp.enabled"] != "true" || req.Metadata["policy.guardrail.names"] != "strict" {
		t.Fatalf("policy was not materialized into request metadata: %+v", req.Metadata)
	}

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/policy-attachments", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"policy_name":"strict"`) {
		t.Fatalf("list attachments status=%d body=%s", list.Code, list.Body.String())
	}

	remove := httptest.NewRecorder()
	router.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/admin/v1/policy-attachments/clinical", nil))
	if remove.Code != http.StatusNoContent || len(registry.PolicyAttachments()) != 0 {
		t.Fatalf("delete attachment status=%d body=%s", remove.Code, remove.Body.String())
	}
}

func TestModelAuthorizationPrecedesPolicyAttachmentResolution(t *testing.T) {
	registry := NewAccessRegistry()
	if _, err := registry.PutPolicyAttachment("global", PolicyAttachment{PolicyName: "missing", Scope: "*"}); err != nil {
		t.Fatal(err)
	}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"allowed"}}}), &chatProvider{}).WithAccessRegistry(registry))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"blocked","messages":[{"role":"user","content":"hello"}]}`)))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"model_not_allowed"`) {
		t.Fatalf("policy resolution changed authorization semantics: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAccessRegistryRejectsMissingOrDisabledProject(t *testing.T) {
	registry := NewAccessRegistry()
	if _, err := registry.PutGroup("orphan", AccessGroup{Name: "Orphan", ProjectID: "missing", Enabled: true}); err == nil {
		t.Fatal("group with missing project was accepted")
	}
	_, _ = registry.PutProject("disabled", Project{Name: "Disabled", Enabled: false})
	if _, err := registry.PutGroup("disabled-group", AccessGroup{Name: "Disabled", ProjectID: "disabled", Enabled: true}); err == nil {
		t.Fatal("group for disabled project was accepted")
	}
}

func TestAccessRegistryAdminAPI(t *testing.T) {
	audit := &recordingAuditClient{}
	handler := NewHandler(modulesPipeline("admin"), nil).WithAccessRegistry(NewAccessRegistry()).WithAudit(audit)
	router := Routes(handler)

	putProject := httptest.NewRecorder()
	router.ServeHTTP(putProject, httptest.NewRequest(http.MethodPut, "/admin/v1/projects/payments", strings.NewReader(`{"name":"Payments","team_id":"team-a","tags":["prod"],"enabled":true}`)))
	if putProject.Code != http.StatusOK {
		t.Fatalf("put project status=%d body=%s", putProject.Code, putProject.Body.String())
	}

	putGroup := httptest.NewRecorder()
	router.ServeHTTP(putGroup, httptest.NewRequest(http.MethodPut, "/admin/v1/access-groups/payments-read", strings.NewReader(`{"name":"Payments read","project_id":"payments","allowed_models":["gpt-*"],"allowed_tools":["toolset:ledger"],"enabled":true}`)))
	if putGroup.Code != http.StatusOK {
		t.Fatalf("put group status=%d body=%s", putGroup.Code, putGroup.Body.String())
	}
	if len(audit.events) != 4 || audit.events[3].Action != "access_group.update" || audit.events[3].TargetID != "payments-read" || audit.events[3].Outcome != "succeeded" {
		t.Fatalf("unexpected audit events: %+v", audit.events)
	}

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/access-groups", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"allowed_models":["gpt-*"]`) || strings.Contains(strings.ToLower(list.Body.String()), "secret") {
		t.Fatalf("unsafe access group response: status=%d body=%s", list.Code, list.Body.String())
	}

	deleteProject := httptest.NewRecorder()
	router.ServeHTTP(deleteProject, httptest.NewRequest(http.MethodDelete, "/admin/v1/projects/payments", nil))
	if deleteProject.Code != http.StatusConflict {
		t.Fatalf("expected conflict, got status=%d body=%s", deleteProject.Code, deleteProject.Body.String())
	}
}
