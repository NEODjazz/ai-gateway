package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recordingAuditClient struct {
	events    []AuditEvent
	appendErr error
	listed    []AuditEvent
}

func (c *recordingAuditClient) AppendAudit(_ context.Context, _ ManagementAudit, event AuditEvent) (AuditEvent, error) {
	if c.appendErr != nil {
		return AuditEvent{}, c.appendErr
	}
	c.events = append(c.events, event)
	return event, nil
}
func (c *recordingAuditClient) ListAudit(context.Context, ManagementAudit, AuditFilter) ([]AuditEvent, error) {
	return c.listed, nil
}

func TestManagementMutationWritesAttemptAndOutcomeAuditEvents(t *testing.T) {
	management := &recordingManagementClient{}
	audit := &recordingAuditClient{}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithManagement(management).WithAudit(audit)
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"user_id":"user-1"}`))
	request.Header.Set("X-Request-ID", "req-audit")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || len(audit.events) != 2 || audit.events[0].Outcome != "attempted" || audit.events[1].Outcome != "succeeded" || audit.events[1].TargetID != "vk_created" {
		t.Fatalf("status=%d events=%+v", response.Code, audit.events)
	}
}

func TestUnavailableAuditBlocksMutationBeforeSideEffect(t *testing.T) {
	management := &recordingManagementClient{}
	audit := &recordingAuditClient{appendErr: errors.New("postgres down")}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithManagement(management).WithAudit(audit)
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"user_id":"user-1"}`)))
	if response.Code != http.StatusServiceUnavailable || management.creates != 0 {
		t.Fatalf("status=%d creates=%d", response.Code, management.creates)
	}
}

func TestAdminListsAuditEvents(t *testing.T) {
	audit := &recordingAuditClient{listed: []AuditEvent{{ID: 2, Action: "budget.update", Outcome: "succeeded"}}}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithAudit(audit)
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/audit/events?limit=10", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "budget.update") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRemoteAuditClientUsesScopedHeadersAndFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Management-Token") != "secret" || r.Header.Get("X-Actor-ID") != "admin" {
			t.Fatalf("headers=%v", r.Header)
		}
		if r.URL.Query().Get("limit") != "25" || r.URL.Query().Get("action") != "budget.update" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []AuditEvent{{ID: 1}}})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "secret")
	events, err := client.ListAudit(context.Background(), ManagementAudit{RequestID: "r", ActorID: "admin", CredentialID: "c"}, AuditFilter{Limit: 25, Action: "budget.update"})
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestRemoteAuditAppendSendsOnlyMutableEventFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"id", "occurred_at", "request_id", "actor_id", "actor_credential_id"} {
			if _, found := body[forbidden]; found {
				t.Errorf("append body contains read-only field %q: %+v", forbidden, body)
			}
		}
		if body["action"] != "budget.update" || body["outcome"] != "attempted" {
			t.Errorf("unexpected append body: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(AuditEvent{ID: 1, OccurredAt: time.Now(), RequestID: "req-1", ActorID: "admin", ActorCredentialID: "credential", Action: "budget.update", TargetType: "budget", Outcome: "attempted"})
	}))
	defer server.Close()
	client := NewRemoteBudgetManagementClient(server.URL, "secret")
	if _, err := client.AppendAudit(context.Background(), ManagementAudit{RequestID: "req-1", ActorID: "admin", CredentialID: "credential"}, AuditEvent{Action: "budget.update", TargetType: "budget", Outcome: "attempted"}); err != nil {
		t.Fatal(err)
	}
}
