package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-billing/internal/modules"
)

type identityBudgetManager struct {
	fakeBudgetManager
	query modules.IdentityBudgetQuery
	calls int
}

func (m *identityBudgetManager) IdentityBudgetPage(_ context.Context, q modules.IdentityBudgetQuery, _ time.Time) (modules.IdentityBudgetPage, error) {
	m.query = q
	m.calls++
	return modules.IdentityBudgetPage{Data: []modules.BudgetSummary{}, Total: 60, Offset: q.Offset, Limit: q.Limit}, nil
}

func TestIdentityBudgetPageRequiresSecretAndValidBoundedScope(t *testing.T) {
	manager := &identityBudgetManager{}
	mux := http.NewServeMux()
	registerBudgetManagement(mux, manager, nil, "fixture-secret")
	for _, tc := range []struct {
		query, secret string
		status        int
	}{
		{"scope_type=team&scope_id=platform&offset=50&limit=25", "fixture-secret", 200},
		{"scope_type=user&scope_id=alice", "", 401},
		{"scope_type=global&scope_id=*", "fixture-secret", 400},
		{"scope_type=team&scope_id=platform&limit=101", "fixture-secret", 400},
		{"scope_type=team&scope_id=platform&offset=bad", "fixture-secret", 400},
	} {
		before := manager.calls
		r := httptest.NewRequest(http.MethodGet, "/internal/v1/budgets/page?"+tc.query, nil)
		r.Header.Set("X-Management-Token", tc.secret)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.status || (tc.status != 200 && manager.calls != before) {
			t.Fatalf("status=%d query=%s", w.Code, tc.query)
		}
		if tc.status == 200 && (manager.query.ScopeID != "platform" || manager.query.Offset != 50 || !strings.Contains(w.Body.String(), `"total":60`)) {
			t.Fatal("scope or page lost")
		}
	}
}
