package gateway

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

const agentFixtureInstructions = "Use concise factual replies."

func configuredAgentRegistry(t *testing.T) *AgentRegistry {
	t.Helper()
	registry := NewAgentRegistry()
	if _, err := registry.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"lookup"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return registry
}
func agentFixture() AgentProfile {
	temperature, limit := 0.0, 127
	return AgentProfile{Name: "Research", Model: "test-model", Instructions: agentFixtureInstructions, Generation: &AgentGeneration{Temperature: &temperature, MaxOutputTokens: &limit}, ToolPolicyID: "safe", MaxIterations: 3, Enabled: true}
}

func TestAgentConfigurationBoundsAndCopies(t *testing.T) {
	registry := configuredAgentRegistry(t)
	original := agentFixture()
	saved, err := registry.PutAgentProfile("research", original)
	if err != nil || !saved.ContentStored || !saved.InstructionsConfigured || !saved.ExecutionSupported {
		t.Fatal("valid executable configuration was not accepted")
	}
	*original.Generation.Temperature = 1
	*saved.Generation.MaxOutputTokens = 200
	read, _ := registry.AgentProfile("research")
	*read.Generation.Temperature = 2
	listed := registry.AgentProfiles()
	*listed[0].Generation.Temperature = 1.5
	stored, _ := registry.AgentProfile("research")
	if *stored.Generation.Temperature != 0 || *stored.Generation.MaxOutputTokens != 127 {
		t.Fatal("configuration aliases caller-owned generation settings")
	}
	metadata, err := json.Marshal(stored)
	if err != nil || strings.Contains(string(metadata), agentFixtureInstructions) {
		t.Fatal("metadata exposed stored instructions")
	}
	cases := []struct {
		name   string
		change func(*AgentProfile)
	}{
		{"oversized", func(item *AgentProfile) { item.Instructions = strings.Repeat("x", (64<<10)+1) }},
		{"invalid utf8", func(item *AgentProfile) { item.Instructions = string([]byte{0xff}) }},
		{"NUL", func(item *AgentProfile) { item.Instructions = "invalid\x00instruction" }},
		{"template conflict", func(item *AgentProfile) { item.InstructionsTemplateID = "external" }},
		{"NaN", func(item *AgentProfile) { *item.Generation.Temperature = math.NaN() }},
		{"infinite", func(item *AgentProfile) { *item.Generation.Temperature = math.Inf(1) }},
		{"temperature range", func(item *AgentProfile) { *item.Generation.Temperature = 2.1 }},
		{"zero reserve", func(item *AgentProfile) { *item.Generation.MaxOutputTokens = 0 }},
		{"excess reserve", func(item *AgentProfile) { *item.Generation.MaxOutputTokens = 1000001 }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			item := agentFixture()
			test.change(&item)
			if _, err := registry.PutAgentProfile("invalid", item); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	boundary := agentFixture()
	boundary.Instructions = strings.Repeat("x", 64<<10)
	if _, err := registry.PutAgentProfile("boundary", boundary); err != nil {
		t.Fatal("exact instruction limit rejected")
	}
}

func TestAgentConfigurationAdminAPIEncryptedAndPreserved(t *testing.T) {
	controller := &memoryAdminStateController{}
	runtime, _, _, agents, _ := newTestAdminRuntime(t, controller)
	if _, err := agents.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"lookup"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	audit := &recordingAuditClient{}
	handler := NewHandler(modulesPipeline("admin"), nil).WithAgentRegistry(agents).WithAdminState(runtime).WithAudit(audit)
	router := Routes(handler)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		return response
	}
	basic := `"name":"Research","model":"test-model","tool_policy_id":"safe","max_iterations":3,"enabled":true`
	response := call(http.MethodPut, "/admin/v1/agent-profiles/research", `{`+basic+`,"instructions":"`+agentFixtureInstructions+`","generation":{"temperature":0,"max_output_tokens":127}}`)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), agentFixtureInstructions) {
		t.Fatal("mutation failed or exposed instructions")
	}
	if strings.Contains(string(controller.payload), agentFixtureInstructions) {
		t.Fatal("instructions persisted without encryption")
	}
	if !strings.Contains(string(controller.payload), `"agent_instructions"`) {
		t.Fatal("encrypted instructions missing")
	}
	events, err := json.Marshal(audit.events)
	if err != nil || strings.Contains(string(events), agentFixtureInstructions) {
		t.Fatal("audit exposed instructions")
	}
	list := call(http.MethodGet, "/admin/v1/agent-profiles", "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), agentFixtureInstructions) || !strings.Contains(list.Body.String(), `"content_stored":true`) || list.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("list exposed content or reported incorrect storage boundary")
	}
	detail := call(http.MethodGet, "/admin/v1/agent-profiles/research", "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), agentFixtureInstructions) || detail.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("authorized configuration read failed")
	}
	if missing := call(http.MethodGet, "/admin/v1/agent-profiles/missing", ""); missing.Code != http.StatusNotFound {
		t.Fatal("missing profile did not return 404")
	}
	for _, role := range []string{"reader", "org_admin"} {
		denied := httptest.NewRecorder()
		Routes(NewHandler(modulesPipeline(role), nil).WithAgentRegistry(agents)).ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/admin/v1/agent-profiles/research", nil))
		if denied.Code != http.StatusForbidden || strings.Contains(denied.Body.String(), agentFixtureInstructions) {
			t.Fatal("configuration read bypassed global administrator authorization")
		}
	}
	if preserved := call(http.MethodPut, "/admin/v1/agent-profiles/research", `{`+basic+`}`); preserved.Code != http.StatusOK {
		t.Fatal("legacy profile update failed")
	}
	_, _, _, restored, _ := newTestAdminRuntime(t, controller)
	saved, ok := restored.AgentProfile("research")
	if !ok || saved.Instructions != agentFixtureInstructions || saved.Generation == nil || *saved.Generation.Temperature != 0 || *saved.Generation.MaxOutputTokens != 127 {
		t.Fatal("legacy update or replica restoration lost executable settings")
	}
	if cleared := call(http.MethodPut, "/admin/v1/agent-profiles/research", `{`+basic+`,"instructions":"","generation":{}}`); cleared.Code != http.StatusOK {
		t.Fatal("explicit configuration clear failed")
	}
	saved, _ = agents.AgentProfile("research")
	if saved.Instructions != "" || saved.ContentStored || saved.InstructionsConfigured || saved.Generation != nil {
		t.Fatal("configuration did not clear")
	}
}

func TestAgentInstructionsFailClosedWithoutEncryption(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "no runtime", true: "no key"}[durable], func(t *testing.T) {
			registry := configuredAgentRegistry(t)
			handler := NewHandler(modulesPipeline("admin"), nil).WithAgentRegistry(registry)
			if durable {
				runtime, err := NewAdminStateRuntime(context.Background(), &memoryAdminStateController{}, nil, NewAccessRegistry(), NewMCPRegistry(), registry, newLoggingRegistryWithoutWorker(nil))
				if err != nil {
					t.Fatal(err)
				}
				handler = handler.WithAdminState(runtime)
			}
			response := httptest.NewRecorder()
			Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/admin/v1/agent-profiles/research", strings.NewReader(`{"name":"Research","model":"test-model","tool_policy_id":"safe","max_iterations":3,"enabled":true,"instructions":"`+agentFixtureInstructions+`"}`)))
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), agentFixtureInstructions) || len(registry.AgentProfiles()) != 0 {
				t.Fatal("unencrypted instructions were accepted or exposed")
			}
		})
	}
}

func TestAgentInstructionsRejectTamperedOrMisboundCiphertext(t *testing.T) {
	runtime, _, _, registry, _ := newTestAdminRuntime(t, &memoryAdminStateController{})
	if _, err := registry.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"lookup"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutAgentProfile("research", agentFixture()); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*adminStateSnapshot)
	}{
		{"ciphertext", func(state *adminStateSnapshot) { state.AgentInstructions[0].Ciphertext[0] ^= 1 }},
		{"nonce", func(state *adminStateSnapshot) { state.AgentInstructions[0].Nonce = nil }},
		{"binding", func(state *adminStateSnapshot) { state.AgentInstructions[0].AgentID = "another" }},
		{"duplicate", func(state *adminStateSnapshot) {
			state.AgentInstructions = append(state.AgentInstructions, state.AgentInstructions[0])
		}},
		{"missing", func(state *adminStateSnapshot) { state.AgentInstructions = nil }},
		{"encryption domain", func(state *adminStateSnapshot) {
			item := &state.AgentInstructions[0]
			item.Ciphertext = runtime.mcpAEAD.Seal(nil, item.Nonce, []byte(agentFixtureInstructions), []byte(item.AgentID))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := runtime.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			test.change(&snapshot)
			if runtime.apply(snapshot) == nil {
				t.Fatal("invalid encrypted configuration was accepted")
			}
			saved, _ := registry.AgentProfile("research")
			if saved.Instructions != agentFixtureInstructions {
				t.Fatal("failed restore changed live configuration")
			}
		})
	}
	snapshot, err := runtime.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	wrong := &memoryAdminStateController{payload: payload, revision: 1}
	if _, err := NewAdminStateRuntime(context.Background(), wrong, []byte("different-test-key"), NewAccessRegistry(), NewMCPRegistry(), NewAgentRegistry(), newLoggingRegistryWithoutWorker(nil)); err == nil {
		t.Fatal("wrong encryption key restored instructions")
	}
}

func TestAgentConfigurationConflictRestoresEncryptedInstructions(t *testing.T) {
	controller := &memoryAdminStateController{conflict: true}
	runtime, _, _, registry, _ := newTestAdminRuntime(t, controller)
	if _, err := registry.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"lookup"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutAgentProfile("research", agentFixture()); err != nil {
		t.Fatal(err)
	}
	router := Routes(NewHandler(modulesPipeline("admin"), nil).WithAgentRegistry(registry).WithAdminState(runtime))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/admin/v1/agent-profiles/research", strings.NewReader(`{"name":"Research","model":"test-model","tool_policy_id":"safe","max_iterations":3,"enabled":true,"instructions":"Changed configuration","generation":{"temperature":1,"max_output_tokens":300}}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d", response.Code)
	}
	saved, _ := registry.AgentProfile("research")
	if saved.Instructions != agentFixtureInstructions || *saved.Generation.Temperature != 0 || *saved.Generation.MaxOutputTokens != 127 {
		t.Fatal("failed durable mutation changed live configuration")
	}
}

func TestA2AUsesSavedAgentInstructionsAndGeneration(t *testing.T) {
	registry := configuredAgentRegistry(t)
	if _, err := registry.PutAgentProfile("research", agentFixture()); err != nil {
		t.Fatal(err)
	}
	llm := &a2aTestProvider{}
	billing := &lifecycleBillingModule{}
	router := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"test-model"}}, billing}), llm).WithAgentRegistry(registry))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"rpc","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"message","role":"ROLE_USER","parts":[{"text":"Hello"}]}}}`))
	request.Header.Set("A2A-Version", "1.0")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || llm.request.ResponseRequest == nil {
		t.Fatalf("A2A request failed: status=%d", response.Code)
	}
	actual := llm.request.ResponseRequest
	if actual.Instructions != agentFixtureInstructions || actual.Temperature == nil || *actual.Temperature != 0 || actual.MaxOutputTokens == nil || *actual.MaxOutputTokens != 127 || billing.calls != 1 {
		t.Fatal("saved execution settings or billing were bypassed")
	}
	if strings.Contains(response.Body.String(), agentFixtureInstructions) {
		t.Fatal("A2A response exposed configuration")
	}
}
