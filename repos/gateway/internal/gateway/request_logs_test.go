package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recordingRequestLogClient struct {
	audit  ManagementAudit
	filter RequestLogFilter
}

func (c *recordingRequestLogClient) ListRequestLogs(_ context.Context, audit ManagementAudit, filter RequestLogFilter) (RequestLogPage, error) {
	c.audit, c.filter = audit, filter
	return RequestLogPage{Data: []RequestLog{{RequestID: "req-1", Timestamp: "2026-08-25T12:00:00Z", Status: "ok", APIType: "chat_completions", Phase: "commit", Currency: "USD"}}}, nil
}
func (*recordingRequestLogClient) GetRequestLog(_ context.Context, _ ManagementAudit, requestID string) (RequestLog, error) {
	return RequestLog{RequestID: requestID, Timestamp: "2026-08-25T12:00:00Z", Status: "ok", APIType: "chat_completions", Phase: "commit", Currency: "USD"}, nil
}
func (*recordingRequestLogClient) GetRequestLogSettings(_ context.Context, _ ManagementAudit) (RequestLogSettings, error) {
	return RequestLogSettings{RetentionDays: 730, ContentStored: false, MaxQueryDays: 90}, nil
}

func TestAdminRequestLogsRequireAdminAndValidateFilters(t *testing.T) {
	client := &recordingRequestLogClient{}
	handler := Routes(NewHandler(modulesPipeline("admin"), modelsProvider{}).WithRequestLogs(client))
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/request-logs?days=14&limit=25&status=error&team_id=team-a&session_id=session-1", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-Request-ID", "req-admin")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || client.filter.Days != 14 || client.filter.Limit != 25 || client.filter.Status != "error" || client.filter.TeamID != "team-a" || client.filter.SessionID != "session-1" || client.audit.RequestID != "req-admin" {
		t.Fatalf("status=%d filter=%+v audit=%+v body=%s", response.Code, client.filter, client.audit, response.Body.String())
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/admin/v1/request-logs?status=secret", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestRemoteRequestLogsUseScopedManagementCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Token") != "billing-secret" || r.URL.Query().Get("team_id") != "team-a" || r.URL.Query().Get("limit") != "20" {
			t.Fatalf("request=%s headers=%v", r.URL.String(), r.Header)
		}
		_ = json.NewEncoder(w).Encode(RequestLogPage{Data: []RequestLog{{RequestID: "req-1"}}})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "billing-secret")
	page, err := client.ListRequestLogs(context.Background(), ManagementAudit{RequestID: "admin-request", ActorID: "admin", CredentialID: "cred"}, RequestLogFilter{Days: 7, Limit: 20, TeamID: "team-a"})
	if err != nil || len(page.Data) != 1 || page.Data[0].RequestID != "req-1" {
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
