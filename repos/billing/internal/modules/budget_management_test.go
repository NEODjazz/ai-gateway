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
	organizationSpec, err := normalizeBudgetPolicySpec(BudgetPolicySpec{ScopeType: " ORGANIZATION ", ScopeID: " org-1 ", Period: "DAY", MaxCost: &cost})
	if err != nil || organizationSpec.ScopeType != "organization" || organizationSpec.ScopeID != "org-1" {
		t.Fatalf("organization spec=%+v err=%v", organizationSpec, err)
	}
	tagSpec, err := normalizeBudgetPolicySpec(BudgetPolicySpec{ScopeType: " TAG ", ScopeID: " production ", Period: "DAY", MaxCost: &cost})
	if err != nil || tagSpec.ScopeType != "tag" || tagSpec.ScopeID != "production" {
		t.Fatalf("tag spec=%+v err=%v", tagSpec, err)
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

func TestProjectKeyBudgetsIncludesEveryApplicableIdentityPolicy(t *testing.T) {
	summaries := []BudgetSummary{
		{Policy: ManagedBudgetPolicy{ID: 1, ScopeType: "global", ScopeID: "*"}},
		{Policy: ManagedBudgetPolicy{ID: 2, ScopeType: "organization", ScopeID: "org-1"}},
		{Policy: ManagedBudgetPolicy{ID: 3, ScopeType: "team", ScopeID: "team-1"}},
		{Policy: ManagedBudgetPolicy{ID: 4, ScopeType: "user", ScopeID: "user-1"}},
		{Policy: ManagedBudgetPolicy{ID: 5, ScopeType: "key", ScopeID: "key-1"}},
		{Policy: ManagedBudgetPolicy{ID: 6, ScopeType: "model", ScopeID: "gpt-5"}},
	}
	projections := projectKeyBudgets([]KeyBudgetSubject{{KeyID: "key-1", UserID: "user-1", TeamID: "team-1", OrganizationID: "org-1"}, {KeyID: "key-2"}}, summaries)
	if len(projections) != 2 || len(projections[0].Policies) != 5 || len(projections[1].Policies) != 1 {
		t.Fatalf("projections=%+v", projections)
	}
}

func TestNormalizeKeyBudgetSubjectsRejectsUnsafeOrDuplicateInput(t *testing.T) {
	for _, subjects := range [][]KeyBudgetSubject{nil, {{KeyID: ""}}, {{KeyID: "key-1"}, {KeyID: " key-1 "}}} {
		if _, err := normalizeKeyBudgetSubjects(subjects); err == nil {
			t.Fatalf("accepted subjects=%+v", subjects)
		}
	}
}
