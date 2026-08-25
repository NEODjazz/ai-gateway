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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if strings.Contains(query, "req' OR 1=1") || !strings.Contains(query, "request_id = {request_id:String}") || !strings.Contains(query, "LIMIT 3") {
			t.Fatalf("unsafe request log query: %s", query)
		}
		if r.URL.Query().Get("param_request_id") != "req' OR 1=1" {
			t.Fatalf("request id was not bound as a parameter: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(
			`{"timestamp":"2026-08-25T12:00:00Z","request_id":"r3","status":"ok","api_type":"chat_completions","phase":"commit","latency_ms":4,"input_tokens":1,"output_tokens":2,"total_tokens":3,"cost":0.1,"currency":"USD","content_stored":false}` + "\n" +
				`{"timestamp":"2026-08-25T11:59:00Z","request_id":"r2","status":"error","failure_class":"upstream","api_type":"chat_completions","phase":"cancel","latency_ms":5,"input_tokens":0,"output_tokens":0,"total_tokens":0,"cost":0,"currency":"USD","content_stored":false}` + "\n" +
				`{"timestamp":"2026-08-25T11:58:00Z","request_id":"r1","status":"ok","api_type":"responses","phase":"commit","latency_ms":6,"input_tokens":3,"output_tokens":4,"total_tokens":7,"cost":0.2,"currency":"USD","content_stored":false}` + "\n"))
	}))
	defer server.Close()
	reporter, err := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "safe_db", ClickHouseUsageEventsTable: "safe_events"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := reporter.ListRequestLogs(context.Background(), RequestLogFilter{Days: 7, Limit: 2, RequestID: "req' OR 1=1", Before: time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC), BeforeRequestID: "cursor-request"})
	if err != nil || len(page.Data) != 2 || page.NextBefore != "2026-08-25T11:59:00Z" || page.NextRequestID != "r2" || page.Data[1].FailureClass != "upstream" || page.Data[0].ContentStored {
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

func TestRequestLogFilterBounds(t *testing.T) {
	reporter := &ClickHouseUsageReporter{client: http.DefaultClient}
	if _, err := reporter.ListRequestLogs(context.Background(), RequestLogFilter{Days: 91, Limit: 10}); err == nil {
		t.Fatal("unbounded request log window accepted")
	}
	if _, err := ParseRequestLogLimit("201", 100); err == nil {
		t.Fatal("unbounded request log page accepted")
	}
}
