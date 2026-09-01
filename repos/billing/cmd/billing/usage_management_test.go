package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-billing/internal/modules"
)

type fakeUsageReporter struct {
	days  int
	scope modules.UsageScope
	query modules.UsageReportQuery
}

func (f *fakeUsageReporter) ReportQuery(_ context.Context, query modules.UsageReportQuery) (modules.UsageReport, error) {
	f.query = query
	return modules.UsageReport{From: query.From, To: query.To}, nil
}

func (f *fakeUsageReporter) Report(_ context.Context, days int) (modules.UsageReport, error) {
	f.days = days
	return modules.UsageReport{Days: days, Totals: []modules.UsageAggregate{{Currency: "USD", Requests: 2}}}, nil
}

func (f *fakeUsageReporter) ReportScoped(_ context.Context, days int, scope modules.UsageScope) (modules.UsageReport, error) {
	f.days, f.scope = days, scope
	return modules.UsageReport{Days: days}, nil
}

func TestUsageManagementRequiresSecretAndBoundsRange(t *testing.T) {
	reporter := &fakeUsageReporter{}
	mux := http.NewServeMux()
	registerUsageManagement(mux, reporter, nil, "secret")
	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/internal/v1/usage/report", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	invalidRequest := httptest.NewRequest(http.MethodGet, "/internal/v1/usage/report?days=365", nil)
	invalidRequest.Header.Set("X-Management-Token", "secret")
	invalid := httptest.NewRecorder()
	mux.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d", invalid.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/usage/report?days=7", nil)
	request.Header.Set("X-Management-Token", "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || reporter.days != 7 || !strings.Contains(response.Body.String(), `"requests":2`) {
		t.Fatalf("status=%d days=%d body=%s", response.Code, reporter.days, response.Body.String())
	}
}

func TestUsageManagementSupportsScopedCustomerReport(t *testing.T) {
	reporter := &fakeUsageReporter{}
	mux := http.NewServeMux()
	registerUsageManagement(mux, reporter, nil, "secret")
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/usage/report?days=14&scope_type=team&scope_id=team-a", nil)
	request.Header.Set("X-Management-Token", "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || reporter.days != 14 || reporter.scope.Type != "team" || reporter.scope.ID != "team-a" {
		t.Fatalf("status=%d days=%d scope=%+v body=%s", response.Code, reporter.days, reporter.scope, response.Body.String())
	}
}

func TestUsageManagementSupportsOrganizationScope(t *testing.T) {
	reporter := &fakeUsageReporter{}
	mux := http.NewServeMux()
	registerUsageManagement(mux, reporter, nil, "secret")
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/usage/report?days=14&scope_type=organization&scope_id=org-a", nil)
	request.Header.Set("X-Management-Token", "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || reporter.scope.Type != "organization" || reporter.scope.ID != "org-a" {
		t.Fatalf("status=%d scope=%+v body=%s", response.Code, reporter.scope, response.Body.String())
	}
}

func TestUsageManagementForwardsTagFilter(t *testing.T) {
	reporter := &fakeUsageReporter{}
	mux := http.NewServeMux()
	registerUsageManagement(mux, reporter, nil, "secret")
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/usage/report?from=2026-08-01T00:00:00Z&to=2026-08-02T00:00:00Z&tag=production", nil)
	request.Header.Set("X-Management-Token", "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || reporter.query.Tag != "production" {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, reporter.query, response.Body.String())
	}
}

func TestUsageManagementForwardsDrillDownFilters(t *testing.T) {
	reporter := &fakeUsageReporter{}
	mux := http.NewServeMux()
	registerUsageManagement(mux, reporter, nil, "secret")
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/usage/report?from=2026-08-01T00:00:00Z&to=2026-08-02T00:00:00Z&upstream_model=gpt-5.6&endpoint=azure-primary&scope_type=organization&scope_id=org-a", nil)
	request.Header.Set("X-Management-Token", "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || reporter.query.UpstreamModel != "gpt-5.6" || reporter.query.Endpoint != "azure-primary" || reporter.query.Scope.Type != "organization" || reporter.query.Scope.ID != "org-a" {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, reporter.query, response.Body.String())
	}
}
