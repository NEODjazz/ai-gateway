package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ai-gateway-gateway/internal/provider"
)

type memoryAdminStateController struct {
	mu       sync.Mutex
	payload  json.RawMessage
	revision int64
	conflict bool
}

func (c *memoryAdminStateController) AdminState(context.Context) (json.RawMessage, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append(json.RawMessage(nil), c.payload...), c.revision, nil
}

func (c *memoryAdminStateController) UpdateAdminState(_ context.Context, payload json.RawMessage) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conflict {
		return c.revision, provider.ErrControlPlaneConflict
	}
	c.revision++
	c.payload = append(json.RawMessage(nil), payload...)
	return c.revision, nil
}

func newTestAdminRuntime(t *testing.T, controller AdminStateController) (*AdminStateRuntime, *AccessRegistry, *MCPRegistry, *AgentRegistry, *LoggingRegistry) {
	t.Helper()
	access := NewAccessRegistry()
	mcp := NewMCPRegistry()
	agents := NewAgentRegistry()
	logging := newLoggingRegistryWithoutWorker(nil)
	runtime, err := NewAdminStateRuntime(context.Background(), controller, []byte("test-encryption-key"), access, mcp, agents, logging)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, access, mcp, agents, logging
}

func TestAdminStatePersistsEncryptedAndRestores(t *testing.T) {
	controller := &memoryAdminStateController{}
	runtime, access, mcp, agents, logging := newTestAdminRuntime(t, controller)
	handler := runtime.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := access.PutProject("project-a", Project{Name: "Project A", Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := mcp.PutServer("server-a", MCPServer{Label: "Server A", ServerURL: "https://mcp.example.test", Transport: "sse", Tools: []string{"mcp:server-a:*"}, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := agents.PutToolPolicy("policy-a", ToolPolicy{Name: "Policy A", AllowedTools: []string{"search"}, MaxToolCalls: 4, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := logging.Put("logs-a", LoggingDestination{Name: "Logs", Type: "webhook", URL: "https://logs.example.test/events", EventTypes: []string{"request_outcome"}, Enabled: true}, "do-not-store-plaintext"); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}), true)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/admin/v1/projects/project-a", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(string(controller.payload), "do-not-store-plaintext") {
		t.Fatal("logging secret was persisted in plaintext")
	}

	_, restoredAccess, restoredMCP, restoredAgents, restoredLogging := newTestAdminRuntime(t, controller)
	if len(restoredAccess.Projects()) != 1 || len(restoredMCP.Servers()) != 1 || len(restoredAgents.ToolPolicies()) != 1 {
		t.Fatalf("state was not restored: projects=%d servers=%d policies=%d", len(restoredAccess.Projects()), len(restoredMCP.Servers()), len(restoredAgents.ToolPolicies()))
	}
	restoredLogging.mu.RLock()
	secret := restoredLogging.destinations["logs-a"].secret
	restoredLogging.mu.RUnlock()
	if secret != "do-not-store-plaintext" {
		t.Fatalf("restored secret = %q", secret)
	}
}

func TestAdminStateConflictRollsBackMutation(t *testing.T) {
	controller := &memoryAdminStateController{conflict: true}
	runtime, access, _, _, _ := newTestAdminRuntime(t, controller)
	handler := runtime.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = access.PutProject("conflict", Project{Name: "Conflict", Enabled: true})
		w.WriteHeader(http.StatusOK)
	}), true)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/admin/v1/projects/conflict", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(access.Projects()) != 0 {
		t.Fatal("failed mutation was not rolled back")
	}
}

func TestAdminStateRefreshesAcrossReplicas(t *testing.T) {
	controller := &memoryAdminStateController{}
	runtimeA, accessA, _, _, _ := newTestAdminRuntime(t, controller)
	runtimeB, accessB, _, _, _ := newTestAdminRuntime(t, controller)
	write := runtimeA.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = accessA.PutProject("shared", Project{Name: "Shared", Enabled: true})
		w.WriteHeader(http.StatusOK)
	}), true)
	write.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/admin/v1/projects/shared", nil))
	read := runtimeB.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if len(accessB.Projects()) != 1 {
			t.Errorf("replica did not refresh durable state")
		}
		w.WriteHeader(http.StatusOK)
	}), false)
	read.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/v1/projects", nil))
}
