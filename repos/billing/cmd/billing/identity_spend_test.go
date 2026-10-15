package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-billing/internal/modules"
)

type identitySpendReporter struct {
	fakeUsageReporter
	calls int
}

func (r *identitySpendReporter) IdentitySpend(_ context.Context, q modules.IdentitySpendQuery) ([]modules.IdentitySpend, error) {
	r.calls++
	return []modules.IdentitySpend{{ID: q.IDs[0], Currency: "USD", Cost: 3, Requests: 1}}, nil
}

func TestIdentitySpendHTTPSecretScopeAndValidation(t *testing.T) {
	reporter := &identitySpendReporter{}
	mux := http.NewServeMux()
	registerUsageManagement(mux, reporter, nil, "fixture-secret")
	valid := `{"scope_type":"team","ids":["late"],"from":"2026-09-15T12:00:00Z","to":"2026-10-15T12:00:00Z"}`
	for _, tc := range []struct {
		body, secret, roles, org string
		status                   int
	}{{valid, "fixture-secret", "admin", "", 200}, {valid, "", "admin", "", 401}, {valid, "fixture-secret", "org_admin", "org-a", 403}, {strings.Replace(valid, `"team"`, `"organization"`, 1), "fixture-secret", "admin", "", 400}, {strings.Replace(valid, `["late"]`, `[]`, 1), "fixture-secret", "admin", "", 400}} {
		before := reporter.calls
		r := httptest.NewRequest("POST", "/internal/v1/usage/identities", strings.NewReader(tc.body))
		r.Header.Set("X-Management-Token", tc.secret)
		r.Header.Set("X-Actor-Roles", tc.roles)
		r.Header.Set("X-Actor-Organization-ID", tc.org)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.status || tc.status != 200 && reporter.calls != before {
			t.Fatalf("status=%d roles=%s", w.Code, tc.roles)
		}
	}
}
