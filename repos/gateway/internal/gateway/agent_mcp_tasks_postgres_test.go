package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/controlstore"
	"ai-gateway-gateway/internal/modules"
)

func TestPostgresAgentMCPApprovalReplicaCASAndBillingIntegration(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "JSON", true: "SSE"}[streaming], func(t *testing.T) {
			method := "SendMessage"
			resultTask := agentMCPResultTask
			if streaming {
				method, resultTask = "SendStreamingMessage", agentMCPStreamTask
			}
			store, pool, dsn := agentPostgresTestStore(t)
			_, err := pool.Exec(t.Context(), `CREATE TABLE gateway_a2a_tasks (
		id TEXT PRIMARY KEY, owner_key TEXT NOT NULL, agent_id TEXT NOT NULL, model TEXT NOT NULL, context_id TEXT NOT NULL,
		state TEXT NOT NULL, payload BYTEA NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 2097152),
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), expires_at TIMESTAMPTZ NOT NULL);
		CREATE TABLE gateway_mcp_tool_calls (
		scope_key TEXT NOT NULL, idempotency_key TEXT NOT NULL, request_hash TEXT NOT NULL, execution_id TEXT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('pending','completed')),http_status INTEGER NOT NULL DEFAULT 0,response BYTEA,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(scope_key,idempotency_key))`)
			if err != nil {
				t.Fatal("could not prepare isolated agent task tables")
			}
			h, llm, client := agentMCPApprovalHandler(t)
			llm.toolArguments = `{"city":"Paris","count":9007199254740993,"amount":0.1234567890123456789012345}`
			h = h.WithA2ATaskStore(store, A2ATaskRuntimeConfig{OwnerQuota: 10, TTL: time.Hour}).WithMCPCallStore(store)
			initial := agentMCPSend(h, method, "")
			first := resultTask(t, initial)
			if first.Status.State != "TASK_STATE_INPUT_REQUIRED" || client.callCalls != 0 {
				t.Fatal("PostgreSQL task did not pause before tools")
			}
			replicaStore, err := controlstore.NewPostgresStore(t.Context(), dsn, nil)
			if err != nil {
				t.Fatal("could not open a separate replica store")
			}
			t.Cleanup(replicaStore.Close)
			replica := h.WithA2ATaskStore(replicaStore, h.a2aTaskConfig).WithMCPCallStore(replicaStore)
			for name, response := range map[string]*httptest.ResponseRecorder{
				"initial":         initial,
				"replica refresh": agentMCPTaskRPC(replica, "GetTask", a2aMessage{}, first.ID),
				"replica replay":  agentMCPSend(replica, method, ""),
			} {
				if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"count":9007199254740993`) || !strings.Contains(response.Body.String(), `"amount":0.1234567890123456789012345`) {
					t.Fatalf("%s rounded persisted approval arguments", name)
				}
			}
			if len(llm.requests) != 1 || client.callCalls != 0 {
				t.Fatal("replica reads or replay incurred new model or tool work")
			}
			// Both replicas read the same pending version; only one may claim it.
			stored, err := store.GetA2ATask(t.Context(), fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"}), "research", first.ID)
			if err != nil {
				t.Fatal("could not load persisted approval")
			}
			if _, err := replicaStore.GetA2ATask(t.Context(), "another-owner", "research", first.ID); !errors.Is(err, a2astate.ErrNotFound) {
				t.Fatal("task crossed owner boundary")
			}
			decision := agentMCPDecision(t, first, true, "approve")
			entered, release := make(chan struct{}), make(chan struct{})
			llm.afterResponse = func() { close(entered); <-release }
			var completed *httptest.ResponseRecorder
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); completed = agentMCPTaskRPC(replica, method, decision, "") }()
			<-entered
			duplicate := agentMCPTaskRPC(h, method, decision, "")
			conflict := agentMCPTaskRPC(h, method, agentMCPDecision(t, first, true, "other-decision"), "")
			if _, err := store.UpdateA2ATask(t.Context(), stored, stored.UpdatedAt, time.Hour); !errors.Is(err, a2astate.ErrConflict) {
				close(release)
				wg.Wait()
				t.Fatal("stale PostgreSQL task update was accepted")
			}
			close(release)
			wg.Wait()
			if completed.Code != http.StatusOK || duplicate.Code != http.StatusOK || conflict.Code != http.StatusConflict || client.callCalls != 1 || len(llm.requests) != 2 {
				t.Fatal("replica approvals executed duplicate effects")
			}
			if resultTask(t, completed).Status.State != "TASK_STATE_COMPLETED" || resultTask(t, duplicate).Status.State != "TASK_STATE_WORKING" {
				t.Fatal("replica response did not retain its claimed task status")
			}
			if client.callArgs["count"] != json.Number("9007199254740993") || client.callArgs["amount"] != json.Number("0.1234567890123456789012345") {
				t.Fatal("replica approval executed rounded numeric arguments")
			}
			if llm.requests[0].RequestID == llm.requests[1].RequestID {
				t.Fatal("independent model steps shared billing identity")
			}
			var state string
			var rows int
			if pool.QueryRow(t.Context(), `SELECT count(*) FROM gateway_mcp_tool_calls WHERE state='completed'`).Scan(&rows) != nil || rows != 1 {
				t.Fatal("MCP execution was not durably deduplicated")
			}
			if pool.QueryRow(t.Context(), `SELECT state FROM gateway_a2a_tasks WHERE id=$1`, first.ID).Scan(&state) != nil || state != "TASK_STATE_COMPLETED" {
				t.Fatal("replica completion was not persisted")
			}
			if resultTask(t, agentMCPSend(h, method, "")).Status.State != "TASK_STATE_COMPLETED" || len(llm.requests) != 2 {
				t.Fatal("durable initial replay incurred new model work")
			}
			if strings.Contains(completed.Body.String(), "agentMcp") || strings.Contains(completed.Body.String(), "mcp.example") {
				t.Fatal("private execution state leaked")
			}
		})
	}
}
