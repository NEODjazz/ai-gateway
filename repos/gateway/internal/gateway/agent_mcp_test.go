package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/mcpclient"
	"ai-gateway-gateway/internal/mcpstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type agentMCPTestProvider struct {
	a2aTestProvider
	requests         []modules.RequestContext
	callCount        int
	alwaysTool       bool
	unknownTool      bool
	invalidArguments bool
	duplicateCalls   bool
	afterResponse    func()
}

func (p *agentMCPTestProvider) Responses(_ context.Context, request modules.RequestContext) (openai.ResponseResponse, error) {
	p.requests = append(p.requests, request)
	result := openai.ResponseResponse{ID: fmt.Sprintf("resp_%d", len(p.requests)), Status: "completed", Model: "test-model", OutputText: "The forecast is sunny"}
	if len(p.requests) == 1 || p.alwaysTool {
		result.OutputText = ""
		for i := 0; i < p.callCount; i++ {
			name := request.ResponseRequest.Tools[0].Name
			if p.unknownTool {
				name = "unbound"
			}
			arguments := `{"city":"Paris","count":2}`
			if p.invalidArguments {
				arguments = `[]`
			}
			id := fmt.Sprintf("call_%d_%d", len(p.requests), i)
			if p.duplicateCalls {
				id = "duplicate"
			}
			result.Output = append(result.Output, openai.ResponseOutputItem{Type: "function_call", Name: name, CallID: id, Arguments: arguments})
		}
	}
	if p.afterResponse != nil {
		p.afterResponse()
	}
	return result, nil
}

func agentMCPTestHandler(t *testing.T, llm *agentMCPTestProvider, policy ToolPolicy, tools []string, calls int, iterations int) (Handler, *fakeMCPRuntimeClient, *mcpBillingRecorder) {
	t.Helper()
	agents := NewAgentRegistry()
	if policy.ID == "" {
		policy = ToolPolicy{Name: "Safe", AllowedTools: []string{"forecast"}, Enabled: true, MaxToolCalls: calls}
	}
	if _, err := agents.PutToolPolicy("safe", policy); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.PutAgentProfile("research", AgentProfile{Name: "Research", Model: "test-model", ToolPolicyID: "safe", Enabled: true, MaxIterations: iterations, MCPTools: []AgentMCPTool{{ServerID: "weather", ToolName: "forecast"}}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeMCPRuntimeClient{page: mcpclient.ToolPage{Tools: []mcpclient.Tool{{Name: "forecast", InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},"count":{"type":"integer","minimum":1}},"required":["city"]}`)}}}, callResult: mcpclient.CallResult{Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"sunny"}`)}}}
	billing := &mcpBillingRecorder{}
	h := NewHandler(modules.NewPipeline([]modules.Module{mcpRuntimeAuth{tools: tools}, billing}), llm).WithAgentRegistry(agents).WithMCPRegistry(runtimeRegistry(t, "streamable-http")).WithMCPCallStore(mcpstate.NewMemoryStore(100, time.Hour)).WithAudit(&recordingAuditClient{}).WithMCPRuntimeFactory(func(string, string, string) (MCPRuntimeClient, error) { return client, nil })
	return h, client, billing
}
func agentMCPSend(h Handler, method, extra string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"rpc","method":"`+method+`","params":{"tenant":"research","message":{"messageId":"user-message","role":"ROLE_USER","parts":[{"text":"Check Paris weather"}]} `+extra+`}}`))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer test-key")
	response := httptest.NewRecorder()
	Routes(h).ServeHTTP(response, request)
	return response
}

func TestAgentMCPLoopUsesCredentialACLAndBillingForEveryStep(t *testing.T) {
	llm := &agentMCPTestProvider{callCount: 1}
	h, client, billing := agentMCPTestHandler(t, llm, ToolPolicy{}, []string{"mcp:weather@https://mcp.example.test/v1#tool:forecast"}, 2, 3)
	response := agentMCPSend(h, "SendMessage", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "The forecast is sunny") || len(llm.requests) != 2 || client.callCalls != 1 {
		t.Fatalf("status=%d response=%s models=%d tools=%d", response.Code, response.Body.String(), len(llm.requests), client.callCalls)
	}
	if client.callName != "forecast" || client.callArgs["city"] != "Paris" || client.callArgs["count"] != json.Number("2") {
		t.Fatalf("tool arguments=%v", client.callArgs)
	}
	first := llm.requests[0].ResponseRequest
	if !strings.Contains(first.Tools[0].Description, "forecast") || !strings.Contains(first.Tools[0].Description, "weather") {
		t.Fatal("description-less MCP tool lost its semantic name behind the private alias")
	}
	if first.Tools[0].Name != agentMCPFunctionName(AgentMCPTool{ServerID: "weather", ToolName: "forecast"}) {
		t.Fatal("unstable tool alias")
	}
	history, _ := json.Marshal(llm.requests[1].ResponseRequest.Input)
	if !strings.Contains(string(history), `"type":"function_call_output"`) || !strings.Contains(string(history), "sunny") {
		t.Fatalf("missing typed tool result: %s", history)
	}
	if llm.requests[0].RequestID == llm.requests[1].RequestID || llm.requests[0].RequestID == "" {
		t.Fatal("model iterations share an execution ID")
	}
	if strings.Join(billing.apiTypes, ",") != "mcp_tools_list,mcp_tools_list,a2a,mcp_tools_call,mcp_tools_call,a2a" {
		t.Fatalf("billing api types=%v", billing.apiTypes)
	}
	if strings.Contains(response.Body.String(), "mcp.example") || strings.Contains(response.Body.String(), "test-key") {
		t.Fatal("private connector or credential leaked")
	}
}

func TestAgentMCPLoopStopsBeforeUnapprovedOrInvalidTools(t *testing.T) {
	tests := []struct {
		name              string
		llm               agentMCPTestProvider
		policy            ToolPolicy
		grants            []string
		calls, iterations int
		status            int
		modelCalls        int
	}{
		{name: "iteration limit", llm: agentMCPTestProvider{callCount: 1}, calls: 3, iterations: 1, status: http.StatusConflict, modelCalls: 1},
		{name: "whole batch call limit", llm: agentMCPTestProvider{callCount: 2}, calls: 1, iterations: 3, status: http.StatusConflict, modelCalls: 1},
		{name: "unknown function", llm: agentMCPTestProvider{callCount: 1, unknownTool: true}, calls: 3, iterations: 3, status: http.StatusBadGateway, modelCalls: 1},
		{name: "invalid arguments", llm: agentMCPTestProvider{callCount: 1, invalidArguments: true}, calls: 3, iterations: 3, status: http.StatusBadGateway, modelCalls: 1},
		{name: "duplicate call", llm: agentMCPTestProvider{callCount: 2, duplicateCalls: true}, calls: 3, iterations: 3, status: http.StatusBadGateway, modelCalls: 1},
		{name: "approval required", llm: agentMCPTestProvider{callCount: 1}, policy: ToolPolicy{ID: "safe", Name: "Safe", AllowedTools: []string{"forecast"}, ApprovalRequired: []string{"forecast"}, MaxToolCalls: 3, Enabled: true}, calls: 3, iterations: 3, status: http.StatusConflict, modelCalls: 1},
		{name: "credential denied", llm: agentMCPTestProvider{callCount: 1}, grants: []string{"unrelated"}, calls: 3, iterations: 3, status: http.StatusForbidden},
		{name: "profile denied", llm: agentMCPTestProvider{callCount: 1}, policy: ToolPolicy{ID: "safe", Name: "Safe", AllowedTools: []string{"forecast"}, DeniedTools: []string{"forecast"}, MaxToolCalls: 3, Enabled: true}, calls: 3, iterations: 3, status: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, client, _ := agentMCPTestHandler(t, &tt.llm, tt.policy, tt.grants, tt.calls, tt.iterations)
			response := agentMCPSend(h, "SendMessage", "")
			if response.Code != tt.status || client.callCalls != 0 || len(tt.llm.requests) != tt.modelCalls {
				t.Fatalf("status=%d body=%s models=%d tools=%d", response.Code, response.Body.String(), len(tt.llm.requests), client.callCalls)
			}
		})
	}
}

func TestAgentMCPLoopEnforcesCumulativeLimitAndPolicyRevocation(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			llm := &agentMCPTestProvider{callCount: 1, alwaysTool: true}
			h, client, _ := agentMCPTestHandler(t, llm, ToolPolicy{}, nil, 1, 4)
			if revoke {
				llm.afterResponse = func() {
					_, err := h.agents.PutToolPolicy("safe", ToolPolicy{Name: "Revoked", AllowedTools: []string{"forecast"}, MaxToolCalls: 1, Enabled: false})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			response := agentMCPSend(h, "SendMessage", "")
			expected := 1
			if revoke {
				expected = 0
			}
			if response.Code == http.StatusOK || client.callCalls != expected {
				t.Fatalf("status=%d tools=%d body=%s", response.Code, client.callCalls, response.Body.String())
			}
		})
	}
}

func TestAgentMCPLoopRejectsUnsupportedExecutionModes(t *testing.T) {
	for _, method := range []string{"SendStreamingMessage", "SendMessage"} {
		llm := &agentMCPTestProvider{callCount: 1}
		h, client, _ := agentMCPTestHandler(t, llm, ToolPolicy{}, nil, 2, 3)
		extra := ""
		if method == "SendMessage" {
			extra = `,"configuration":{"returnImmediately":true}`
		}
		response := agentMCPSend(h, method, extra)
		if response.Code != http.StatusNotImplemented || len(llm.requests) != 0 || client.calls != 0 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		profile, _ := h.agents.AgentProfile("research")
		card := h.a2aAgentCard(httptest.NewRequest("GET", "/", nil), profile)
		if card["capabilities"].(map[string]any)["streaming"] != false {
			t.Fatal("unsupported streaming advertised")
		}
	}
}

func TestAgentMCPBindingsValidateCopyAndLegacyUpdates(t *testing.T) {
	registry := NewAgentRegistry()
	_, _ = registry.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"forecast"}, MaxToolCalls: 1, Enabled: true})
	base := AgentProfile{Name: "Research", Model: "test-model", ToolPolicyID: "safe", MaxIterations: 3, Enabled: true, MCPTools: []AgentMCPTool{{ServerID: "weather", ToolName: "forecast"}}}
	for _, invalid := range [][]AgentMCPTool{{{ServerID: "bad/id", ToolName: "forecast"}}, {{ServerID: "weather", ToolName: " "}}, {{ServerID: "weather", ToolName: "x\x00"}}, {{ServerID: "weather", ToolName: "forecast"}, {ServerID: "weather", ToolName: "forecast"}}, make([]AgentMCPTool, 33)} {
		value := base
		value.MCPTools = invalid
		if _, err := registry.PutAgentProfile("research", value); err == nil {
			t.Fatal("invalid binding accepted")
		}
	}
	stored, err := registry.PutAgentProfile("research", base)
	if err != nil {
		t.Fatal(err)
	}
	base.MCPTools[0].ToolName = "mutated"
	stored.MCPTools[0].ToolName = "mutated"
	read, _ := registry.AgentProfile("research")
	if read.MCPTools[0].ToolName != "forecast" {
		t.Fatal("binding slices alias registry")
	}
	h := NewHandler(modulesPipeline("admin"), nil).WithAgentRegistry(registry).WithAudit(&recordingAuditClient{})
	body := `{"name":"Research","model":"test-model","tool_policy_id":"safe","max_iterations":3,"enabled":true`
	for _, clear := range []bool{false, true} {
		suffix := "}"
		if clear {
			suffix = `,"mcp_tools":[]}`
		}
		response := httptest.NewRecorder()
		Routes(h).ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/admin/v1/agent-profiles/research", strings.NewReader(body+suffix)))
		read, _ = registry.AgentProfile("research")
		want := 1
		if clear {
			want = 0
		}
		if response.Code != 200 || len(read.MCPTools) != want {
			t.Fatalf("status=%d bindings=%v", response.Code, read.MCPTools)
		}
	}
}

func TestAgentMCPBindingsStateVersionAndReplicaRestore(t *testing.T) {
	controller := &memoryAdminStateController{}
	runtime, _, _, agents, _ := newTestAdminRuntime(t, controller)
	_, err := agents.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"forecast"}, MaxToolCalls: 2, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = agents.PutAgentProfile("research", AgentProfile{Name: "Research", Model: "test-model", ToolPolicyID: "safe", MaxIterations: 3, Enabled: true, MCPTools: []AgentMCPTool{{ServerID: "weather", ToolName: "forecast"}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.snapshot()
	if err != nil || snapshot.SchemaVersion != 3 {
		t.Fatalf("snapshot version=%d err=%v", snapshot.SchemaVersion, err)
	}
	for _, oldVersion := range []int{1, 2} {
		snapshot.SchemaVersion = oldVersion
		payload, _ := json.Marshal(snapshot)
		if runtime.applyPayload(payload, 100) == nil {
			t.Fatal("MCP bindings accepted with an old writer-compatible version")
		}
	}
	snapshot.SchemaVersion = 3
	payload, _ := json.Marshal(snapshot)
	controller.payload = payload
	controller.revision = 1
	_, _, _, restored, _ := newTestAdminRuntime(t, controller)
	profile, ok := restored.AgentProfile("research")
	if !ok || len(profile.MCPTools) != 1 || profile.MCPTools[0].ToolName != "forecast" {
		t.Fatal("fresh replica lost MCP bindings")
	}
	profile.MCPTools = nil
	_, err = agents.PutAgentProfile("research", profile)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = runtime.snapshot()
	if err != nil || snapshot.SchemaVersion != 1 {
		t.Fatal("cleared bindings did not restore compatible state")
	}
}

func TestAgentMCPContextCannotAuthorizePublicUnboundFunctions(t *testing.T) {
	llm := &agentMCPTestProvider{callCount: 1}
	h, _, _ := agentMCPTestHandler(t, llm, ToolPolicy{}, []string{"mcp:weather@https://mcp.example.test/v1#tool:forecast"}, 2, 3)
	name := agentMCPFunctionName(AgentMCPTool{ServerID: "weather", ToolName: "forecast"})
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello","tools":[{"type":"function","name":"`+name+`","parameters":{"type":"object"}}]}`))
	response := httptest.NewRecorder()
	Routes(h).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || len(llm.requests) != 0 {
		t.Fatal("public function used private agent MCP authorization")
	}
}

func TestAgentMCPLoopStopsOnToolFailureAndCancellation(t *testing.T) {
	llm := &agentMCPTestProvider{callCount: 1}
	h, client, _ := agentMCPTestHandler(t, llm, ToolPolicy{}, nil, 2, 3)
	client.callErr = fmt.Errorf("private MCP transport detail")
	response := agentMCPSend(h, "SendMessage", "")
	if response.Code != http.StatusBadGateway || len(llm.requests) != 1 || client.callCalls != 1 || strings.Contains(response.Body.String(), "private MCP transport detail") {
		t.Fatalf("status=%d models=%d tools=%d body=%s", response.Code, len(llm.requests), client.callCalls, response.Body.String())
	}
	profile, _ := h.agents.AgentProfile("research")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", nil).WithContext(ctx)
	capture := httptest.NewRecorder()
	h.serveAgentResponsesAs(capture, request, openai.ResponseRequest{Model: "test-model", Input: []any{}}, profile, func(openai.ResponseResponse, modules.RequestContext) any {
		t.Fatal("cancelled execution completed")
		return nil
	})
	if capture.Code != http.StatusRequestTimeout || len(llm.requests) != 1 || client.calls != 1 {
		t.Fatal("cancelled run executed discovery or inference")
	}
}

func TestAgentMCPCaptureBoundsResponseMemory(t *testing.T) {
	capture := newA2AResponseCapture()
	bounded := agentMCPCapture{capture}
	if _, err := bounded.Write(make([]byte, agentMCPMaxBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := bounded.Write([]byte("overflow")); err == nil || capture.body.Len() != agentMCPMaxBytes {
		t.Fatal("agent capture exceeded its bound")
	}
}

func TestAgentMCPIndependentMessageDoesNotClaimTaskContinuity(t *testing.T) {
	llm := &agentMCPTestProvider{callCount: 1}
	h, _, _ := agentMCPTestHandler(t, llm, ToolPolicy{}, nil, 2, 3)
	tasks := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	h = h.WithA2ATaskStore(tasks, A2ATaskRuntimeConfig{OwnerQuota: 10, TTL: time.Hour})
	response := agentMCPSend(h, "SendMessage", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"message"`) || strings.Contains(response.Body.String(), `"task"`) || len(tasks.tasks) != 0 {
		t.Fatal("MCP execution advertised unclaimed task continuity")
	}
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"rpc","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"followup","role":"ROLE_USER","taskId":"task","parts":[{"text":"next"}]}}}`))
	request.Header.Set("A2A-Version", "1.0")
	capture := httptest.NewRecorder()
	Routes(h).ServeHTTP(capture, request)
	if capture.Code != http.StatusNotImplemented || len(llm.requests) != 2 {
		t.Fatal("unsupported task continuation reached tool execution")
	}
}

func TestAgentMCPRejectsConnectorChangeAfterSchemaDiscovery(t *testing.T) {
	llm := &agentMCPTestProvider{callCount: 1}
	h, client, _ := agentMCPTestHandler(t, llm, ToolPolicy{}, nil, 2, 3)
	llm.afterResponse = func() {
		server, _ := h.mcp.Server("weather")
		server.ServerURL = "https://other.example.test/mcp"
		if _, err := h.mcp.PutServer("weather", server); err != nil {
			t.Fatal(err)
		}
	}
	response := agentMCPSend(h, "SendMessage", "")
	if response.Code != http.StatusForbidden || client.callCalls != 0 || len(llm.requests) != 1 {
		t.Fatal("schema from one connector authorized execution on another")
	}
}

func TestAgentMCPPolicyToolsetIsRevalidated(t *testing.T) {
	llm := &agentMCPTestProvider{callCount: 1}
	policy := ToolPolicy{ID: "safe", Name: "Safe", AllowedTools: []string{"toolset:read"}, MaxToolCalls: 2, Enabled: true}
	h, client, _ := agentMCPTestHandler(t, llm, policy, nil, 2, 3)
	if _, err := h.mcp.PutToolset("read", MCPToolset{Name: "Read", Tools: []string{"mcp:weather@https://mcp.example.test/v1#tool:forecast"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	response := agentMCPSend(h, "SendMessage", "")
	if response.Code != http.StatusOK || client.callCalls != 1 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := h.mcp.PutToolset("read", MCPToolset{Name: "Read", Tools: []string{"mcp:weather@https://mcp.example.test/v1#tool:other"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	response = agentMCPSend(h, "SendMessage", "")
	if response.Code != http.StatusForbidden || client.callCalls != 1 {
		t.Fatal("revoked policy toolset still executed MCP")
	}
}
