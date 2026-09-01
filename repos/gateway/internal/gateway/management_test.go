package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

type managementAuthModule struct {
	roles []string
	err   error
}

func (m managementAuthModule) Name() string   { return "auth" }
func (m managementAuthModule) Required() bool { return true }
func (m managementAuthModule) Handle(_ context.Context, req *modules.RequestContext) error {
	if m.err != nil {
		return m.err
	}
	req.UserID = "admin-user"
	req.CredentialID = "admin-credential"
	req.Roles = append([]string(nil), m.roles...)
	req.APIKey = ""
	return nil
}

type recordingManagementClient struct {
	audit    ManagementAudit
	spec     ManagedVirtualKey
	id       string
	creates  int
	rotates  int
	revokes  int
	updates  int
	disabled *bool
	filter   VirtualKeyListFilter
	page     *VirtualKeyPage
}

func (c *recordingManagementClient) ListVirtualKeys(_ context.Context, audit ManagementAudit, _ int) ([]VirtualKeyMetadata, error) {
	c.audit = audit
	return []VirtualKeyMetadata{{ID: "vk_safe123", UserID: "user-1", RotationFamily: "vk_safe123"}}, nil
}
func (c *recordingManagementClient) ListVirtualKeysPage(_ context.Context, audit ManagementAudit, filter VirtualKeyListFilter) (VirtualKeyPage, error) {
	c.audit, c.filter = audit, filter
	if c.page != nil {
		return *c.page, nil
	}
	return VirtualKeyPage{Data: []VirtualKeyMetadata{{ID: "vk_safe123", UserID: "user-1", RotationFamily: "vk_safe123"}}, Total: 27, Limit: filter.Limit, Offset: filter.Offset}, nil
}

func (c *recordingManagementClient) CreateVirtualKey(_ context.Context, audit ManagementAudit, spec ManagedVirtualKey) (IssuedVirtualKey, error) {
	c.audit, c.spec, c.creates = audit, spec, c.creates+1
	return IssuedVirtualKey{ID: "vk_created", Token: "sk-ag-once"}, nil
}
func (c *recordingManagementClient) RotateVirtualKey(_ context.Context, audit ManagementAudit, id string, spec ManagedVirtualKey) (IssuedVirtualKey, error) {
	c.audit, c.id, c.spec, c.rotates = audit, id, spec, c.rotates+1
	return IssuedVirtualKey{ID: "vk_rotated", Token: "sk-ag-rotated"}, nil
}
func (c *recordingManagementClient) RevokeVirtualKey(_ context.Context, audit ManagementAudit, id string) error {
	c.audit, c.id, c.revokes = audit, id, c.revokes+1
	return nil
}
func (c *recordingManagementClient) UpdateVirtualKey(_ context.Context, audit ManagementAudit, id string, spec ManagedVirtualKey) error {
	c.audit, c.id, c.spec, c.updates = audit, id, spec, c.updates+1
	return nil
}
func (c *recordingManagementClient) SetVirtualKeyDisabled(_ context.Context, audit ManagementAudit, id string, disabled bool) error {
	c.audit, c.id, c.disabled = audit, id, &disabled
	return nil
}

func TestAdminVirtualKeyUpdateAndDisable(t *testing.T) {
	client := &recordingManagementClient{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithManagement(client)
	update := httptest.NewRequest(http.MethodPut, "/admin/v1/keys/vk_safe123", strings.NewReader(`{"alias":"ci","description":"automation","tags":["prod"],"user_id":"user-1"}`))
	update.Header.Set("X-Request-ID", "req-update")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, update)
	if response.Code != http.StatusNoContent || client.updates != 1 || client.spec.Alias != "ci" || len(client.spec.Tags) != 1 {
		t.Fatalf("update failed: status=%d client=%+v", response.Code, client)
	}
	disable := httptest.NewRequest(http.MethodPost, "/admin/v1/keys/vk_safe123/disable", nil)
	response = httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, disable)
	if response.Code != http.StatusNoContent || client.disabled == nil || !*client.disabled {
		t.Fatalf("disable failed: status=%d client=%+v", response.Code, client)
	}
}

func TestAdminVirtualKeyAPIRequiresAdminRole(t *testing.T) {
	client := &recordingManagementClient{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"developer"}}}), modelsProvider{}).WithManagement(client)
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"user_id":"user-1"}`))
	request.Header.Set("Authorization", "Bearer client-secret")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || client.creates != 0 {
		t.Fatalf("non-admin reached management service: status=%d calls=%d", response.Code, client.creates)
	}
}

func TestAdminVirtualKeyCreateReturnsOneTimeTokenAndAuditIdentity(t *testing.T) {
	client := &recordingManagementClient{}
	registry := NewAccessRegistry()
	if _, err := registry.PutGroup("platform", AccessGroup{Name: "Platform", AllowedModels: []string{"gpt-*"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithManagement(client).WithAccessRegistry(registry)
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"user_id":"user-1","team_id":"team-1","access_group_ids":["platform"],"allowed_models":["gpt-*"]}`))
	request.Header.Set("Authorization", "Bearer client-secret")
	request.Header.Set("X-Request-ID", "req-admin-1")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	var issued IssuedVirtualKey
	if err := json.NewDecoder(response.Body).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	if issued.Token != "sk-ag-once" || client.spec.UserID != "user-1" || client.spec.TeamID != "team-1" || len(client.spec.AccessGroupIDs) != 1 || client.spec.AccessGroupIDs[0] != "platform" {
		t.Fatalf("unexpected issued key/spec: issued=%+v spec=%+v", issued, client.spec)
	}
	if client.audit.RequestID != "req-admin-1" || client.audit.ActorID != "admin-user" || client.audit.CredentialID != "admin-credential" {
		t.Fatalf("missing audit identity: %+v", client.audit)
	}
}

func TestAdminVirtualKeyMutationsRejectUnavailableAccessGroup(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: "/admin/v1/keys"},
		{name: "update", method: http.MethodPut, path: "/admin/v1/keys/vk_safe123"},
		{name: "rotate", method: http.MethodPost, path: "/admin/v1/keys/vk_safe123/rotate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &recordingManagementClient{}
			handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithManagement(client).WithAccessRegistry(NewAccessRegistry())
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(`{"organization_id":"org-1","access_group_ids":["missing"]}`))
			response := httptest.NewRecorder()
			Routes(handler).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || client.creates+client.updates+client.rotates != 0 || !strings.Contains(response.Body.String(), "must reference enabled access groups") {
				t.Fatalf("unavailable access group was accepted: status=%d client=%+v body=%s", response.Code, client, response.Body.String())
			}
		})
	}
}

func TestAdminVirtualKeyCreateAllowsOrganizationOwnerWithoutUser(t *testing.T) {
	client := &recordingManagementClient{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithManagement(client)
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"organization_id":"org-1","allowed_models":["gpt-*"]}`))
	request.Header.Set("Authorization", "Bearer client-secret")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || client.spec.OrganizationID != "org-1" || client.spec.UserID != "" || client.spec.TeamID != "" {
		t.Fatalf("organization owner was not forwarded: status=%d spec=%+v body=%s", response.Code, client.spec, response.Body.String())
	}
}

func TestAdminVirtualKeyListReturnsSafeMetadata(t *testing.T) {
	client := &recordingManagementClient{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithManagement(client)
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/keys?limit=25&offset=25&search=prod&organization_id=org-1&team_id=team-1&user_id=user-1&key_id=safe&access_group_id=platform&status=non_revoked&sort_by=alias&sort_order=asc", nil)
	request.Header.Set("Authorization", "Bearer admin-key")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"vk_safe123"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if client.filter.Offset != 25 || client.filter.Search != "prod" || client.filter.OrganizationID != "org-1" || client.filter.AccessGroupID != "platform" || client.filter.Status != "non_revoked" || client.filter.SortBy != "alias" || client.filter.SortOrder != "asc" || !strings.Contains(response.Body.String(), `"total":27`) {
		t.Fatalf("list filter was not propagated: filter=%+v body=%s", client.filter, response.Body.String())
	}
	for _, forbidden := range []string{"token_hash", `"token"`} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("list exposed secret field %q: %s", forbidden, response.Body.String())
		}
	}
	invalid := httptest.NewRequest(http.MethodGet, "/admin/v1/keys?sort_by=token_hash", nil)
	invalidResponse := httptest.NewRecorder()
	Routes(handler).ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid sort was accepted: %d", invalidResponse.Code)
	}
	invalidGroup := httptest.NewRequest(http.MethodGet, "/admin/v1/keys?access_group_id=bad/group", nil)
	invalidGroupResponse := httptest.NewRecorder()
	Routes(handler).ServeHTTP(invalidGroupResponse, invalidGroup)
	if invalidGroupResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid access group filter was accepted: %d", invalidGroupResponse.Code)
	}
}

func TestAdminVirtualKeyListExpandsBudgetFinancialsForReturnedPage(t *testing.T) {
	keys := &recordingManagementClient{}
	budgets := &recordingBudgetClient{}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithManagement(keys).WithBudgetManagement(budgets)
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/keys?limit=25&expand=financials", nil)
	request.Header.Set("X-Request-ID", "req-financials")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(budgets.subjects) != 1 || budgets.subjects[0].KeyID != "vk_safe123" || budgets.subjects[0].UserID != "user-1" || !strings.Contains(response.Body.String(), `"financials":{"vk_safe123"`) {
		t.Fatalf("status=%d subjects=%+v body=%s", response.Code, budgets.subjects, response.Body.String())
	}
	if budgets.audit.RequestID != "req-financials" {
		t.Fatalf("audit=%+v", budgets.audit)
	}
	invalid := httptest.NewRecorder()
	Routes(handler).ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/admin/v1/keys?expand=secrets", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid expand status=%d", invalid.Code)
	}
}

func TestAdminVirtualKeyAPIRejectsUnknownFieldsAndInvalidID(t *testing.T) {
	client := &recordingManagementClient{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithManagement(client)
	invalidBody := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"user_id":"user-1","token":"must-not-be-accepted"}`))
	invalidBodyResponse := httptest.NewRecorder()
	Routes(handler).ServeHTTP(invalidBodyResponse, invalidBody)
	if invalidBodyResponse.Code != http.StatusBadRequest || client.creates != 0 {
		t.Fatalf("unknown field accepted: status=%d calls=%d", invalidBodyResponse.Code, client.creates)
	}
	invalidID := httptest.NewRequest(http.MethodDelete, "/admin/v1/keys/bad", nil)
	invalidIDResponse := httptest.NewRecorder()
	Routes(handler).ServeHTTP(invalidIDResponse, invalidID)
	if invalidIDResponse.Code != http.StatusBadRequest || client.revokes != 0 {
		t.Fatalf("invalid path reached client: status=%d calls=%d", invalidIDResponse.Code, client.revokes)
	}
}

func TestAdminVirtualKeyAPIMapsUnauthorized(t *testing.T) {
	client := &recordingManagementClient{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{err: modules.ErrUnauthorized}}), modelsProvider{}).WithManagement(client)
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"user_id":"user-1"}`))
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || client.creates != 0 || !strings.Contains(response.Body.String(), "unauthorized") {
		t.Fatalf("unexpected unauthorized response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRemoteManagementClientUsesScopedSecretNotClientBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("client bearer was forwarded to auth management")
		}
		if r.Header.Get("X-Management-Token") != "internal-secret" || r.Header.Get("X-Actor-ID") != "admin-user" || r.Header.Get("X-Actor-Credential-ID") != "fingerprint" {
			t.Fatalf("unexpected management headers: %+v", r.Header)
		}
		var spec ManagedVirtualKey
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(IssuedVirtualKey{ID: "vk_created", Token: "sk-ag-once"})
	}))
	defer server.Close()
	client := NewRemoteManagementClient(server.URL, "internal-secret")
	issued, err := client.CreateVirtualKey(context.Background(), ManagementAudit{RequestID: "req-1", ActorID: "admin-user", CredentialID: "fingerprint"}, ManagedVirtualKey{UserID: "user-1"})
	if err != nil || issued.ID != "vk_created" {
		t.Fatalf("unexpected remote result: issued=%+v err=%v", issued, err)
	}
}

func TestRemoteManagementClientListsMetadataWithoutRequestBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("limit") != "25" || r.URL.Query().Get("offset") != "50" || r.URL.Query().Get("search") != "prod" || r.URL.Query().Get("organization_id") != "org-1" || r.URL.Query().Get("team_id") != "team-1" || r.URL.Query().Get("user_id") != "user-1" || r.URL.Query().Get("key_id") != "safe" || r.URL.Query().Get("access_group_id") != "platform" || r.URL.Query().Get("status") != "non_revoked" || r.URL.Query().Get("sort_by") != "alias" || r.URL.Query().Get("sort_order") != "asc" || r.ContentLength > 0 {
			t.Fatalf("unexpected list request: method=%s url=%s content_length=%d", r.Method, r.URL.String(), r.ContentLength)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Token") != "internal-secret" {
			t.Fatalf("unsafe management headers: %+v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(VirtualKeyPage{Data: []VirtualKeyMetadata{{ID: "vk_safe123", UserID: "user-1", RotationFamily: "vk_safe123"}}, Total: 73, Limit: 25, Offset: 50})
	}))
	defer server.Close()
	client := NewRemoteManagementClient(server.URL, "internal-secret")
	page, err := client.ListVirtualKeysPage(context.Background(), ManagementAudit{RequestID: "req-list", ActorID: "admin-user", CredentialID: "fingerprint"}, VirtualKeyListFilter{Limit: 25, Offset: 50, Search: "prod", OrganizationID: "org-1", TeamID: "team-1", UserID: "user-1", KeyID: "safe", AccessGroupID: "platform", Status: "non_revoked", SortBy: "alias", SortOrder: "asc"})
	if err != nil || len(page.Data) != 1 || page.Data[0].ID != "vk_safe123" || page.Total != 73 {
		t.Fatalf("unexpected page=%+v err=%v", page, err)
	}
}
