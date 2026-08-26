package gateway

import (
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
