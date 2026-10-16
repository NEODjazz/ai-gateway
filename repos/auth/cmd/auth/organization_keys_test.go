package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-auth/internal/modules"
)

func TestOrganizationKeyListingUsesOwnedScope(t *testing.T) {
	for _, test := range []struct {
		query, org string
		status     int
	}{
		{"", "org-a", 200},
		{"?organization_id=org-a", "org-a", 200},
		{"?organization_id=org-b", "org-a", 403},
		{"", "", 403},
	} {
		store := &commandManagementStore{}
		module := modules.NewAuthModuleWithStore(true, store, "fixture", false)
		mux := http.NewServeMux()
		registerManagementRoutes(mux, &module, "fixture-service-secret")
		r := httptest.NewRequest("GET", "/internal/v1/keys"+test.query, nil)
		r.Header.Set("X-Management-Token", "fixture-service-secret")
		r.Header.Set("X-Actor-ID", "actor")
		r.Header.Set("X-Actor-Credential-ID", "credential")
		r.Header.Set("X-Request-ID", "request")
		r.Header.Set("X-Actor-Roles", "org_admin")
		r.Header.Set("X-Actor-Organization-ID", test.org)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatal(w.Code, w.Body.String())
		}
		if test.status == 200 && (!store.query.StrictOrganization || store.query.OrganizationID != "org-a") {
			t.Fatal("member affiliation used instead of tenant ownership")
		}
	}
}
func TestOrganizationKeyMutationIsDeniedAtServiceBoundary(t *testing.T) {
	module := modules.NewAuthModuleWithJWT(true, modules.JWTAuthConfig{})
	mux := http.NewServeMux()
	registerManagementRoutes(mux, &module, "fixture-service-secret")
	for _, test := range []struct{ method, path string }{{"POST", "/internal/v1/keys"}, {"PUT", "/internal/v1/keys/key-a"}, {"DELETE", "/internal/v1/keys/key-a"}, {"POST", "/internal/v1/keys/key-a/rotate"}, {"POST", "/internal/v1/keys/key-a/enable"}, {"POST", "/internal/v1/keys/key-a/disable"}} {
		r := httptest.NewRequest(test.method, test.path, nil)
		r.Header.Set("X-Management-Token", "fixture-service-secret")
		r.Header.Set("X-Actor-ID", "actor")
		r.Header.Set("X-Actor-Credential-ID", "credential")
		r.Header.Set("X-Request-ID", "request")
		r.Header.Set("X-Actor-Roles", "org_admin")
		r.Header.Set("X-Actor-Organization-ID", "org-a")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("organization actor reached global key mutation", w.Code)
		}
	}
}
