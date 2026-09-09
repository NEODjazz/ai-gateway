package modules

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClickHouseUsageReporterBuildsBoundedReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, "{from:UInt64}") || !strings.Contains(query, "{to:UInt64}") || r.URL.Query().Get("param_from") != "1785067200" || r.URL.Query().Get("param_to") != "1787659200" || !strings.Contains(query, "safe_db.safe_events AS usage") || !strings.Contains(query, "provider_id != ''") || !strings.Contains(query, "substring(pricing_key, position(pricing_key, '/') + 1)") || !strings.Contains(query, "startsWith(model, concat(provider_endpoint_name, '-'))") || !strings.Contains(query, "'upstream_model' AS kind") || !strings.Contains(query, "if(upstream_model = '', 'Unassigned', upstream_model)") || !strings.Contains(query, "'endpoint' AS kind") || !strings.Contains(query, "if(provider_endpoint_name = '', 'Unassigned', provider_endpoint_name)") || !strings.Contains(query, "sum(cache_read_input_tokens) AS cache_read_input_tokens") || !strings.Contains(query, "sum(cache_write_input_tokens) AS cache_write_input_tokens") || !strings.Contains(query, "sum(search_requests) AS search_requests") || !strings.Contains(query, "countIf(cache_status = 'hit')") || !strings.Contains(query, "ARRAY JOIN if(empty(tags), ['Untagged'], tags)") || !strings.Contains(query, "'key' AS kind") || !strings.Contains(query, "'user' AS kind") || !strings.Contains(query, "'team' AS kind") || !strings.Contains(query, "'organization' AS kind") || !strings.Contains(query, "if(api_key_fingerprint = '', 'Unassigned', api_key_fingerprint)") || !strings.Contains(query, "sum(usage.cost) AS cost") || strings.Contains(query, "sum(cost) / count()") || strings.Contains(query, "token_hash") {
			t.Fatalf("unsafe report query: %s", query)
		}
		_, _ = w.Write([]byte("{\"kind\":\"total\",\"date\":\"\",\"name\":\"\",\"currency\":\"USD\",\"requests\":2,\"errors\":1,\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"cache_read_input_tokens\":6,\"cache_write_input_tokens\":2,\"search_requests\":2,\"cost\":0.25,\"avg_latency_ms\":12.5}\n" +
			"{\"kind\":\"model\",\"date\":\"\",\"name\":\"m1\",\"currency\":\"USD\",\"requests\":2,\"errors\":1,\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"cost\":0.25,\"avg_latency_ms\":12.5}\n" +
			"{\"kind\":\"upstream_model\",\"date\":\"\",\"name\":\"gpt-5.6-2026-07-09\",\"currency\":\"USD\",\"requests\":2,\"errors\":1,\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"cost\":0.25,\"avg_latency_ms\":12.5}\n" +
			"{\"kind\":\"endpoint\",\"date\":\"\",\"name\":\"azure-primary\",\"currency\":\"USD\",\"requests\":2,\"errors\":1,\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"cost\":0.25,\"avg_latency_ms\":12.5}\n" +
			"{\"kind\":\"tag\",\"date\":\"\",\"name\":\"production\",\"currency\":\"USD\",\"requests\":2,\"errors\":1,\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"cost\":0.25,\"avg_latency_ms\":12.5}\n" +
			"{\"kind\":\"key\",\"date\":\"\",\"name\":\"key-1\",\"currency\":\"USD\",\"requests\":2,\"errors\":1,\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"cost\":0.25,\"avg_latency_ms\":12.5}\n" +
			"{\"kind\":\"user\",\"date\":\"\",\"name\":\"user-1\",\"currency\":\"USD\",\"requests\":2,\"errors\":1,\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"cost\":0.25,\"avg_latency_ms\":12.5}\n" +
			"{\"kind\":\"team\",\"date\":\"\",\"name\":\"team-1\",\"currency\":\"USD\",\"requests\":2,\"errors\":1,\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"cost\":0.25,\"avg_latency_ms\":12.5}\n" +
			"{\"kind\":\"organization\",\"date\":\"\",\"name\":\"org-1\",\"currency\":\"USD\",\"requests\":2,\"errors\":1,\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"cost\":0.25,\"avg_latency_ms\":12.5}\n"))
	}))
	defer server.Close()
	reporter, err := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "safe_db", ClickHouseUsageEventsTable: "safe_events"})
	if err != nil {
		t.Fatal(err)
	}
	reporter.now = func() time.Time { return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC) }
	report, err := reporter.Report(context.Background(), 30)
	if err != nil || len(report.Totals) != 1 || report.Totals[0].TotalTokens != 15 || report.Totals[0].CacheReadInputTokens != 6 || report.Totals[0].CacheWriteInputTokens != 2 || report.Totals[0].SearchRequests != 2 || len(report.ByModel) != 1 || len(report.ByUpstreamModel) != 1 || report.ByUpstreamModel[0].Name != "gpt-5.6-2026-07-09" || len(report.ByEndpoint) != 1 || report.ByEndpoint[0].Name != "azure-primary" || len(report.ByTag) != 1 || report.ByTag[0].Name != "production" || len(report.ByKey) != 1 || report.ByKey[0].Name != "key-1" || len(report.ByUser) != 1 || len(report.ByTeam) != 1 || len(report.ByOrganization) != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestClickHouseUsageReporterParameterizesDimensionFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, "= {model:String}") || !strings.Contains(query, "upstream_model = {upstream_model:String}") || !strings.Contains(query, "= {provider:String}") || !strings.Contains(query, "provider_endpoint_name = {endpoint:String}") || !strings.Contains(query, "has(tags, {tag:String})") || r.URL.Query().Get("param_model") != "model' OR 1=1" || r.URL.Query().Get("param_upstream_model") != "upstream/model" || r.URL.Query().Get("param_provider") != "provider/x" || r.URL.Query().Get("param_endpoint") != "endpoint/a" || r.URL.Query().Get("param_tag") != "production' OR 1=1" || strings.Contains(query, "model' OR 1=1") || strings.Contains(query, "production' OR 1=1") {
			t.Fatalf("filters were not safely parameterized: query=%q params=%v", query, r.URL.Query())
		}
	}))
	defer server.Close()
	reporter, _ := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "db", ClickHouseUsageEventsTable: "events"})
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if _, err := reporter.ReportQuery(context.Background(), UsageReportQuery{From: from, To: from.Add(24 * time.Hour), Model: "model' OR 1=1", UpstreamModel: "upstream/model", Provider: "provider/x", Endpoint: "endpoint/a", Tag: "production' OR 1=1"}); err != nil {
		t.Fatal(err)
	}
}

func TestClickHouseUsageReporterRejectsUnsafeConfigurationAndRange(t *testing.T) {
	if _, err := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseDatabase: "db;DROP", ClickHouseUsageEventsTable: "events"}); err == nil {
		t.Fatal("unsafe database identifier accepted")
	}
	reporter, err := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseDatabase: "db", ClickHouseUsageEventsTable: "events"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reporter.Report(context.Background(), 91); err == nil {
		t.Fatal("unbounded report range accepted")
	}
}

func TestClickHouseUsageReporterUsesParameterizedScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, "team_id = {scope_id:String}") || strings.Contains(query, "team-a' OR") || r.URL.Query().Get("param_scope_id") != "team-a' OR 1=1" {
			t.Fatalf("scope was not safely parameterized: query=%q params=%v", query, r.URL.Query())
		}
	}))
	defer server.Close()
	reporter, _ := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "db", ClickHouseUsageEventsTable: "events"})
	if _, err := reporter.ReportScoped(context.Background(), 7, UsageScope{Type: "team", ID: "team-a' OR 1=1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reporter.ReportScoped(context.Background(), 7, UsageScope{Type: "provider", ID: "x"}); err == nil {
		t.Fatal("unsupported customer scope accepted")
	}
}

func TestClickHouseUsageReporterSupportsOrganizationScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, "organization_id = {scope_id:String}") || r.URL.Query().Get("param_scope_id") != "org-a" {
			t.Fatalf("organization scope was not parameterized: query=%q params=%v", query, r.URL.Query())
		}
	}))
	defer server.Close()
	reporter, _ := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "db", ClickHouseUsageEventsTable: "events"})
	if _, err := reporter.ReportScoped(context.Background(), 7, UsageScope{Type: "organization", ID: "org-a"}); err != nil {
		t.Fatal(err)
	}
}

func TestClickHouseUsageReporterUsesNonSecretKeyFingerprintScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, "api_key_fingerprint = {scope_id:String}") || strings.Contains(query, "credential_id = {scope_id:String}") || r.URL.Query().Get("param_scope_id") != "key-1" {
			t.Fatalf("key scope did not use the ClickHouse fingerprint column: query=%q params=%v", query, r.URL.Query())
		}
	}))
	defer server.Close()
	reporter, _ := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "db", ClickHouseUsageEventsTable: "events"})
	if _, err := reporter.ReportScoped(context.Background(), 7, UsageScope{Type: "key", ID: "key-1"}); err != nil {
		t.Fatal(err)
	}
}
