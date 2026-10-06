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

	"ai-gateway-gateway/internal/mcpstate"
	"ai-gateway-gateway/internal/modules"
)

type observingAdminStateController struct {
	*memoryAdminStateController
	read    func()
	failure error
}

func (c *observingAdminStateController) AdminState(ctx context.Context) (json.RawMessage, int64, error) {
	if c.read != nil {
		c.read()
	}
	if c.failure != nil {
		return nil, 0, c.failure
	}
	return c.memoryAdminStateController.AdminState(ctx)
}
func persistRegistryFixture(t *testing.T, runtime *AdminStateRuntime) {
	t.Helper()
	response := httptest.NewRecorder()
	runtime.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), true).ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/admin/v1/agent-profiles/fixture", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("persist fixture: status=%d", response.Code)
	}
}

func TestPublicAgentAndCatalogRefreshSavedConfiguration(t *testing.T) {
	controller := &memoryAdminStateController{}
	writer, _, _, agents, _ := newTestAdminRuntime(t, controller)
	reader, access, mcp, replicaAgents, _ := newTestAdminRuntime(t, controller)
	if _, err := agents.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"lookup"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.PutAgentProfile("research", agentFixture()); err != nil {
		t.Fatal(err)
	}
	persistRegistryFixture(t, writer)
	llm := &a2aTestProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"test-model"}}}), llm).WithAgentRegistry(replicaAgents).WithAccessRegistry(access).WithMCPRegistry(mcp).WithAdminState(reader)
	router := Routes(handler)
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"rpc","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"message","role":"ROLE_USER","parts":[{"text":"Hello"}]}}}`))
	request.Header.Set("A2A-Version", "1.0")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || llm.request.ResponseRequest == nil || llm.request.ResponseRequest.Instructions != agentFixtureInstructions {
		t.Fatal("public execution did not refresh another replica's saved configuration")
	}
	disabled := agentFixture()
	disabled.Enabled = false
	if _, err := agents.PutAgentProfile("research", disabled); err != nil {
		t.Fatal(err)
	}
	persistRegistryFixture(t, writer)
	catalog := httptest.NewRecorder()
	router.ServeHTTP(catalog, httptest.NewRequest(http.MethodGet, "/v1/playground/catalog", nil))
	if catalog.Code != http.StatusOK || strings.Contains(catalog.Body.String(), `"research"`) {
		t.Fatal("catalog kept a disabled agent from an older revision")
	}
	card := httptest.NewRecorder()
	router.ServeHTTP(card, httptest.NewRequest(http.MethodGet, "/a2a/research/.well-known/agent-card.json", nil))
	if card.Code != http.StatusNotFound {
		t.Fatal("public discovery kept a disabled agent")
	}
}

func TestPublicMCPRefreshesDisabledServerBeforeDiscoveryOrExecution(t *testing.T) {
	controller := &memoryAdminStateController{}
	writer, _, registry, _, _ := newTestAdminRuntime(t, controller)
	reader, access, replica, _, _ := newTestAdminRuntime(t, controller)
	server := MCPServer{Label: "Weather", ServerURL: "https://mcp.example.test/v1", Transport: "streamable-http", Tools: []string{"mcp:weather@https://mcp.example.test/v1"}, Enabled: true}
	if _, err := registry.PutServer("weather", server); err != nil {
		t.Fatal(err)
	}
	persistRegistryFixture(t, writer)
	client := &fakeMCPRuntimeClient{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{mcpRuntimeAuth{}}), nil).WithAdminState(reader).WithAccessRegistry(access).WithMCPRegistry(replica).WithMCPCallStore(mcpstate.NewMemoryStore(10, time.Hour)).WithAudit(&recordingAuditClient{}).WithMCPRuntimeFactory(func(string, string, string) (MCPRuntimeClient, error) { return client, nil })
	router := Routes(handler)
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/v1/mcp/servers/weather/tools", nil))
	if first.Code != http.StatusOK || client.calls != 1 {
		t.Fatal("initial server discovery failed")
	}
	server.Enabled = false
	if _, err := registry.PutServer("weather", server); err != nil {
		t.Fatal(err)
	}
	persistRegistryFixture(t, writer)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path, body := "/v1/mcp/servers/weather/tools", ""
		if method == http.MethodPost {
			path += "/lookup"
			body = `{"arguments":{}}`
		}
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "disabled-server")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || client.calls != 1 || client.callCalls != 0 {
			t.Fatal("disabled server was contacted from a stale replica")
		}
	}
}

func TestAdminStatePublicRefreshDoesNotHoldLockDuringControllerIOAndFailsClosed(t *testing.T) {
	controller := &observingAdminStateController{memoryAdminStateController: &memoryAdminStateController{}}
	runtime, _, _, agents, _ := newTestAdminRuntime(t, controller)
	controller.read = func() {
		if !runtime.mu.TryLock() {
			t.Error("public refresh held runtime lock during controller IO")
			return
		}
		runtime.mu.Unlock()
	}
	if err := runtime.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	controller.failure = errors.New("test storage unavailable")
	llm := &a2aTestProvider{}
	handler := NewHandler(modulesPipeline("admin"), llm).WithAdminState(runtime).WithAgentRegistry(agents)
	for _, path := range []string{"/a2a/research/.well-known/agent-card.json", "/v1/playground/catalog"} {
		response := httptest.NewRecorder()
		Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusServiceUnavailable || llm.request.ResponseRequest != nil {
			t.Fatal("public configuration read did not fail closed")
		}
	}
}

func TestAgentConfigurationStateVersionSupportsSafeLegacyRollback(t *testing.T) {
	runtime, _, _, agents, _ := newTestAdminRuntime(t, &memoryAdminStateController{})
	if _, err := agents.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"lookup"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	legacy := agentFixture()
	legacy.Instructions = ""
	legacy.Generation = nil
	if _, err := agents.PutAgentProfile("research", legacy); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.snapshot()
	if err != nil || snapshot.SchemaVersion != 1 {
		t.Fatal("metadata-only profiles no longer use legacy state format")
	}
	if _, err := agents.PutAgentProfile("research", agentFixture()); err != nil {
		t.Fatal(err)
	}
	snapshot, err = runtime.snapshot()
	if err != nil || snapshot.SchemaVersion != 2 {
		t.Fatal("new configuration did not protect state from older binary writers")
	}
	snapshot.SchemaVersion = 1
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.applyPayload(payload, 1) == nil {
		t.Fatal("new configuration was accepted under the older state version")
	}
	cleared := agentFixture()
	cleared.Instructions = ""
	cleared.Generation = &AgentGeneration{}
	if _, err := agents.PutAgentProfile("research", cleared); err != nil {
		t.Fatal(err)
	}
	snapshot, err = runtime.snapshot()
	if err != nil || snapshot.SchemaVersion != 1 {
		t.Fatal("cleared settings did not restore the legacy-compatible state boundary")
	}
}
