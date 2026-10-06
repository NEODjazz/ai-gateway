package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-billing/internal/modules"
)

func orgReportRequest(path, org, roles string) *http.Request {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("X-Management-Token", "fixture-service-secret")
	r.Header.Set("X-Actor-Organization-ID", org)
	r.Header.Set("X-Actor-Roles", roles)
	return r
}
func TestBillingOrganizationUsageScopeCannotBeOverridden(t *testing.T) {
	for _, test := range []struct {
		path, org string
		status    int
	}{
		{"/internal/v1/usage/report", "org-a", 200},
		{"/internal/v1/usage/report?scope_type=organization&scope_id=org-a", "org-a", 200},
		{"/internal/v1/usage/report?scope_type=organization&scope_id=org-b", "org-a", 403},
		{"/internal/v1/usage/report?scope_type=user&scope_id=shared-user", "org-a", 403},
		{"/internal/v1/usage/report?from=2026-08-01T00:00:00Z&to=2026-08-02T00:00:00Z&model=m", "org-a", 200},
		{"/internal/v1/usage/report", "", 403},
	} {
		t.Run(test.path+test.org, func(t *testing.T) {
			reporter := &fakeUsageReporter{}
			mux := http.NewServeMux()
			registerUsageManagement(mux, reporter, nil, "fixture-service-secret")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, orgReportRequest(test.path, test.org, "org_admin"))
			if w.Code != test.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if w.Code == 200 {
				scope := reporter.scope
				if reporter.query.Scope.Type != "" {
					scope = reporter.query.Scope
				}
				if scope.Type != "organization" || scope.ID != "org-a" {
					t.Fatal("query escaped tenant", scope)
				}
			} else if reporter.days != 0 || reporter.query.Scope.Type != "" {
				t.Fatal("unauthorized query executed")
			}
		})
	}
	reporter := &fakeUsageReporter{}
	mux := http.NewServeMux()
	registerUsageManagement(mux, reporter, nil, "fixture-service-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, orgReportRequest("/internal/v1/usage/report", "org-a", "admin,org_admin"))
	if w.Code != 200 || reporter.scope.Type != "" {
		t.Fatal("platform administrator lost global scope")
	}
}

type scopedLogReporter struct {
	fakeRequestLogReporter
	org, id string
	row     modules.RequestLog
}

func (s *scopedLogReporter) GetRequestLogScoped(_ context.Context, id, org string) (modules.RequestLog, error) {
	s.id, s.org = id, org
	return s.row, nil
}
func TestBillingOrganizationRequestLogScopeAndDetail(t *testing.T) {
	for _, test := range []struct {
		path, org string
		status    int
	}{
		{"/internal/v1/request-logs?user_id=shared-user", "org-a", 200},
		{"/internal/v1/request-logs?organization_id=org-b", "org-a", 403},
		{"/internal/v1/request-logs/groups?dimension=session", "org-a", 200},
		{"/internal/v1/request-logs/groups?dimension=session&organization_id=org-b", "org-a", 403},
		{"/internal/v1/request-logs", "", 403},
	} {
		t.Run(test.path, func(t *testing.T) {
			reporter := &scopedLogReporter{}
			mux := http.NewServeMux()
			registerRequestLogManagement(mux, reporter, nil, "fixture-service-secret")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, orgReportRequest(test.path, test.org, "org_admin"))
			if w.Code != test.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if w.Code == 200 && reporter.filter.OrganizationID != "org-a" && reporter.groupFilter.OrganizationID != "org-a" {
				t.Fatal("list/group query escaped tenant")
			}
		})
	}
	for _, org := range []string{"org-a", "org-b", ""} {
		reporter := &scopedLogReporter{row: modules.RequestLog{RequestID: "shared-id", OrganizationID: org}}
		mux := http.NewServeMux()
		registerRequestLogManagement(mux, reporter, nil, "fixture-service-secret")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, orgReportRequest("/internal/v1/request-logs/shared-id", "org-a", "org_admin"))
		expected := 404
		if org == "org-a" {
			expected = 200
		}
		if w.Code != expected || reporter.org != "org-a" || reporter.id != "shared-id" {
			t.Fatal("detail disclosure or unscoped query", w.Code)
		}
	}
	mux := http.NewServeMux()
	registerRequestLogManagement(mux, &fakeRequestLogReporter{}, nil, "fixture-service-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, orgReportRequest("/internal/v1/request-logs/shared-id", "org-a", "org_admin"))
	if w.Code != 503 {
		t.Fatal("missing scoped reporter fell back to global detail")
	}
}
