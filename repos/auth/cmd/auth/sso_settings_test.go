package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-auth/internal/modules"
)

func TestSSOManagementRequiresServiceIdentityAndGlobalAdmin(t *testing.T) {
	module := modules.NewAuthModuleWithJWT(true, modules.JWTAuthConfig{})
	mux := http.NewServeMux()
	registerSSORoutes(mux, &module, "fixture-service-secret")
	for _, test := range []struct {
		name, secret, roles, path, body string
		status                          int
	}{
		{"missing secret", "", "admin", "/internal/v1/sso/settings", "", 401},
		{"team admin", "fixture-service-secret", "team_admin", "/internal/v1/sso/settings", "", 403},
		{"unconfigured", "fixture-service-secret", "admin", "/internal/v1/sso/settings", "", 503},
		{"private environment fallback", "fixture-service-secret", "service", "/internal/v1/sso/active", "", 200},
		{"unknown input", "fixture-service-secret", "admin", "/internal/v1/sso/test", `{"expected_revision":0,"unexpected":true}`, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			method := "GET"
			if test.body != "" {
				method = "POST"
			}
			r := httptest.NewRequest(method, test.path, strings.NewReader(test.body))
			r.Header.Set("X-Management-Token", test.secret)
			r.Header.Set("X-Actor-ID", "fixture-admin")
			r.Header.Set("X-Actor-Credential-ID", "fixture-key")
			r.Header.Set("X-Request-ID", "fixture-request")
			r.Header.Set("X-Actor-Roles", test.roles)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status=%d", w.Code)
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Fatal("secret exposed")
			}
		})
	}
}
