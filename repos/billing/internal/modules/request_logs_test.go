package modules

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClickHouseRequestLogsUseParametersAndBoundPagination(t *testing.T) {
	minCost, maxCost := 0.01, 0.2
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if strings.Contains(query, "req' OR 1=1") || strings.Contains(query, "production' OR 1=1") || !strings.Contains(query, "request_id = {request_id:String}") || !strings.Contains(query, "trace_id = {trace_id:String}") || !strings.Contains(query, "has(tags, {tag:String})") || !strings.Contains(query, "failure_class = {failure_class:String}") || !strings.Contains(query, "usage.cost >= {min_cost:Float64}") || !strings.Contains(query, "usage.cost <= {max_cost:Float64}") || !strings.Contains(query, "LIMIT 3") || !strings.Contains(query, "first_token_latency_ms") || !strings.Contains(query, "cache_read_input_tokens") || !strings.Contains(query, "cache_write_input_tokens") || !strings.Contains(query, "search_requests") || !strings.Contains(query, "search_requests_estimated") || !strings.Contains(query, "usage_estimated") {
			t.Fatalf("unsafe request log query: %s", query)
		}
		if r.URL.Query().Get("param_request_id") != "req' OR 1=1" {
			t.Fatalf("request id was not bound as a parameter: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("param_trace_id") != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("trace id was not bound as a parameter: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("param_tag") != "production' OR 1=1" {
			t.Fatalf("tag was not bound as a parameter: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("param_failure_class") != "upstream" || r.URL.Query().Get("param_min_cost") != "0.01" || r.URL.Query().Get("param_max_cost") != "0.2" {
			t.Fatalf("cost/failure filters were not bound: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("param_from") != "1785542400" || r.URL.Query().Get("param_to") != "1785628800" || !strings.Contains(query, "timestamp_unix >= {from:UInt64}") || !strings.Contains(query, "timestamp_unix < {to:UInt64}") {
			t.Fatalf("custom range was not bound: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(
			`{"timestamp":"2026-08-25T12:00:00Z","request_id":"r3","trace_id":"0123456789abcdef0123456789abcdef","organization_id":"org-1","tags":["production"],"status":"ok","api_type":"chat_completions","phase":"commit","latency_ms":40,"first_token_latency_ms":12,"retry_count":2,"fallback_count":1,"cache_status":"miss","cache_kind":"exact","input_tokens":1,"output_tokens":2,"total_tokens":3,"cache_read_input_tokens":2,"cache_write_input_tokens":1,"search_requests":2,"search_requests_estimated":false,"usage_estimated":true,"cost":0.1,"currency":"USD","content_stored":false}` + "\n" +
				`{"timestamp":"2026-08-25T11:59:00Z","request_id":"r2","status":"error","failure_class":"upstream","api_type":"chat_completions","phase":"cancel","latency_ms":5,"input_tokens":0,"output_tokens":0,"total_tokens":0,"cost":0,"currency":"USD","content_stored":false}` + "\n" +
				`{"timestamp":"2026-08-25T11:58:00Z","request_id":"r1","status":"ok","api_type":"responses","phase":"commit","latency_ms":6,"input_tokens":3,"output_tokens":4,"total_tokens":7,"cost":0.2,"currency":"USD","content_stored":false}` + "\n"))
	}))
	defer server.Close()
	reporter, err := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "safe_db", ClickHouseUsageEventsTable: "safe_events"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := reporter.ListRequestLogs(context.Background(), RequestLogFilter{Days: 7, From: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), Limit: 2, RequestID: "req' OR 1=1", TraceID: "0123456789abcdef0123456789abcdef", Tag: "production' OR 1=1", FailureClass: "upstream", MinCost: &minCost, MaxCost: &maxCost, Before: time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC), BeforeRequestID: "cursor-request"})
	if err != nil || len(page.Data) != 2 || page.NextBefore != "2026-08-25T11:59:00Z" || page.NextRequestID != "r2" || page.Data[1].FailureClass != "upstream" || page.Data[0].ContentStored || page.Data[0].TraceID != "0123456789abcdef0123456789abcdef" || len(page.Data[0].Tags) != 1 || page.Data[0].Tags[0] != "production" || page.Data[0].FirstTokenLatencyMS != 12 || page.Data[0].RetryCount != 2 || page.Data[0].FallbackCount != 1 || page.Data[0].CacheReadInputTokens != 2 || page.Data[0].CacheWriteInputTokens != 1 || page.Data[0].SearchRequests != 2 || page.Data[0].SearchRequestsEstimated || !page.Data[0].UsageEstimated || page.Data[0].OrganizationID != "org-1" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestClickHouseRequestLogDetailAndSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("param_request_id") != "req-1" || strings.Contains(r.URL.Query().Get("query"), "provider.error") {
			t.Fatalf("unsafe detail query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"timestamp":"2026-08-25T12:00:00Z","request_id":"req-1","status":"ok","api_type":"chat_completions","phase":"commit","latency_ms":4,"input_tokens":1,"output_tokens":2,"total_tokens":3,"cost":0.1,"currency":"USD","content_stored":false}` + "\n"))
	}))
	defer server.Close()
	reporter, _ := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "safe_db", ClickHouseUsageEventsTable: "safe_events"})
	row, err := reporter.GetRequestLog(context.Background(), "req-1")
	settings := reporter.RequestLogSettings()
	if err != nil || row.RequestID != "req-1" || settings.ContentStored || settings.RetentionDays != 730 || settings.MaxQueryDays != 90 {
		t.Fatalf("row=%+v settings=%+v err=%v", row, settings, err)
	}
}

func TestClickHouseRequestLogGroupsAreServerAggregatedAndCursorPaginated(t *testing.T) {
	minCost, maxCost := 0.01, 0.2
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		for _, expected := range []string{"if(session_id = ''", "countIf(status = 'error')", "groupUniqArray(", "sum(cache_read_input_tokens)", "sum(cache_write_input_tokens)", "sum(search_requests)", "countIf(cache_status = 'hit')", "sum(usage.cost)", "usage.cost >= {min_cost:Float64}", "usage.cost <= {max_cost:Float64}", "GROUP BY group_id,currency", "before_group_id:String", "LIMIT 3"} {
			if !strings.Contains(query, expected) {
				t.Fatalf("group query missing %q: %s", expected, query)
			}
		}
		if r.URL.Query().Get("param_tag") != "production' OR 1=1" || r.URL.Query().Get("param_min_cost") != "0.01" || r.URL.Query().Get("param_max_cost") != "0.2" || r.URL.Query().Get("param_before_group_id") != "session-3" || r.URL.Query().Get("param_before_currency") != "USD" {
			t.Fatalf("group parameters were not bound: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(
			`{"group_id":"session-2","requests":3,"errors":1,"models":["gpt"],"providers":["azure"],"total_tokens":42,"cache_read_input_tokens":12,"cache_write_input_tokens":4,"search_requests":2,"cache_hits":2,"latency_ms":12.5,"cost":0.3,"currency":"USD","started_at":"2026-08-25T10:00:00Z","ended_at":"2026-08-25T12:00:00Z"}` + "\n" +
				`{"group_id":"session-1","requests":2,"errors":0,"models":["phi3"],"providers":["ollama"],"total_tokens":10,"cache_hits":0,"latency_ms":8,"cost":0,"currency":"USD","started_at":"2026-08-25T09:00:00Z","ended_at":"2026-08-25T11:00:00Z"}` + "\n" +
				`{"group_id":"older","requests":1,"errors":0,"models":[],"providers":[],"total_tokens":1,"cache_hits":0,"latency_ms":1,"cost":0,"currency":"EUR","started_at":"2026-08-24T09:00:00Z","ended_at":"2026-08-24T09:00:00Z"}` + "\n"))
	}))
	defer server.Close()
	reporter, err := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "safe_db", ClickHouseUsageEventsTable: "safe_events"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := reporter.ListRequestLogGroups(context.Background(), RequestLogGroupFilter{
		RequestLogFilter: RequestLogFilter{Days: 7, Limit: 2, Tag: "production' OR 1=1", MinCost: &minCost, MaxCost: &maxCost, Before: time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)},
		Dimension:        "session", BeforeGroupID: "session-3", BeforeCurrency: "USD",
	})
	if err != nil || len(page.Data) != 2 || page.Data[0].Requests != 3 || page.Data[0].CacheReadInputTokens != 12 || page.Data[0].CacheWriteInputTokens != 4 || page.Data[0].SearchRequests != 2 || page.Data[0].CacheHits != 2 || page.NextBefore != "2026-08-25T11:00:00Z" || page.NextBeforeGroupID != "session-1" || page.NextBeforeCurrency != "USD" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestClickHouseRequestLogGroupsRejectInvalidDimensionAndPartialCursor(t *testing.T) {
	reporter := &ClickHouseUsageReporter{client: http.DefaultClient}
	if _, err := reporter.ListRequestLogGroups(context.Background(), RequestLogGroupFilter{RequestLogFilter: RequestLogFilter{Days: 7, Limit: 10}, Dimension: "model"}); err == nil {
		t.Fatal("invalid group dimension accepted")
	}
	if _, err := reporter.ListRequestLogGroups(context.Background(), RequestLogGroupFilter{RequestLogFilter: RequestLogFilter{Days: 7, Limit: 10, Before: time.Now()}, Dimension: "trace", BeforeGroupID: "trace-1"}); err == nil {
		t.Fatal("partial group cursor accepted")
	}
}

func TestRequestLogFilterBounds(t *testing.T) {
	reporter := &ClickHouseUsageReporter{client: http.DefaultClient}
	if _, err := reporter.ListRequestLogs(context.Background(), RequestLogFilter{Days: 91, Limit: 10}); err == nil {
		t.Fatal("unbounded request log window accepted")
	}
	if _, err := reporter.ListRequestLogs(context.Background(), RequestLogFilter{Days: 7, Limit: 10, From: time.Now().Add(-91 * 24 * time.Hour), To: time.Now()}); err == nil {
		t.Fatal("unbounded custom request log window accepted")
	}
	if _, err := ParseRequestLogLimit("201", 100); err == nil {
		t.Fatal("unbounded request log page accepted")
	}
	minCost, maxCost := 2.0, 1.0
	if _, err := reporter.ListRequestLogs(context.Background(), RequestLogFilter{Days: 7, Limit: 10, MinCost: &minCost, MaxCost: &maxCost}); err == nil {
		t.Fatal("inverted cost range accepted")
	}
}
