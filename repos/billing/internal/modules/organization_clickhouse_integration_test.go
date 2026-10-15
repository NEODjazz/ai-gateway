package modules

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClickHouseOrganizationReportingIsolationIntegration(t *testing.T) {
	endpoint := os.Getenv("BILLING_CLICKHOUSE_TEST_URL")
	if endpoint == "" {
		if os.Getenv("CLICKHOUSE_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("BILLING_CLICKHOUSE_TEST_URL is required")
		}
		t.Skip("BILLING_CLICKHOUSE_TEST_URL is not set")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" {
		t.Fatal("invalid test ClickHouse URL")
	}
	nonce := make([]byte, 8)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	database := "tenant_reporting_" + hex.EncodeToString(nonce)
	settings := Settings{UsageEventsEnabled: true, ClickHouseURL: endpoint, ClickHouseDatabase: database, ClickHouseUsageEventsTable: "usage_events", ClickHouseUsername: os.Getenv("BILLING_CLICKHOUSE_TEST_USERNAME"), ClickHousePassword: os.Getenv("BILLING_CLICKHOUSE_TEST_PASSWORD")}
	client := &http.Client{Timeout: 8 * time.Second}
	query := func(ctx context.Context, sql string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(sql))
		if err != nil {
			return err
		}
		req.SetBasicAuth(settings.ClickHouseUsername, settings.ClickHousePassword)
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if _, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20)); err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK {
			return errors.New("ClickHouse fixture statement failed")
		}
		return nil
	}
	if err = query(t.Context(), "CREATE DATABASE "+database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := query(ctx, "DROP DATABASE "+database); err != nil {
			t.Error(err)
		}
	})
	migrations, err := filepath.Glob(filepath.Join("..", "..", "migrations", "clickhouse", "*.sql"))
	if err != nil || len(migrations) == 0 {
		t.Fatal("ClickHouse migrations not found")
	}
	for _, path := range migrations {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(strings.ReplaceAll(string(raw), "ai_gateway", database), ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if err = query(t.Context(), statement); err != nil {
				t.Fatalf("migration %s: %v", filepath.Base(path), err)
			}
		}
	}
	writer := NewClickHouseUsageEventWriter(settings)
	now := time.Now().UTC().Truncate(time.Second)
	for n, event := range []struct {
		id, org string
		cost    float64
		tokens  int
	}{{"shared-id", "org-a", 1, 10}, {"a-second", "org-a", 2, 20}, {"shared-id", "org-b", 100, 1000}, {"unassigned", "", 1000, 10000}} {
		event := BillingEvent{Timestamp: now.Add(time.Duration(n-5) * time.Minute).Format(time.RFC3339), EventID: database + "-" + event.id + "-" + event.org, RequestID: event.id, OrganizationID: event.org, TeamID: "team-" + event.org, UserID: "shared-user", SessionID: "shared-session", Roles: []string{"org_admin"}, Tags: []string{"test"}, Provider: "fixture", ProviderID: "fixture", Model: "model", APIType: "chat.completions", Phase: "commit", Status: "success", InputTokens: event.tokens, TotalTokens: event.tokens, Cost: event.cost, Currency: "USD"}
		if err = writer.WriteUsageEvent(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	reporter, err := NewClickHouseUsageReporter(settings)
	if err != nil {
		t.Fatal(err)
	}
	reporter.now = func() time.Time { return now }
	report, err := reporter.ReportScoped(t.Context(), 1, UsageScope{Type: "organization", ID: "org-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Totals) != 1 || report.Totals[0].Requests != 2 || report.Totals[0].Cost != 3 || report.Totals[0].TotalTokens != 30 || len(report.ByOrganization) != 1 || report.ByOrganization[0].Name != "org-a" {
		t.Fatal("usage aggregates leaked foreign/unassigned tenant events")
	}
	filter := RequestLogFilter{Days: 1, Limit: 1, OrganizationID: "org-a"}
	page, err := reporter.ListRequestLogs(t.Context(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].RequestID != "a-second" || page.NextBefore == "" {
		t.Fatal("tenant page/cursor incorrect")
	}
	filter.Before, err = time.Parse(time.RFC3339Nano, page.NextBefore)
	if err != nil {
		t.Fatal(err)
	}
	filter.BeforeRequestID = page.NextRequestID
	page, err = reporter.ListRequestLogs(t.Context(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].RequestID != "shared-id" || page.Data[0].OrganizationID != "org-a" || page.Data[0].Cost != 1 || page.NextBefore != "" {
		t.Fatal("tenant pagination crossed boundary")
	}
	detail, err := reporter.GetRequestLogScoped(t.Context(), "shared-id", "org-a")
	if err != nil || detail.OrganizationID != "org-a" || detail.Cost != 1 {
		t.Fatal("detail picked newer foreign event", err)
	}
	if _, err = reporter.GetRequestLogScoped(t.Context(), "shared-id", "org-a' OR 1=1"); !errors.Is(err, ErrRequestLogNotFound) {
		t.Fatal("detail scope bypass", err)
	}
	groups, err := reporter.ListRequestLogGroups(t.Context(), RequestLogGroupFilter{RequestLogFilter: RequestLogFilter{Days: 1, Limit: 25, OrganizationID: "org-a"}, Dimension: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups.Data) != 1 || groups.Data[0].Requests != 2 || groups.Data[0].Cost != 3 || groups.Data[0].TotalTokens != 30 {
		t.Fatal("group aggregates crossed organization")
	}
	report, err = reporter.ReportScoped(t.Context(), 1, UsageScope{Type: "organization", ID: "org-a' OR 1=1"})
	if err != nil || len(report.Totals) != 0 {
		t.Fatal("usage scope was interpolated", err)
	}
	for _, extra := range []struct {
		id, phase, currency string
		cost                float64
		age                 time.Duration
	}{{"eur", "commit", "EUR", 4, time.Minute}, {"canceled", "cancel", "USD", 0, time.Minute}, {"reserve", "reserve", "USD", 999, time.Minute}, {"old", "commit", "USD", 999, 48 * time.Hour}} {
		event := BillingEvent{Timestamp: now.Add(-extra.age).Format(time.RFC3339), EventID: database + extra.id, RequestID: extra.id, TeamID: "team-org-a", UserID: "other-user", Phase: extra.phase, Status: "ok", Cost: extra.cost, Currency: extra.currency}
		if err := writer.WriteUsageEvent(t.Context(), event); err != nil {
			t.Fatal(err)
		}
		if extra.id == "eur" {
			if err := writer.WriteUsageEvent(t.Context(), event); err != nil {
				t.Fatal(err)
			}
		}
	}
	ids := []string{"team-org-a"}
	if err := writer.WriteUsageEvent(t.Context(), BillingEvent{Timestamp: now.Add(-time.Minute).Format(time.RFC3339), EventID: database + "-independent-execution", RequestID: "shared-id", OrganizationID: "org-a", TeamID: "team-org-a", UserID: "shared-user", Phase: "commit", Cost: 0.5, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 200; i++ {
		ids = append(ids, "absent-"+strconv.Itoa(i))
	}
	spend, err := reporter.IdentitySpend(t.Context(), IdentitySpendQuery{IdentityBudgetSummariesQuery: IdentityBudgetSummariesQuery{ScopeType: "team", IDs: ids}, From: now.Add(-24 * time.Hour), To: now})
	if err != nil || len(spend) != 2 {
		t.Fatalf("batch spend=%+v err=%v", spend, err)
	}
	for _, row := range spend {
		if row.ID != "team-org-a" || row.Currency == "USD" && (row.Cost != 3.5 || row.Requests != 4) || row.Currency == "EUR" && (row.Cost != 4 || row.Requests != 1) {
			t.Fatalf("incorrect finalized spend: %+v", row)
		}
	}
	spend, err = reporter.IdentitySpend(t.Context(), IdentitySpendQuery{IdentityBudgetSummariesQuery: IdentityBudgetSummariesQuery{ScopeType: "user", IDs: []string{"other-user"}}, From: now.Add(-24 * time.Hour), To: now})
	if err != nil || len(spend) != 2 || spend[0].ID != "other-user" {
		t.Fatalf("user spend escaped scope: %+v err=%v", spend, err)
	}
	spend, err = reporter.IdentitySpend(t.Context(), IdentitySpendQuery{IdentityBudgetSummariesQuery: IdentityBudgetSummariesQuery{ScopeType: "user", IDs: []string{"shared-user"}}, From: now.Add(-24 * time.Hour), To: now})
	if err != nil || len(spend) != 1 || spend[0].Cost != 1103.5 || spend[0].Requests != 5 {
		t.Fatalf("independent execution events were collapsed by external request ID: %+v err=%v", spend, err)
	}
	for i := range 2 {
		if err := writer.WriteUsageEvent(t.Context(), BillingEvent{Timestamp: now.Add(-time.Minute).Format(time.RFC3339), RequestID: "legacy-" + strconv.Itoa(i), UserID: "legacy-user", Phase: "commit", Cost: 0.5, Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
	}
	spend, err = reporter.IdentitySpend(t.Context(), IdentitySpendQuery{IdentityBudgetSummariesQuery: IdentityBudgetSummariesQuery{ScopeType: "user", IDs: []string{"legacy-user"}}, From: now.Add(-24 * time.Hour), To: now})
	if err != nil || len(spend) != 1 || spend[0].Cost != 1 || spend[0].Requests != 2 {
		t.Fatalf("legacy events without IDs were collapsed: %+v err=%v", spend, err)
	}
}
