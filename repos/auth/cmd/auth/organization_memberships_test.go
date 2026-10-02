package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-auth/internal/modules"
)

func TestOrganizationMembershipManagementRequiresGlobalAdmin(t *testing.T) {
	module := modules.NewAuthModuleWithJWT(true, modules.JWTAuthConfig{})
	mux := http.NewServeMux()
	registerOrganizationMembershipRoutes(mux, &module, "fixture-service-secret")
	for _, roles := range []string{"", "user", "team_admin", "org_admin"} {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			path := "/internal/v1/organizations/org-a/members"
			if method == http.MethodPut {
				path += "/user-a"
			}
			r := httptest.NewRequest(method, path, nil)
			r.Header.Set("X-Management-Token", "fixture-service-secret")
			r.Header.Set("X-Actor-ID", "fixture-actor")
			r.Header.Set("X-Actor-Credential-ID", "fixture-key")
			r.Header.Set("X-Request-ID", "fixture-request")
			r.Header.Set("X-Actor-Roles", roles)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("roles=%q method=%s status=%d", roles, method, w.Code)
			}
		}
	}
}
