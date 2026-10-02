package modules

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClickHouseRequestLogDetailOrganizationIsParameterized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, "organization_id = {organization_id:String}") || strings.Contains(query, "org-a' OR") || r.URL.Query().Get("param_organization_id") != "org-a' OR 1=1" {
			t.Fatal("organization was not safely bound")
		}
		if r.URL.Query().Get("param_request_id") != "shared-id" {
			t.Fatal("request id missing")
		}
	}))
	t.Cleanup(server.Close)
	reporter, err := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "safe_db", ClickHouseUsageEventsTable: "safe_events"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reporter.GetRequestLogScoped(t.Context(), "shared-id", "org-a' OR 1=1"); !errors.Is(err, ErrRequestLogNotFound) {
		t.Fatal("foreign or absent event disclosed", err)
	}
	if _, err = reporter.GetRequestLogScoped(t.Context(), "shared-id", ""); err == nil {
		t.Fatal("empty organization accepted")
	}
}
