package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recordingIdentityBudgets struct {
	recordingBudgetClient
	query      IdentityBudgetQuery
	wrongScope bool
}

func (c *recordingIdentityBudgets) IdentityBudgetPage(_ context.Context, audit ManagementAudit, q IdentityBudgetQuery) (IdentityBudgetPage, error) {
	c.calls++
	c.query, c.audit = q, audit
	id := q.ScopeID
	if c.wrongScope {
		id = "foreign"
	}
	return IdentityBudgetPage{Data: []BudgetSummary{{Policy: ManagedBudgetPolicy{ID: 51, ScopeType: q.ScopeType, ScopeID: id}}}, Total: 60, Offset: q.Offset, Limit: q.Limit}, nil
}

func TestIdentityBudgetPagesScopeBoundsAndAuthorization(t *testing.T) {
	client := &recordingIdentityBudgets{}
	for _, tc := range []struct {
		role, query string
		status      int
	}{
		{"admin", "scope_type=team&scope_id=platform&offset=50&limit=25", 200},
		{"team_admin", "scope_type=team&scope_id=platform", 403},
		{"org_admin", "scope_type=user&scope_id=platform", 403},
		{"user", "scope_type=user&scope_id=platform", 403},
		{"admin", "scope_type=global&scope_id=*", 400},
		{"admin", "scope_type=team", 400},
		{"admin", "scope_type=team&scope_id=x&limit=101", 400},
		{"admin", "scope_type=team&scope_id=x&offset=-1", 400},
		{"admin", "scope_type=team&scope_id=x&offset=bad", 400},
	} {
		before := client.calls
		handler := NewHandler(modulesPipeline(tc.role), modelsProvider{}).WithBudgetManagement(client)
		recorder := httptest.NewRecorder()
		Routes(handler).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/v1/budgets/page?"+tc.query, nil))
		if recorder.Code != tc.status || (tc.status != 200 && client.calls != before) {
			t.Fatalf("role=%s query=%s status=%d calls=%d", tc.role, tc.query, recorder.Code, client.calls)
		}
		if tc.status == 200 && (client.query.ScopeID != "platform" || client.query.Offset != 50 || !strings.Contains(recorder.Body.String(), `"total":60`)) {
			t.Fatal("scope, paging or totals lost")
		}
	}
	client.wrongScope = true
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithBudgetManagement(client)
	w := httptest.NewRecorder()
	Routes(handler).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/v1/budgets/page?scope_type=team&scope_id=platform", nil))
	if w.Code != 502 || strings.Contains(w.Body.String(), "foreign") {
		t.Fatal("foreign budget passed through")
	}
}

func TestRemoteIdentityBudgetPagesPreserveFiltersAndScopedAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/internal/v1/budgets/page" || q.Get("scope_type") != "user" || q.Get("scope_id") != "user +1" || q.Get("offset") != "50" || q.Get("limit") != "25" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Token") != "fixture-secret" || r.Header.Get("X-Actor-ID") != "operator" {
			t.Error("filters or scoped authentication changed")
		}
		_ = json.NewEncoder(w).Encode(IdentityBudgetPage{Data: []BudgetSummary{}, Total: 60, Offset: 50, Limit: 25})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "fixture-secret")
	page, err := client.IdentityBudgetPage(t.Context(), ManagementAudit{ActorID: "operator"}, IdentityBudgetQuery{ScopeType: "user", ScopeID: "user +1", Offset: 50, Limit: 25})
	if err != nil || page.Total != 60 || page.Offset != 50 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}
