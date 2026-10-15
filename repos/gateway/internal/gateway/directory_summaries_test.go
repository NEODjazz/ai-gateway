package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type directorySummaryStub struct {
	directoryClientStub
	query DirectorySummaryQuery
	rows  []DirectorySummary
	err   error
	calls int
}

func (c *directorySummaryStub) DirectorySummaries(_ context.Context, _ ManagementAudit, q DirectorySummaryQuery) ([]DirectorySummary, error) {
	c.query = q
	c.calls++
	return c.rows, c.err
}

type budgetSummaryStub struct {
	recordingBudgetClient
	query DirectorySummaryQuery
	rows  []IdentityBudgetSummary
	err   error
}

func (c *budgetSummaryStub) IdentityBudgetSummaries(_ context.Context, _ ManagementAudit, q DirectorySummaryQuery) ([]IdentityBudgetSummary, error) {
	c.query = q
	c.calls++
	return c.rows, c.err
}

type identitySpendStub struct {
	recordingUsageClient
	query IdentitySpendQuery
	rows  []IdentitySpend
	err   error
	calls int
}

func (c *identitySpendStub) IdentitySpend(_ context.Context, _ ManagementAudit, q IdentitySpendQuery) ([]IdentitySpend, error) {
	c.query = q
	c.calls++
	return c.rows, c.err
}

func TestDirectorySummariesBatchAndPartialFailure(t *testing.T) {
	d := &directorySummaryStub{rows: []DirectorySummary{{ID: "late", KeyCount: 71, Organization: &OrganizationMetadata{ID: "owner", Name: "Owner", Status: "active"}}}}
	b := &budgetSummaryStub{rows: []IdentityBudgetSummary{{ID: "late", Total: 0, Data: []BudgetSummary{}}}}
	u := &identitySpendStub{rows: []IdentitySpend{{ID: "late", Currency: "EUR", Cost: 4, Requests: 2}, {ID: "late", Currency: "USD", Cost: 3, Requests: 1}}}
	a := NewAccessRegistry()
	if err := a.PutIdentityPolicy(IdentityInferencePolicy{ScopeType: "team", ScopeID: "late", ModelAccess: "allowlist", AllowedModels: []string{"a", "b", "c"}, RPM: 60, TPM: 100000}); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(modulesPipeline("admin"), nil).WithIdentityDirectory(d).WithBudgetManagement(b).WithUsageReporting(u).WithAccessRegistry(a)
	for _, failure := range []string{"", "directory", "budgets", "spend"} {
		d.err = nil
		b.err = nil
		u.err = nil
		switch failure {
		case "directory":
			d.err = errors.New("private backend diagnostic")
		case "budgets":
			b.err = errors.New("private backend diagnostic")
		case "spend":
			u.err = errors.New("private backend diagnostic")
		}
		w := httptest.NewRecorder()
		Routes(h).ServeHTTP(w, httptest.NewRequest("POST", "/admin/v1/directory-summaries", strings.NewReader(`{"scope_type":"team","ids":["late"]}`)))
		var response struct {
			Data   []identityDirectorySummary `json:"data"`
			Errors map[string]string          `json:"errors"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Data) != 1 || strings.Contains(w.Body.String(), "private backend diagnostic") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		row := response.Data[0]
		if row.Policy == nil || row.Policy.ModelCount != 3 || len(row.Policy.AllowedModels) != 2 || row.Policy.RPM != 60 || row.Policy.TPM != 100000 {
			t.Fatalf("model policy preview lost: %+v", row.Policy)
		}
		if (row.Directory == nil) != (failure == "directory") || (row.Budgets == nil) != (failure == "budgets") || (row.Spend == nil) != (failure == "spend") || (failure != "" && response.Errors[failure] == "") {
			t.Fatalf("partial failure hid independent sources: %+v", response)
		}
		if d.query.IDs[0] != "late" || b.query.IDs[0] != "late" || u.query.To.Sub(u.query.From) != 30*24*time.Hour {
			t.Fatal("batch scope or spend period lost")
		}
	}
}

func TestDirectorySummariesRejectScopedActorsAndInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		role, body string
		status     int
	}{{"team_admin", `{"scope_type":"team","ids":["team-a"]}`, 403}, {"org_admin", `{"scope_type":"user","ids":["a"]}`, 403}, {"user", `{"scope_type":"user","ids":["a"]}`, 403}, {"admin", `{"scope_type":"global","ids":["*"]}`, 400}, {"admin", `{"scope_type":"team","ids":["a","a"]}`, 400}, {"admin", `{"scope_type":"team","ids":["a"],"extra":1}`, 400}} {
		d := &directorySummaryStub{}
		w := httptest.NewRecorder()
		Routes(NewHandler(modulesPipeline(tc.role), nil).WithIdentityDirectory(d)).ServeHTTP(w, httptest.NewRequest("POST", "/admin/v1/directory-summaries", strings.NewReader(tc.body)))
		if w.Code != tc.status || d.calls != 0 {
			t.Fatalf("role=%s status=%d calls=%d", tc.role, w.Code, d.calls)
		}
	}
}

func TestDirectorySummariesDiscardForeignOrIncompleteSourceData(t *testing.T) {
	for _, malformed := range []string{"directory", "budgets", "spend"} {
		d := &directorySummaryStub{rows: []DirectorySummary{{ID: "foreign", KeyCount: 999, Organization: &OrganizationMetadata{ID: "foreign-owner", Name: "Foreign Owner", Status: "active"}}}}
		b := &budgetSummaryStub{rows: []IdentityBudgetSummary{{ID: "a", Total: 1, Data: []BudgetSummary{{Policy: ManagedBudgetPolicy{ID: 1, ScopeType: "user", ScopeID: "foreign"}}}}}}
		u := &identitySpendStub{rows: []IdentitySpend{{ID: "foreign", Currency: "USD", Cost: 999, Requests: 1}}}
		h := NewHandler(modulesPipeline("admin"), nil).WithAccessRegistry(NewAccessRegistry())
		switch malformed {
		case "directory":
			h = h.WithIdentityDirectory(d)
		case "budgets":
			h = h.WithBudgetManagement(b)
		case "spend":
			h = h.WithUsageReporting(u)
		}
		w := httptest.NewRecorder()
		Routes(h).ServeHTTP(w, httptest.NewRequest("POST", "/admin/v1/directory-summaries", strings.NewReader(`{"scope_type":"team","ids":["a"]}`)))
		if w.Code != 200 || strings.Contains(w.Body.String(), "foreign") || !strings.Contains(w.Body.String(), `"`+malformed+`":null`) {
			t.Fatalf("foreign source escaped: %s", w.Body.String())
		}
	}
}

func TestRemoteDirectorySummaryRPCPayloadAndLargeBoundedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q DirectorySummaryQuery
		if r.Method != "POST" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Token") != "fixture-secret" || r.Header.Get("X-Request-ID") != "summary-request" || json.NewDecoder(r.Body).Decode(&q) != nil || len(q.IDs) != 200 {
			t.Error("summary RPC lost service auth, audit or IDs")
			w.WriteHeader(400)
			return
		}
		switch r.URL.Path {
		case "/internal/v1/directory-summaries":
			rows := make([]DirectorySummary, 200)
			for i, id := range q.IDs {
				rows[i] = DirectorySummary{ID: id, KeyCount: 71, Organization: &OrganizationMetadata{ID: "owner", Name: "Owner", Status: "active", Description: strings.Repeat("x", 1024)}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": rows})
		case "/internal/v1/budgets/identities":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []IdentityBudgetSummary{}})
		case "/internal/v1/usage/identities":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []IdentitySpend{}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	q := DirectorySummaryQuery{ScopeType: "team"}
	for i := range 200 {
		q.IDs = append(q.IDs, fmt.Sprintf("team-%d", i))
	}
	audit := ManagementAudit{RequestID: "summary-request", ActorID: "operator", CredentialID: "fixture-key", Roles: []string{"admin"}}
	rows, err := NewRemoteManagementClient(server.URL, "fixture-secret").DirectorySummaries(t.Context(), audit, q)
	if err != nil || len(rows) != 200 || rows[199].KeyCount != 71 {
		t.Fatalf("large valid page truncated: count=%d err=%v", len(rows), err)
	}
	client := NewRemoteBudgetManagementClient(server.URL, "fixture-secret")
	if _, err := client.IdentityBudgetSummaries(t.Context(), audit, q); err != nil {
		t.Fatal(err)
	}
	if _, err := client.IdentitySpend(t.Context(), audit, IdentitySpendQuery{DirectorySummaryQuery: q}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteDirectoryPageAcceptsValidLargePages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/v1/users") {
			rows := make([]DirectoryUser, 200)
			for i := range rows {
				rows[i] = DirectoryUser{ID: fmt.Sprintf("user-%d", i), Name: strings.Repeat("n", 256), Email: strings.Repeat("e", 300) + "@example.test", Status: "active"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": rows, "total": 620, "filtering_version": 1})
		} else {
			rows := make([]DirectoryTeam, 200)
			for i := range rows {
				rows[i] = DirectoryTeam{ID: fmt.Sprintf("team-%d", i), Name: strings.Repeat("n", 256), Description: strings.Repeat("d", 1024), Status: "active"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": rows, "total": 620, "filtering_version": 1})
		}
	}))
	defer server.Close()
	c := NewRemoteManagementClient(server.URL, "fixture-secret")
	q := DirectoryPageFilter{Offset: 400, Limit: 200, Status: "active"}
	users, total, err := c.ListUsersPage(t.Context(), ManagementAudit{}, q)
	if err != nil || len(users) != 200 || total != 620 {
		t.Fatalf("valid user page truncated: count=%d total=%d err=%v", len(users), total, err)
	}
	teams, total, err := c.ListTeamsPage(t.Context(), ManagementAudit{}, q)
	if err != nil || len(teams) != 200 || total != 620 {
		t.Fatalf("valid team page truncated: count=%d total=%d err=%v", len(teams), total, err)
	}
}

func TestRemoteSummaryClientsRejectOmittedCountsInsteadOfInventingZeros(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":[{"id":"a"}]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		q := DirectorySummaryQuery{ScopeType: "team", IDs: []string{"a"}}
		_, directoryErr := NewRemoteManagementClient(server.URL, "fixture-secret").DirectorySummaries(t.Context(), ManagementAudit{}, q)
		c := NewRemoteBudgetManagementClient(server.URL, "fixture-secret")
		_, budgetErr := c.IdentityBudgetSummaries(t.Context(), ManagementAudit{}, q)
		_, spendErr := c.IdentitySpend(t.Context(), ManagementAudit{}, IdentitySpendQuery{DirectorySummaryQuery: q})
		server.Close()
		if directoryErr == nil || budgetErr == nil || spendErr == nil {
			t.Fatal("omitted counts were presented as valid zeros")
		}
	}
}
