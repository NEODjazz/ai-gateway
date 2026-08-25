package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

type recordingBudgetClient struct {
	audit ManagementAudit
	spec  BudgetPolicySpec
	id    int64
	calls int
}

func (c *recordingBudgetClient) List(context.Context, ManagementAudit) ([]ManagedBudgetPolicy, error) {
	return []ManagedBudgetPolicy{{ID: 1}}, nil
}
func (c *recordingBudgetClient) Get(context.Context, ManagementAudit, int64) (ManagedBudgetPolicy, error) {
	return ManagedBudgetPolicy{ID: 1}, nil
}
func (c *recordingBudgetClient) Create(_ context.Context, a ManagementAudit, s BudgetPolicySpec) (ManagedBudgetPolicy, error) {
	c.audit = a
	c.spec = s
	c.calls++
	return ManagedBudgetPolicy{ID: 1, ScopeType: s.ScopeType}, nil
}
func (c *recordingBudgetClient) Update(context.Context, ManagementAudit, int64, BudgetPolicySpec) (ManagedBudgetPolicy, error) {
	return ManagedBudgetPolicy{ID: 1}, nil
}
func (c *recordingBudgetClient) Disable(context.Context, ManagementAudit, int64) error { return nil }
func (c *recordingBudgetClient) Summary(context.Context, ManagementAudit, int64) (BudgetSummary, error) {
	return BudgetSummary{Policy: ManagedBudgetPolicy{ID: 1}}, nil
}

func TestAdminBudgetCreateRequiresAdminAndCarriesAudit(t *testing.T) {
	client := &recordingBudgetClient{}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithBudgetManagement(client)
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/budgets", strings.NewReader(`{"scope_type":"team","scope_id":"t1","period":"month","max_cost":10}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-Request-ID", "req-budget")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || client.calls != 1 || client.audit.RequestID != "req-budget" || client.spec.ScopeID != "t1" {
		t.Fatalf("status=%d client=%+v", response.Code, client)
	}
}

func TestAdminBudgetRejectsInvalidIDAndUnknownField(t *testing.T) {
	client := &recordingBudgetClient{}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithBudgetManagement(client)
	for _, tc := range []struct{ method, path, body string }{{http.MethodGet, "/admin/v1/budgets/nope", ""}, {http.MethodPost, "/admin/v1/budgets", `{"secret":"no"}`}} {
		response := httptest.NewRecorder()
		Routes(handler).ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d", tc.path, response.Code)
		}
	}
}

func TestRemoteBudgetManagementUsesOnlyScopedSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Token") != "billing-secret" || r.Header.Get("X-Actor-ID") != "admin" {
			t.Fatalf("headers=%v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "billing-secret")
	if _, err := client.List(context.Background(), ManagementAudit{ActorID: "admin"}); err != nil {
		t.Fatal(err)
	}
}

func modulesPipeline(role string) modules.Pipeline {
	return modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{role}}})
}
