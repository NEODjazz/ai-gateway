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
	audit   ManagementAudit
	spec    ManagedVirtualKey
	id      string
	creates int
	rotates int
	revokes int
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
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithManagement(client)
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"user_id":"user-1","team_id":"team-1","allowed_models":["gpt-*"]}`))
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
	if issued.Token != "sk-ag-once" || client.spec.UserID != "user-1" || client.spec.TeamID != "team-1" {
		t.Fatalf("unexpected issued key/spec: issued=%+v spec=%+v", issued, client.spec)
	}
	if client.audit.RequestID != "req-admin-1" || client.audit.ActorID != "admin-user" || client.audit.CredentialID != "admin-credential" {
		t.Fatalf("missing audit identity: %+v", client.audit)
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
