package main

import (
	"ai-gateway-auth/internal/modules"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIIssuerManagementRequiresAuthenticatedPlatformActor(t *testing.T) {
	module := modules.NewAuthModuleWithJWT(true, modules.JWTAuthConfig{})
	mux := http.NewServeMux()
	registerAPIIssuerRoutes(mux, &module, "fixture-service-secret")
	for _, methodPath := range []struct{ method, path string }{{"GET", "/internal/v1/api-issuers"}, {"POST", "/internal/v1/api-issuers"}, {"PUT", "/internal/v1/api-issuers/a"}, {"POST", "/internal/v1/api-issuers/a/test"}, {"POST", "/internal/v1/api-issuers/a/action"}} {
		for _, role := range []string{"", "org_admin", "team_admin", "admin"} {
			r := httptest.NewRequest(methodPath.method, methodPath.path, strings.NewReader(`{}`))
			r.Header.Set("X-Request-ID", "fixture")
			r.Header.Set("X-Actor-ID", "operator")
			r.Header.Set("X-Actor-Credential-ID", "key")
			r.Header.Set("X-Actor-Roles", role)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatal("service authorization bypass", w.Code)
			}
			r = httptest.NewRequest(methodPath.method, methodPath.path, strings.NewReader(`{}`))
			r.Header.Set("X-Management-Token", "fixture-service-secret")
			r.Header.Set("X-Request-ID", "fixture")
			r.Header.Set("X-Actor-ID", "operator")
			r.Header.Set("X-Actor-Credential-ID", "key")
			r.Header.Set("X-Actor-Roles", role)
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if role != "admin" && w.Code != 403 {
				t.Fatal("tenant reached API trust", w.Code)
			}
			if role == "admin" && w.Code != 503 {
				t.Fatal("unconfigured registry failed open", w.Code)
			}
		}
	}
}
