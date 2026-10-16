package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
)

// Every newly published administration route must be reviewed here. Tenant
// reporting exceptions have separate tests for their enforced storage scope.
func TestOrganizationAdministratorCannotReachPlatformAdministration(t *testing.T) {
	reportRoutes := map[string]bool{
		"/admin/v1/session":      true,
		"/admin/v1/keys":         true,
		"/admin/v1/usage/report": true,
		"/admin/v1/customers/{scope_type}/{scope_id}/usage": true,
		"/admin/v1/request-logs":                            true,
		"/admin/v1/request-logs/groups":                     true,
		"/admin/v1/request-logs/settings":                   true,
		"/admin/v1/request-logs/{request_id}":               true,
	}
	client := NewRemoteManagementClient("http://service.invalid", "fixture")
	handler := Routes(organizationReportHandler("org-a", "org_admin").WithIdentityDirectory(client).WithOrganizations(client))
	for _, route := range DocumentedRoutes() {
		if !strings.HasPrefix(route.Path, "/admin/v1/") || route.Method == "GET" && reportRoutes[route.Path] {
			continue
		}
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			parts := strings.Split(route.Path, "/")
			for i, part := range parts {
				if strings.HasPrefix(part, "{") {
					parts[i] = "foreign"
				}
			}
			r := httptest.NewRequest(route.Method, strings.Join(parts, "/"), strings.NewReader(`{}`))
			// These are untrusted client headers, never service actor metadata.
			r.Header.Set("X-Actor-Roles", "admin")
			r.Header.Set("X-Actor-Organization-ID", "org-b")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("tenant reached platform route: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

type tenantResourceAuth struct{}

func (tenantResourceAuth) Name() string   { return "auth" }
func (tenantResourceAuth) Required() bool { return true }
func (tenantResourceAuth) Handle(_ context.Context, req *modules.RequestContext) error {
	// Separate approved credentials establish the tenant, even for one user.
	if req.APIKey != "tenant-a" && req.APIKey != "tenant-b" {
		return modules.ErrUnauthorized
	}
	req.CredentialID = req.APIKey
	req.UserID = "shared-user"
	req.OrganizationID = "org-" + strings.TrimPrefix(req.APIKey, "tenant-")
	req.Roles = []string{"org_admin"}
	return nil
}

func TestOrganizationResourceCredentialBoundaryCannotBeChangedByHeaders(t *testing.T) {
	store := &memoryFileStore{files: map[string]filestate.File{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{tenantResourceAuth{}}), modelsProvider{}).WithFileStore(store, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}))
	body, contentType := fileUploadBody(t, "user_data", "tenant.txt", []byte("tenant-a-private"))
	r := httptest.NewRequest("POST", "/v1/files", body)
	r.Header.Set("Authorization", "Bearer tenant-a")
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var file struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &file); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/v1/files/" + file.ID, 404},
		{"GET", "/v1/files/" + file.ID + "/content", 404},
		{"DELETE", "/v1/files/" + file.ID, 404},
		{"GET", "/v1/files", 200},
	} {
		r := httptest.NewRequest(request.method, request.path, nil)
		r.Header.Set("Authorization", "Bearer tenant-b")
		r.Header.Set("X-Organization-ID", "org-a")
		r.Header.Set("X-Actor-Organization-ID", "org-a")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != request.status || strings.Contains(w.Body.String(), file.ID) || strings.Contains(w.Body.String(), "tenant-a-private") {
			t.Fatalf("foreign resource exposed: %d %s", w.Code, w.Body.String())
		}
	}
	r = httptest.NewRequest("GET", "/v1/files/"+file.ID+"/content", nil)
	r.Header.Set("Authorization", "Bearer tenant-a")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "tenant-a-private" {
		t.Fatal("foreign delete affected original owner")
	}
}
