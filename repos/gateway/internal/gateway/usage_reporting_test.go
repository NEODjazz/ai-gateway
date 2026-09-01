package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/provider"
)

type recordingUsageClient struct {
	audit     ManagementAudit
	days      int
	scopeType string
	scopeID   string
	report    UsageReport
	query     UsageReportQuery
}

func (c *recordingUsageClient) FilteredUsageReport(_ context.Context, audit ManagementAudit, query UsageReportQuery) (UsageReport, error) {
	c.audit, c.query = audit, query
	return UsageReport{From: query.From, To: query.To, Totals: []UsageAggregate{{Currency: "USD", Requests: 1, CacheHits: 1, Cost: 0.25, CostPerRequest: 0.25}}}, nil
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
	return UsageReport{Days: days, Totals: []UsageAggregate{{Currency: "USD", Requests: 3, TotalTokens: 42, Cost: 0.12}}, ByUpstreamModel: []UsageAggregate{{Name: "gpt-upstream", Currency: "USD", Requests: 3}}, ByEndpoint: []UsageAggregate{{Name: "azure-primary", Currency: "USD", Requests: 3}}, ByKey: []UsageAggregate{{Name: "key-1", Currency: "USD", Requests: 3}}, ByUser: []UsageAggregate{{Name: "user-1", Currency: "USD", Requests: 3}}, ByTeam: []UsageAggregate{{Name: "team-1", Currency: "USD", Requests: 3}}, ByOrganization: []UsageAggregate{{Name: "org-1", Currency: "USD", Requests: 3}}}, nil
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
	organization := httptest.NewRecorder()
	Routes(handler).ServeHTTP(organization, httptest.NewRequest(http.MethodGet, "/admin/v1/customers/organization/org-a/usage?days=7", nil))
	if organization.Code != http.StatusOK || client.scopeType != "organization" || client.scopeID != "org-a" {
		t.Fatalf("organization status=%d client=%+v body=%s", organization.Code, client, organization.Body.String())
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
	if response.Code != http.StatusOK || client.days != 7 || client.audit.RequestID != "req-usage" || !strings.Contains(response.Body.String(), `"total_tokens":42`) || !strings.Contains(response.Body.String(), `"by_upstream_model":[{"name":"gpt-upstream"`) || !strings.Contains(response.Body.String(), `"by_endpoint":[{"name":"azure-primary"`) || !strings.Contains(response.Body.String(), `"by_key":[{"name":"key-1"`) || !strings.Contains(response.Body.String(), `"by_organization":[{"name":"org-1"`) {
		t.Fatalf("status=%d client=%+v body=%s", response.Code, client, response.Body.String())
	}
	invalid := httptest.NewRecorder()
	Routes(handler).ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/admin/v1/usage/report?days=0", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d", invalid.Code)
	}
}

func TestAdminUsageReportForwardsBoundedDateAndDimensionFilters(t *testing.T) {
	client := &recordingUsageClient{}
	handler := Routes(NewHandler(modulesPipeline("admin"), modelsProvider{}).WithUsageReporting(client))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/usage/report?from=2026-08-01T00:00:00Z&to=2026-08-10T00:00:00Z&model=gpt-5&upstream_model=gpt-5.6&provider=azure&endpoint=azure-primary&tag=production&scope_type=organization&scope_id=org-a", nil))
	if response.Code != http.StatusOK || client.query.Model != "gpt-5" || client.query.UpstreamModel != "gpt-5.6" || client.query.Provider != "azure" || client.query.Endpoint != "azure-primary" || client.query.Tag != "production" || client.query.ScopeType != "organization" || client.query.ScopeID != "org-a" || !strings.Contains(response.Body.String(), `"cache_hits":1`) || !strings.Contains(response.Body.String(), `"cost_per_request":0.25`) {
		t.Fatalf("filtered report was not forwarded: status=%d query=%+v body=%s", response.Code, client.query, response.Body.String())
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/admin/v1/usage/report?from=2026-01-01T00:00:00Z&to=2026-08-10T00:00:00Z", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("unbounded range accepted: %d %s", invalid.Code, invalid.Body.String())
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
		{Name: "gpt-5.6-luna", Currency: "USD", Requests: 20, TotalTokens: 1944, CacheReadInputTokens: 10, CacheWriteInputTokens: 2, AvgLatencyMS: 100},
		{Name: "azure-open-ai", Currency: "USD", Requests: 2, TotalTokens: 58, CacheReadInputTokens: 3, CacheWriteInputTokens: 1, Cost: 0.0008234, AvgLatencyMS: 200},
	}}}
	handler := NewHandler(modulesPipeline("admin"), runtime).WithUsageReporting(client)
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/usage/report?days=30", nil))
	var report UsageReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(report.ByProvider) != 1 || report.ByProvider[0].Name != "azure-open-ai" || report.ByProvider[0].Requests != 22 || report.ByProvider[0].TotalTokens != 2002 || report.ByProvider[0].CacheReadInputTokens != 13 || report.ByProvider[0].CacheWriteInputTokens != 3 || report.ByProvider[0].AvgLatencyMS < 109 || report.ByProvider[0].AvgLatencyMS > 110 {
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

func TestRemoteFilteredUsageReportUsesEncodedDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("from") != "2026-08-01T00:00:00Z" || r.URL.Query().Get("to") != "2026-08-02T00:00:00Z" || r.URL.Query().Get("model") != "model/a" || r.URL.Query().Get("upstream_model") != "upstream/a" || r.URL.Query().Get("provider") != "azure openai" || r.URL.Query().Get("endpoint") != "endpoint/a" || r.URL.Query().Get("scope_type") != "team" || r.URL.Query().Get("scope_id") != "team/a" || r.Header.Get("X-Management-Token") != "billing-secret" {
			t.Fatalf("request=%s headers=%v", r.URL.String(), r.Header)
		}
		_ = json.NewEncoder(w).Encode(UsageReport{Days: 1})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "billing-secret")
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	report, err := client.FilteredUsageReport(context.Background(), ManagementAudit{ActorID: "admin"}, UsageReportQuery{From: from, To: from.Add(24 * time.Hour), ScopeType: "team", ScopeID: "team/a", Model: "model/a", UpstreamModel: "upstream/a", Provider: "azure openai", Endpoint: "endpoint/a", Tag: "production"})
	if err != nil || report.Days != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
