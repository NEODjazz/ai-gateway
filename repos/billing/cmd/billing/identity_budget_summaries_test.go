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

type identityBudgetSummaryManager struct {
	fakeBudgetManager
	query modules.IdentityBudgetSummariesQuery
	calls int
}

func (m *identityBudgetSummaryManager) IdentityBudgetSummaries(_ context.Context, q modules.IdentityBudgetSummariesQuery, _ time.Time) ([]modules.IdentityBudgetSummary, error) {
	m.query = q
	m.calls++
	return []modules.IdentityBudgetSummary{{ID: q.IDs[0], Total: 60, Data: []modules.BudgetSummary{}}}, nil
}

func TestIdentityBudgetSummaryHTTPValidationAndSecret(t *testing.T) {
	m := &identityBudgetSummaryManager{}
	mux := http.NewServeMux()
	registerBudgetManagement(mux, m, nil, "fixture-secret")
	for _, tc := range []struct {
		body, secret string
		status       int
	}{
		{`{"scope_type":"team","ids":["late-team"]}`, "fixture-secret", 200},
		{`{"scope_type":"team","ids":["late-team"]}`, "", 401},
		{`{"scope_type":"global","ids":["*"]}`, "fixture-secret", 400},
		{`{"scope_type":"team","ids":["a","a"]}`, "fixture-secret", 400},
		{`{"scope_type":"team","ids":[]}`, "fixture-secret", 400},
		{`{"scope_type":"team","ids":["a"],"extra":1}`, "fixture-secret", 400},
	} {
		before := m.calls
		r := httptest.NewRequest("POST", "/internal/v1/budgets/identities", strings.NewReader(tc.body))
		r.Header.Set("X-Management-Token", tc.secret)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.status || tc.status != 200 && m.calls != before {
			t.Fatalf("status=%d query=%s", w.Code, tc.body)
		}
		if tc.status == 200 && (!strings.Contains(w.Body.String(), `"total":60`) || m.query.IDs[0] != "late-team") {
			t.Fatal("scope or exact count lost")
		}
	}
}
