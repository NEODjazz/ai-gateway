package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recordingUsageClient struct {
	audit ManagementAudit
	days  int
}

func (c *recordingUsageClient) UsageReport(_ context.Context, audit ManagementAudit, days int) (UsageReport, error) {
	c.audit, c.days = audit, days
	return UsageReport{Days: days, Totals: []UsageAggregate{{Currency: "USD", Requests: 3, TotalTokens: 42, Cost: 0.12}}}, nil
}

func TestAdminUsageReportRequiresAdminAndBoundsRange(t *testing.T) {
	client := &recordingUsageClient{}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithUsageReporting(client)
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/usage/report?days=7", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-Request-ID", "req-usage")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || client.days != 7 || client.audit.RequestID != "req-usage" || !strings.Contains(response.Body.String(), `"total_tokens":42`) {
		t.Fatalf("status=%d client=%+v body=%s", response.Code, client, response.Body.String())
	}
	invalid := httptest.NewRecorder()
	Routes(handler).ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/admin/v1/usage/report?days=0", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d", invalid.Code)
	}
}

func TestRemoteUsageReportUsesScopedSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("days") != "14" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Token") != "billing-secret" {
			t.Fatalf("request=%s %s headers=%v", r.Method, r.URL.String(), r.Header)
		}
		_ = json.NewEncoder(w).Encode(UsageReport{Days: 14})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "billing-secret")
	report, err := client.UsageReport(context.Background(), ManagementAudit{ActorID: "admin"}, 14)
	if err != nil || report.Days != 14 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
