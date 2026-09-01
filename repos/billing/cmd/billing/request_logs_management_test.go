package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-billing/internal/modules"
)

type fakeRequestLogReporter struct {
	filter      modules.RequestLogFilter
	groupFilter modules.RequestLogGroupFilter
}

func (f *fakeRequestLogReporter) ListRequestLogs(_ context.Context, filter modules.RequestLogFilter) (modules.RequestLogPage, error) {
	f.filter = filter
	return modules.RequestLogPage{Data: []modules.RequestLog{{RequestID: "req-1", Timestamp: "2026-08-25T12:00:00Z", Status: "ok"}}}, nil
}
func (f *fakeRequestLogReporter) ListRequestLogGroups(_ context.Context, filter modules.RequestLogGroupFilter) (modules.RequestLogGroupPage, error) {
	f.groupFilter = filter
	return modules.RequestLogGroupPage{Data: []modules.RequestLogGroup{{GroupID: "session-1", Requests: 2, Currency: "USD"}}}, nil
}
func (*fakeRequestLogReporter) GetRequestLog(_ context.Context, requestID string) (modules.RequestLog, error) {
	return modules.RequestLog{RequestID: requestID, Timestamp: "2026-08-25T12:00:00Z", Status: "ok"}, nil
}

func TestRequestLogGroupManagementValidatesDimensionAndCursor(t *testing.T) {
	reporter := &fakeRequestLogReporter{}
	mux := http.NewServeMux()
	registerRequestLogManagement(mux, reporter, nil, "management-secret")
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/request-logs/groups?dimension=session&days=30&limit=25&tag=production&before=2026-08-25T12%3A00%3A00Z&before_group_id=session-2&before_currency=USD", nil)
	request.Header.Set("X-Management-Token", "management-secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || reporter.groupFilter.Dimension != "session" || reporter.groupFilter.Tag != "production" || reporter.groupFilter.BeforeGroupID != "session-2" || reporter.groupFilter.BeforeCurrency != "USD" {
		t.Fatalf("status=%d filter=%+v body=%s", response.Code, reporter.groupFilter, response.Body.String())
	}
	invalid := httptest.NewRequest(http.MethodGet, "/internal/v1/request-logs/groups?dimension=model", nil)
	invalid.Header.Set("X-Management-Token", "management-secret")
	invalidResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid dimension status=%d", invalidResponse.Code)
	}
	invalidCursor := httptest.NewRequest(http.MethodGet, "/internal/v1/request-logs/groups?dimension=trace&before=2026-08-25T12%3A00%3A00Z", nil)
	invalidCursor.Header.Set("X-Management-Token", "management-secret")
	invalidCursorResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidCursorResponse, invalidCursor)
	if invalidCursorResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status=%d", invalidCursorResponse.Code)
	}
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
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/request-logs?days=14&limit=25&team_id=team-a&organization_id=org-a&trace_id=0123456789abcdef0123456789abcdef&tag=production&cache_status=hit&failure_class=upstream&min_cost=0.01&max_cost=1.5", nil)
	request.Header.Set("X-Management-Token", "management-secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || reporter.filter.Days != 14 || reporter.filter.Limit != 25 || reporter.filter.TeamID != "team-a" || reporter.filter.OrganizationID != "org-a" || reporter.filter.TraceID != "0123456789abcdef0123456789abcdef" || reporter.filter.Tag != "production" || reporter.filter.CacheStatus != "hit" || reporter.filter.FailureClass != "upstream" || reporter.filter.MinCost == nil || *reporter.filter.MinCost != 0.01 || reporter.filter.MaxCost == nil || *reporter.filter.MaxCost != 1.5 {
		t.Fatalf("status=%d filter=%+v body=%s", response.Code, reporter.filter, response.Body.String())
	}
	invalid := httptest.NewRequest(http.MethodGet, "/internal/v1/request-logs?days=0", nil)
	invalid.Header.Set("X-Management-Token", "management-secret")
	invalidResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d", invalidResponse.Code)
	}
	invalidTrace := httptest.NewRequest(http.MethodGet, "/internal/v1/request-logs?trace_id=not-a-trace", nil)
	invalidTrace.Header.Set("X-Management-Token", "management-secret")
	invalidTraceResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidTraceResponse, invalidTrace)
	if invalidTraceResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid trace status=%d", invalidTraceResponse.Code)
	}
	invalidCost := httptest.NewRequest(http.MethodGet, "/internal/v1/request-logs?min_cost=2&max_cost=1", nil)
	invalidCost.Header.Set("X-Management-Token", "management-secret")
	invalidCostResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidCostResponse, invalidCost)
	if invalidCostResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid cost status=%d", invalidCostResponse.Code)
	}
}
