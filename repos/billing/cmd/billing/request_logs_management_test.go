package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-billing/internal/modules"
)

type fakeRequestLogReporter struct {
	filter modules.RequestLogFilter
}

func (f *fakeRequestLogReporter) ListRequestLogs(_ context.Context, filter modules.RequestLogFilter) (modules.RequestLogPage, error) {
	f.filter = filter
	return modules.RequestLogPage{Data: []modules.RequestLog{{RequestID: "req-1", Timestamp: "2026-08-25T12:00:00Z", Status: "ok"}}}, nil
}
func (*fakeRequestLogReporter) GetRequestLog(_ context.Context, requestID string) (modules.RequestLog, error) {
	return modules.RequestLog{RequestID: requestID, Timestamp: "2026-08-25T12:00:00Z", Status: "ok"}, nil
}
func (*fakeRequestLogReporter) RequestLogSettings() modules.RequestLogSettings {
	return modules.RequestLogSettings{RetentionDays: 730, MaxQueryDays: 90}
}

func TestRequestLogManagementRequiresSecretAndValidatesFilters(t *testing.T) {
	reporter := &fakeRequestLogReporter{}
	mux := http.NewServeMux()
	registerRequestLogManagement(mux, reporter, nil, "management-secret")
	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/internal/v1/request-logs", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/request-logs?days=14&limit=25&team_id=team-a&organization_id=org-a&cache_status=hit", nil)
	request.Header.Set("X-Management-Token", "management-secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || reporter.filter.Days != 14 || reporter.filter.Limit != 25 || reporter.filter.TeamID != "team-a" || reporter.filter.OrganizationID != "org-a" || reporter.filter.CacheStatus != "hit" {
		t.Fatalf("status=%d filter=%+v body=%s", response.Code, reporter.filter, response.Body.String())
	}
	invalid := httptest.NewRequest(http.MethodGet, "/internal/v1/request-logs?days=0", nil)
	invalid.Header.Set("X-Management-Token", "management-secret")
	invalidResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d", invalidResponse.Code)
	}
}
