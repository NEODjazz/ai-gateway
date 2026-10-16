package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

type playgroundAuth struct {
	identity modules.RequestContext
	err      error
}

func (playgroundAuth) Name() string   { return "auth" }
func (playgroundAuth) Required() bool { return true }
func (a playgroundAuth) Handle(_ context.Context, req *modules.RequestContext) error {
	request, id := req.Request, req.RequestID
	*req = a.identity
	req.Request, req.RequestID = request, id
	if req.CredentialID == "" {
		req.CredentialID = "test-key"
	}
	return a.err
}

func TestPlaygroundCatalogFiltersMCPAndDoesNotExecuteOrExposeSecrets(t *testing.T) {
	registry := runtimeRegistry(t, "streamable-http")
	server, _ := registry.Server("weather")
	if _, err := registry.PutServer("weather", server, "private-test-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutServer("foreign", MCPServer{Label: "Foreign", ServerURL: "https://foreign.example.test", Transport: "streamable-http", Tools: []string{"*"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutToolset("read", MCPToolset{Name: "Read only", Tools: []string{"mcp:weather@https://mcp.example.test/v1#tool:forecast"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	access := NewAccessRegistry()
	if _, err := access.PutGroup("readers", AccessGroup{Name: "Readers", Enabled: true, AllowedModels: []string{"safe"}, AllowedTools: []string{"toolset:read"}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeMCPRuntimeClient{}
	identity := modules.RequestContext{CredentialID: "key", AllowedModels: []string{"safe"}, AllowedTools: []string{"toolset:read"}, AccessGroupIDs: []string{"readers"}, Tags: []string{"work"}}
	h := NewHandler(modules.NewPipeline([]modules.Module{playgroundAuth{identity: identity}}), provider.New(provider.Config{})).WithMCPRegistry(registry).WithAccessRegistry(access).
		WithMCPRuntimeFactory(func(string, string, string) (MCPRuntimeClient, error) { return client, nil })
	w := httptest.NewRecorder()
	Routes(h).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/playground/catalog?model=safe", nil))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected status %d", w.Code)
	}
	var result playgroundCatalog
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.MCPServers) != 1 || result.MCPServers[0].ID != "weather" || len(result.MCPToolsets) != 1 || strings.Join(result.MCPToolsets[0].ServerIDs, ",") != "weather" {
		t.Fatalf("unexpected selectable resources: %+v", result)
	}
	for _, forbidden := range []string{"private-test-secret", "server_url", "foreign", "credential_configured"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("catalog exposed %s", forbidden)
		}
	}
	if client.calls != 0 || client.callCalls != 0 {
		t.Fatal("catalog invoked MCP")
	}
}

func TestPlaygroundPoliciesUseOrganizationAndUserIdentity(t *testing.T) {
	runtime := provider.New(provider.Config{})
	controller := runtime.(provider.GuardrailController)
	access := NewAccessRegistry()
	for _, item := range []struct{ name, org, user string }{{"organization-policy", "north", ""}, {"user-policy", "", "alice"}, {"foreign-organization", "south", ""}, {"foreign-user", "", "bob"}} {
		if _, err := controller.UpdateGuardrailPolicy(item.name, provider.GuardrailPolicy{Enabled: true, DLP: true, Anonymization: "disabled"}); err != nil {
			t.Fatal(err)
		}
		attachment := PolicyAttachment{PolicyName: item.name, Scope: "specific"}
		if item.org != "" {
			attachment.Organizations = []string{item.org}
		}
		if item.user != "" {
			attachment.Users = []string{item.user}
		}
		if _, err := access.PutPolicyAttachment(item.name, attachment); err != nil {
			t.Fatal(err)
		}
	}
	identity := modules.RequestContext{CredentialID: "key", OrganizationID: "north", UserID: "alice", AllowedModels: []string{"safe"}}
	h := NewHandler(modules.NewPipeline([]modules.Module{playgroundAuth{identity: identity}}), runtime).WithAccessRegistry(access).WithAudit(&recordingAuditClient{}).WithComplianceModules(&complianceModule{name: "dlp"}, nil)
	w := httptest.NewRecorder()
	Routes(h).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/playground/catalog?model=safe", nil))
	var catalog playgroundCatalog
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || strings.Join(catalog.Policies, ",") != "organization-policy,user-policy" {
		t.Fatalf("unexpected policy catalog status=%d", w.Code)
	}
	for _, name := range []string{"organization-policy", "user-policy", "foreign-organization", "foreign-user"} {
		response := httptest.NewRecorder()
		Routes(h).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/guardrails/apply_guardrail", strings.NewReader(`{"guardrail_name":"`+name+`","model":"safe","text":"Synthetic test"}`)))
		want := http.StatusOK
		if strings.HasPrefix(name, "foreign-") {
			want = http.StatusForbidden
		}
		if response.Code != want {
			t.Fatalf("policy %s returned %d, want %d", name, response.Code, want)
		}
	}
}

func TestPlaygroundCatalogFailsClosedAndValidatesQueries(t *testing.T) {
	for _, test := range []struct {
		name, query string
		auth        playgroundAuth
		status      int
	}{
		{name: "authentication", auth: playgroundAuth{err: modules.ErrUnauthorized}, status: http.StatusUnauthorized},
		{name: "model", query: "?model=foreign", auth: playgroundAuth{identity: modules.RequestContext{AllowedModels: []string{"safe"}}}, status: http.StatusForbidden},
		{name: "missing group", auth: playgroundAuth{identity: modules.RequestContext{AccessGroupIDs: []string{"missing"}}}, status: http.StatusForbidden},
		{name: "repeated model", query: "?model=safe&model=foreign", status: http.StatusBadRequest},
		{name: "unknown query", query: "?organization_id=foreign", status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := NewHandler(modules.NewPipeline([]modules.Module{test.auth}), provider.New(provider.Config{})).WithAccessRegistry(NewAccessRegistry())
			w := httptest.NewRecorder()
			Routes(h).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/playground/catalog"+test.query, nil))
			if w.Code != test.status {
				t.Fatalf("status=%d, want %d", w.Code, test.status)
			}
		})
	}
}

func TestPlaygroundCatalogBoundsIdentityTagsAndReportsTruncation(t *testing.T) {
	identity := modules.RequestContext{}
	for i := range 257 {
		identity.Tags = append(identity.Tags, fmt.Sprintf("tag-%03d", i))
	}
	h := NewHandler(modules.NewPipeline([]modules.Module{playgroundAuth{identity: identity}}), provider.New(provider.Config{}))
	w := httptest.NewRecorder()
	Routes(h).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/playground/catalog", nil))
	var result playgroundCatalog
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Tags) != 256 || !result.Truncated {
		t.Fatal("catalog did not report bounded output")
	}
}

func TestPlaygroundCatalogReportsUnavailablePoliciesAndRestrictedTools(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "key", ToolAccessRestricted: true}
	h := NewHandler(modules.NewPipeline([]modules.Module{playgroundAuth{identity: identity}}), nil).WithMCPRegistry(runtimeRegistry(t, "streamable-http"))
	w := httptest.NewRecorder()
	Routes(h).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/playground/catalog", nil))
	var result playgroundCatalog
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.MCPServers) != 0 || result.PolicyError == "" {
		t.Fatal("catalog disguised unavailable policies or exposed restricted tools")
	}
}
