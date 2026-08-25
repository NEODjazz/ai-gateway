package modules

import (
	"testing"
	"time"
)

func TestNormalizeBudgetPolicySpec(t *testing.T) {
	cost := 12.5
	spec, err := normalizeBudgetPolicySpec(BudgetPolicySpec{ScopeType: " TEAM ", ScopeID: " team-1 ", Period: "MONTH", MaxCost: &cost})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ScopeType != "team" || spec.ScopeID != "team-1" || spec.Period != "month" || spec.Currency != "USD" || spec.Enabled == nil || !*spec.Enabled {
		t.Fatalf("spec=%+v", spec)
	}
}

func TestNormalizeBudgetPolicySpecRejectsInvalidLimits(t *testing.T) {
	zero := float64(0)
	for _, spec := range []BudgetPolicySpec{{ScopeType: "global", ScopeID: "tenant", Period: "day", MaxCost: &zero}, {ScopeType: "team", ScopeID: "t", Period: "year", MaxCost: &zero}, {ScopeType: "team", ScopeID: "t", Period: "day"}} {
		if _, err := normalizeBudgetPolicySpec(spec); err == nil {
			t.Fatalf("accepted %+v", spec)
		}
	}
}

func TestBudgetPeriodEnd(t *testing.T) {
	start := budgetPeriodStart(time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC), "month")
	if got := budgetPeriodEnd(start, "month"); got.Month() == start.Month() {
		t.Fatalf("end=%v", got)
	}
}
