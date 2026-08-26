package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/provider"
)

type recordingUsageClient struct {
	audit     ManagementAudit
	days      int
	scopeType string
	scopeID   string
	report    UsageReport
}

func (c *recordingUsageClient) ScopedUsageReport(_ context.Context, audit ManagementAudit, days int, scopeType, scopeID string) (UsageReport, error) {
	c.audit, c.days, c.scopeType, c.scopeID = audit, days, scopeType, scopeID
	return UsageReport{Days: days, Totals: []UsageAggregate{{Currency: "USD", Requests: 2}}}, nil
}

func (c *recordingUsageClient) UsageReport(_ context.Context, audit ManagementAudit, days int) (UsageReport, error) {
	c.audit, c.days = audit, days
	if c.report.ByProvider != nil {
		c.report.Days = days
		return c.report, nil
	}
	return UsageReport{Days: days, Totals: []UsageAggregate{{Currency: "USD", Requests: 3, TotalTokens: 42, Cost: 0.12}}}, nil
}

func TestCustomerUsageReportValidatesAndForwardsScope(t *testing.T) {
	client := &recordingUsageClient{}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithUsageReporting(client)
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/customers/team/team-a/usage?days=14", nil))
	if response.Code != http.StatusOK || client.scopeType != "team" || client.scopeID != "team-a" || client.days != 14 {
		t.Fatalf("status=%d client=%+v body=%s", response.Code, client, response.Body.String())
	}
	invalid := httptest.NewRecorder()
	Routes(handler).ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/admin/v1/customers/provider/x/usage", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}
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

func TestAdminUsageReportMergesDeploymentRowsByManagedProvider(t *testing.T) {
	runtime := provider.New(provider.Config{})
	providers := runtime.(provider.ProviderController)
	if _, err := providers.CreateProvider(provider.ManagedProvider{ID: "azure-open-ai", Type: "demo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	deployments := runtime.(provider.DeploymentController)
	if _, err := deployments.CreateModelDeployment(provider.ModelDeployment{ID: "gpt-5.6-luna", ProviderID: "azure-open-ai", Models: []string{"gpt-5.6-luna"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	client := &recordingUsageClient{report: UsageReport{ByProvider: []UsageAggregate{
		{Name: "gpt-5.6-luna", Currency: "USD", Requests: 20, TotalTokens: 1944, AvgLatencyMS: 100},
		{Name: "azure-open-ai", Currency: "USD", Requests: 2, TotalTokens: 58, Cost: 0.0008234, AvgLatencyMS: 200},
	}}}
	handler := NewHandler(modulesPipeline("admin"), runtime).WithUsageReporting(client)
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/usage/report?days=30", nil))
	var report UsageReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(report.ByProvider) != 1 || report.ByProvider[0].Name != "azure-open-ai" || report.ByProvider[0].Requests != 22 || report.ByProvider[0].TotalTokens != 2002 || report.ByProvider[0].AvgLatencyMS < 109 || report.ByProvider[0].AvgLatencyMS > 110 {
		t.Fatalf("provider usage was not merged: status=%d report=%+v", response.Code, report)
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

func TestRemoteScopedUsageReportUsesEncodedScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope_type") != "user" || r.URL.Query().Get("scope_id") != "customer/a" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Token") != "billing-secret" {
			t.Fatalf("request=%s headers=%v", r.URL.String(), r.Header)
		}
		_ = json.NewEncoder(w).Encode(UsageReport{Days: 7})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "billing-secret")
	report, err := client.ScopedUsageReport(context.Background(), ManagementAudit{ActorID: "admin"}, 7, "user", "customer/a")
	if err != nil || report.Days != 7 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
