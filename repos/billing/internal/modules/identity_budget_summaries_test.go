package modules

import "testing"

func TestIdentityBudgetSummaryQueryValidation(t *testing.T) {
	for _, q := range []IdentityBudgetSummariesQuery{{ScopeType: "global", IDs: []string{"*"}}, {ScopeType: "team"}, {ScopeType: "user", IDs: []string{"a", "a"}}, {ScopeType: "user", IDs: []string{" bad"}}, {ScopeType: "team", IDs: make([]string, 201)}} {
		if q.Valid() {
			t.Fatalf("invalid query accepted: %+v", q)
		}
	}
	if !(IdentityBudgetSummariesQuery{ScopeType: "team", IDs: []string{"a", "b"}}).Valid() {
		t.Fatal("valid query rejected")
	}
}
