package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentProfileMaterializesToolPolicy(t *testing.T) {
	registry := NewAgentRegistry()
	_, err := registry.PutToolPolicy("safe-tools", ToolPolicy{Name: "Safe tools", AllowedTools: []string{"weather", "lookup_*"}, DeniedTools: []string{"lookup_secret"}, ApprovalRequired: []string{"weather"}, MaxToolCalls: 5, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := registry.PutAgentProfile("research", AgentProfile{Name: "Research", Model: "gpt-5", ToolPolicyID: "safe-tools", MaxIterations: 8, Tags: []string{"internal"}, Enabled: true})
	if err != nil || len(profile.AllowedTools) != 2 || profile.MaxToolCalls != 5 || !profile.ExecutionSupported || profile.ContentStored {
		t.Fatalf("unexpected profile: %+v err=%v", profile, err)
	}
	_, _ = registry.PutToolPolicy("safe-tools", ToolPolicy{Name: "Changed", AllowedTools: []string{"different"}, MaxToolCalls: 1, Enabled: true})
	stored := registry.AgentProfiles()[0]
	if stored.AllowedTools[0] != "weather" || stored.MaxToolCalls != 5 {
		t.Fatalf("profile policy changed dynamically: %+v", stored)
	}
	if err := registry.DeleteToolPolicy("safe-tools"); err != nil {
		t.Fatal(err)
	}
	if len(registry.AgentProfiles()) != 1 {
		t.Fatal("materialized profile disappeared with source policy")
	}
}

func TestToolPolicyValidatesApprovalAndProfileBounds(t *testing.T) {
	registry := NewAgentRegistry()
	if _, err := registry.PutToolPolicy("invalid", ToolPolicy{Name: "Invalid", AllowedTools: []string{"weather"}, ApprovalRequired: []string{"finance"}, MaxToolCalls: 2, Enabled: true}); err == nil {
		t.Fatal("approval outside allowlist was accepted")
	}
	_, _ = registry.PutToolPolicy("disabled", ToolPolicy{Name: "Disabled", AllowedTools: []string{"weather"}, MaxToolCalls: 2, Enabled: false})
	if _, err := registry.PutAgentProfile("agent", AgentProfile{Name: "Agent", Model: "gpt", ToolPolicyID: "disabled", MaxIterations: 3}); err == nil {
		t.Fatal("disabled policy was materialized")
	}
	_, _ = registry.PutToolPolicy("enabled", ToolPolicy{Name: "Enabled", AllowedTools: []string{"weather"}, MaxToolCalls: 2, Enabled: true})
	profile, err := registry.PutAgentProfile("templated", AgentProfile{Name: "Templated", Model: "gpt", ToolPolicyID: "enabled", InstructionsTemplateID: "missing-runtime", MaxIterations: 3, Enabled: true})
	if err != nil || profile.ExecutionSupported {
		t.Fatalf("templated profile advertised unsupported execution: %+v err=%v", profile, err)
	}
}

func TestAgentRegistryAdminAPIIsExecutableWithoutStoredContentAndAudited(t *testing.T) {
	audit := &recordingAuditClient{}
	handler := NewHandler(modulesPipeline("admin"), nil).WithAgentRegistry(NewAgentRegistry()).WithAudit(audit)
	router := Routes(handler)
	policy := httptest.NewRecorder()
	router.ServeHTTP(policy, httptest.NewRequest(http.MethodPut, "/admin/v1/tool-policies/safe", strings.NewReader(`{"name":"Safe","allowed_tools":["weather"],"max_tool_calls":4,"enabled":true}`)))
	if policy.Code != http.StatusOK {
		t.Fatalf("policy status=%d body=%s", policy.Code, policy.Body.String())
	}
	profile := httptest.NewRecorder()
	router.ServeHTTP(profile, httptest.NewRequest(http.MethodPut, "/admin/v1/agent-profiles/research", strings.NewReader(`{"name":"Research","model":"gpt-5","instructions_template_id":"prompt-template-1","tool_policy_id":"safe","max_iterations":6,"tags":["internal"],"enabled":true}`)))
	body := profile.Body.String()
	if profile.Code != http.StatusOK || !strings.Contains(body, `"execution_supported":false`) || !strings.Contains(body, `"content_stored":false`) || strings.Contains(strings.ToLower(body), "system_prompt") || len(audit.events) != 4 || audit.events[3].Action != "agent_profile.update" {
		t.Fatalf("unsafe profile response: status=%d body=%s audit=%+v", profile.Code, body, audit.events)
	}
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/agent-profiles", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"instructions_template_id":"prompt-template-1"`) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	content := httptest.NewRecorder()
	router.ServeHTTP(content, httptest.NewRequest(http.MethodPut, "/admin/v1/agent-profiles/content", strings.NewReader(`{"name":"Unsafe","model":"gpt-5","tool_policy_id":"safe","max_iterations":2,"enabled":true,"system_prompt":"do not store"}`)))
	if content.Code != http.StatusBadRequest || strings.Contains(content.Body.String(), "do not store") {
		t.Fatalf("prompt content field was accepted or reflected: status=%d body=%s", content.Code, content.Body.String())
	}
}
