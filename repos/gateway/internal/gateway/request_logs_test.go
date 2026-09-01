package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recordingRequestLogClient struct {
	audit       ManagementAudit
	filter      RequestLogFilter
	groupFilter RequestLogGroupFilter
}

func (c *recordingRequestLogClient) ListRequestLogs(_ context.Context, audit ManagementAudit, filter RequestLogFilter) (RequestLogPage, error) {
	c.audit, c.filter = audit, filter
	return RequestLogPage{Data: []RequestLog{{RequestID: "req-1", SessionID: "session-1", TraceID: "0123456789abcdef0123456789abcdef", Timestamp: "2026-08-25T12:00:00Z", OrganizationID: "org-a", Tags: []string{"production"}, Status: "ok", APIType: "chat_completions", Phase: "commit", FirstTokenLatencyMS: 42, RetryCount: 2, FallbackCount: 1, CacheStatus: "hit", CacheKind: "semantic", UsageEstimated: true, Currency: "USD"}}}, nil
}
func (c *recordingRequestLogClient) ListRequestLogGroups(_ context.Context, audit ManagementAudit, filter RequestLogGroupFilter) (RequestLogGroupPage, error) {
	c.audit, c.groupFilter = audit, filter
	return RequestLogGroupPage{Data: []RequestLogGroup{{GroupID: "session-1", Requests: 2, Currency: "USD"}}}, nil
}
func (*recordingRequestLogClient) GetRequestLog(_ context.Context, _ ManagementAudit, requestID string) (RequestLog, error) {
	return RequestLog{RequestID: requestID, Timestamp: "2026-08-25T12:00:00Z", Status: "ok", APIType: "chat_completions", Phase: "commit", Currency: "USD"}, nil
}

func TestAdminRequestLogGroupsValidateDimensionAndCursor(t *testing.T) {
	client := &recordingRequestLogClient{}
	handler := Routes(NewHandler(modulesPipeline("admin"), modelsProvider{}).WithRequestLogs(client))
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/request-logs/groups?dimension=session&days=30&limit=25&tag=production&before=2026-08-25T12%3A00%3A00Z&before_group_id=session-2&before_currency=USD", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || client.groupFilter.Dimension != "session" || client.groupFilter.Tag != "production" || client.groupFilter.BeforeGroupID != "session-2" || client.groupFilter.BeforeCurrency != "USD" {
		t.Fatalf("status=%d filter=%+v body=%s", response.Code, client.groupFilter, response.Body.String())
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/admin/v1/request-logs/groups?dimension=model", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid dimension status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}
func (*recordingRequestLogClient) GetRequestLogSettings(_ context.Context, _ ManagementAudit) (RequestLogSettings, error) {
	return RequestLogSettings{RetentionDays: 730, ContentStored: false, MaxQueryDays: 90}, nil
}

func TestAdminRequestLogsRequireAdminAndValidateFilters(t *testing.T) {
	client := &recordingRequestLogClient{}
	handler := Routes(NewHandler(modulesPipeline("admin"), modelsProvider{}).WithRequestLogs(client))
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/request-logs?days=14&limit=25&status=error&team_id=team-a&organization_id=org-a&session_id=session-1&trace_id=0123456789abcdef0123456789abcdef&tag=production&cache_status=hit&failure_class=upstream&min_cost=0.01&max_cost=1.5", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-Request-ID", "req-admin")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || client.filter.Days != 14 || client.filter.Limit != 25 || client.filter.Status != "error" || client.filter.TeamID != "team-a" || client.filter.OrganizationID != "org-a" || client.filter.SessionID != "session-1" || client.filter.TraceID != "0123456789abcdef0123456789abcdef" || client.filter.Tag != "production" || client.filter.CacheStatus != "hit" || client.filter.FailureClass != "upstream" || client.filter.MinCost == nil || *client.filter.MinCost != 0.01 || client.filter.MaxCost == nil || *client.filter.MaxCost != 1.5 || client.audit.RequestID != "req-admin" {
		t.Fatalf("status=%d filter=%+v audit=%+v body=%s", response.Code, client.filter, client.audit, response.Body.String())
	}
	for _, expected := range []string{`"organization_id":"org-a"`, `"trace_id":"0123456789abcdef0123456789abcdef"`, `"tags":["production"]`, `"first_token_latency_ms":42`, `"retry_count":2`, `"fallback_count":1`, `"cache_kind":"semantic"`, `"usage_estimated":true`} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("request log field %s was stripped: %s", expected, response.Body.String())
		}
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/admin/v1/request-logs?status=secret", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	invalidTrace := httptest.NewRecorder()
	handler.ServeHTTP(invalidTrace, httptest.NewRequest(http.MethodGet, "/admin/v1/request-logs?trace_id=not-a-trace", nil))
	if invalidTrace.Code != http.StatusBadRequest {
		t.Fatalf("invalid trace status=%d body=%s", invalidTrace.Code, invalidTrace.Body.String())
	}
	invalidCost := httptest.NewRecorder()
	handler.ServeHTTP(invalidCost, httptest.NewRequest(http.MethodGet, "/admin/v1/request-logs?min_cost=2&max_cost=1", nil))
	if invalidCost.Code != http.StatusBadRequest {
		t.Fatalf("invalid cost status=%d body=%s", invalidCost.Code, invalidCost.Body.String())
	}
}

func TestRemoteRequestLogsUseScopedManagementCredential(t *testing.T) {
	minCost, maxCost := 0.01, 1.5
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Token") != "billing-secret" || r.URL.Query().Get("team_id") != "team-a" || r.URL.Query().Get("organization_id") != "org-a" || r.URL.Query().Get("trace_id") != "trace-1" || r.URL.Query().Get("tag") != "production" || r.URL.Query().Get("cache_status") != "miss" || r.URL.Query().Get("failure_class") != "upstream" || r.URL.Query().Get("min_cost") != "0.01" || r.URL.Query().Get("max_cost") != "1.5" || r.URL.Query().Get("limit") != "20" {
			t.Fatalf("request=%s headers=%v", r.URL.String(), r.Header)
		}
		_ = json.NewEncoder(w).Encode(RequestLogPage{Data: []RequestLog{{RequestID: "req-1"}}})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "billing-secret")
	page, err := client.ListRequestLogs(context.Background(), ManagementAudit{RequestID: "admin-request", ActorID: "admin", CredentialID: "cred"}, RequestLogFilter{Days: 7, Limit: 20, TeamID: "team-a", OrganizationID: "org-a", TraceID: "trace-1", Tag: "production", CacheStatus: "miss", FailureClass: "upstream", MinCost: &minCost, MaxCost: &maxCost})
	if err != nil || len(page.Data) != 1 || page.Data[0].RequestID != "req-1" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestRemoteRequestLogGroupsUseBoundedCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.URL.Path != "/internal/v1/request-logs/groups" || query.Get("dimension") != "trace" || query.Get("before_group_id") != "trace-2" || query.Get("before_currency") != "EUR" || query.Get("tag") != "production" {
			t.Fatalf("request=%s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(RequestLogGroupPage{Data: []RequestLogGroup{{GroupID: "trace-1", Requests: 2, Currency: "EUR"}}})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "billing-secret")
	page, err := client.ListRequestLogGroups(context.Background(), ManagementAudit{RequestID: "admin-request"}, RequestLogGroupFilter{
		RequestLogFilter: RequestLogFilter{Days: 7, Limit: 20, Tag: "production", Before: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)},
		Dimension:        "trace", BeforeGroupID: "trace-2", BeforeCurrency: "EUR",
	})
	if err != nil || len(page.Data) != 1 || page.Data[0].GroupID != "trace-1" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestRequestLogResponseNeverContainsProviderErrorOrContent(t *testing.T) {
	client := &recordingRequestLogClient{}
	handler := Routes(NewHandler(modulesPipeline("admin"), modelsProvider{}).WithRequestLogs(client))
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/request-logs/req-1", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	for _, forbidden := range []string{"provider_error", "messages", "prompt", "response_content", "api_key"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("sensitive field %q exposed: %s", forbidden, response.Body.String())
		}
	}
}
