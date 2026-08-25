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

type fakeBudgetManager struct {
	created modules.BudgetPolicySpec
	summary modules.BudgetSummary
}

func (f *fakeBudgetManager) ListBudgetPolicies(context.Context) ([]modules.ManagedBudgetPolicy, error) {
	return []modules.ManagedBudgetPolicy{{ID: 1}}, nil
}
func (f *fakeBudgetManager) GetBudgetPolicy(context.Context, int64) (modules.ManagedBudgetPolicy, bool, error) {
	return modules.ManagedBudgetPolicy{ID: 1}, true, nil
}
func (f *fakeBudgetManager) CreateBudgetPolicy(_ context.Context, s modules.BudgetPolicySpec) (modules.ManagedBudgetPolicy, error) {
	f.created = s
	return modules.ManagedBudgetPolicy{ID: 1, ScopeType: s.ScopeType}, nil
}
func (f *fakeBudgetManager) UpdateBudgetPolicy(context.Context, int64, modules.BudgetPolicySpec) (modules.ManagedBudgetPolicy, bool, error) {
	return modules.ManagedBudgetPolicy{ID: 1}, true, nil
}
func (f *fakeBudgetManager) DisableBudgetPolicy(context.Context, int64) (bool, error) {
	return true, nil
}
func (f *fakeBudgetManager) BudgetSummary(context.Context, int64, time.Time) (modules.BudgetSummary, bool, error) {
	return f.summary, true, nil
}

func TestBudgetManagementRequiresScopedSecret(t *testing.T) {
	mux := http.NewServeMux()
	registerBudgetManagement(mux, &fakeBudgetManager{}, nil, "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/budgets", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestBudgetManagementCreatesPolicy(t *testing.T) {
	manager := &fakeBudgetManager{}
	mux := http.NewServeMux()
	registerBudgetManagement(mux, manager, nil, "secret")
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/budgets", strings.NewReader(`{"scope_type":"team","scope_id":"t1","period":"month","currency":"USD","max_cost":10}`))
	request.Header.Set("X-Management-Token", "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || manager.created.ScopeID != "t1" {
		t.Fatalf("status=%d spec=%+v", response.Code, manager.created)
	}
}

func TestBudgetManagementRejectsUnknownFieldsAndBadID(t *testing.T) {
	mux := http.NewServeMux()
	registerBudgetManagement(mux, &fakeBudgetManager{}, nil, "secret")
	for _, tc := range []struct{ method, path, body string }{{http.MethodPost, "/internal/v1/budgets", `{"unknown":true}`}, {http.MethodGet, "/internal/v1/budgets/nope", ""}} {
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		request.Header.Set("X-Management-Token", "secret")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d", tc.path, response.Code)
		}
	}
}

func TestBillingUsageScopedSecret(t *testing.T) {
	for _, tc := range []struct {
		configured string
		provided   string
		allowed    bool
	}{{"", "", true}, {"secret", "wrong", false}, {"secret", "secret", true}} {
		request := httptest.NewRequest(http.MethodPost, "/usage", nil)
		request.Header.Set("X-Service-Token", tc.provided)
		response := httptest.NewRecorder()
		if got := authorizeBillingUsage(response, request, tc.configured); got != tc.allowed {
			t.Fatalf("configured=%q provided=%q got=%v", tc.configured, tc.provided, got)
		}
		if !tc.allowed && response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.Code)
		}
	}
}
