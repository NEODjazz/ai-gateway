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
	audit    ManagementAudit
	spec     BudgetPolicySpec
	id       int64
	calls    int
	subjects []KeyBudgetSubject
}

func (c *recordingBudgetClient) List(context.Context, ManagementAudit) ([]ManagedBudgetPolicy, error) {
	return []ManagedBudgetPolicy{{ID: 1}}, nil
}
func (c *recordingBudgetClient) ListSummaries(context.Context, ManagementAudit) ([]BudgetSummary, error) {
	return []BudgetSummary{{Policy: ManagedBudgetPolicy{ID: 1, Currency: "USD"}, UsedCost: 2}}, nil
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
func (c *recordingBudgetClient) KeyProjections(_ context.Context, audit ManagementAudit, subjects []KeyBudgetSubject) ([]KeyBudgetProjection, error) {
	c.audit, c.subjects = audit, subjects
	return []KeyBudgetProjection{{KeyID: subjects[0].KeyID, Policies: []BudgetSummary{{Policy: ManagedBudgetPolicy{ID: 1, ScopeType: "team", ScopeID: subjects[0].TeamID}}}}}, nil
}

func TestAdminBudgetCreateRequiresAdminAndCarriesAudit(t *testing.T) {
	client := &recordingBudgetClient{}
	auditClient := &recordingAuditClient{}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithBudgetManagement(client).WithAudit(auditClient)
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/budgets", strings.NewReader(`{"scope_type":"team","scope_id":"t1","period":"month","max_cost":10}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-Request-ID", "req-budget")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || client.calls != 1 || client.audit.RequestID != "req-budget" || client.spec.ScopeID != "t1" || len(auditClient.events) != 2 || auditClient.events[1].TargetID != "1" {
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

func TestAdminBudgetListExpandsSummaries(t *testing.T) {
	client := &recordingBudgetClient{}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithBudgetManagement(client)
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/budgets?expand=summaries", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"summaries":{"1"`) || !strings.Contains(response.Body.String(), `"used_cost":2`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	invalid := httptest.NewRecorder()
	Routes(handler).ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/admin/v1/budgets?expand=secrets", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid expansion status=%d", invalid.Code)
	}
}

func TestRemoteBudgetManagementUsesOnlyScopedSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Token") != "billing-secret" || r.Header.Get("X-Actor-ID") != "admin" {
			t.Fatalf("headers=%v", r.Header)
		}
		if r.URL.Query().Get("expand") == "summaries" {
			_ = json.NewEncoder(w).Encode(map[string]any{"summaries": map[string]BudgetSummary{"2": {Policy: ManagedBudgetPolicy{ID: 2}}, "1": {Policy: ManagedBudgetPolicy{ID: 1}}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "billing-secret")
	if _, err := client.List(context.Background(), ManagementAudit{ActorID: "admin"}); err != nil {
		t.Fatal(err)
	}
	summaries, err := client.ListSummaries(context.Background(), ManagementAudit{ActorID: "admin"})
	if err != nil || len(summaries) != 2 || summaries[0].Policy.ID != 1 || summaries[1].Policy.ID != 2 {
		t.Fatalf("summaries=%+v err=%v", summaries, err)
	}
}

func TestRemoteBudgetManagementProjectsKeysWithoutClientBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/budgets/key-projections" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Token") != "billing-secret" {
			t.Fatalf("request=%s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var request struct {
			Subjects []KeyBudgetSubject `json:"subjects"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Subjects) != 1 || request.Subjects[0].KeyID != "key-1" {
			t.Fatalf("request=%+v err=%v", request, err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []KeyBudgetProjection{{KeyID: "key-1", Policies: []BudgetSummary{}}}})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "billing-secret")
	result, err := client.KeyProjections(context.Background(), ManagementAudit{ActorID: "admin"}, []KeyBudgetSubject{{KeyID: "key-1"}})
	if err != nil || len(result) != 1 || result[0].KeyID != "key-1" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func modulesPipeline(role string) modules.Pipeline {
	return modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{role}}})
}
