package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/mcpclient"
	"ai-gateway-gateway/internal/modules"
)

type mcpRuntimeAuth struct {
	tools        []string
	accessGroups []string
	rpm          int
}

func (m mcpRuntimeAuth) Name() string   { return "auth" }
func (m mcpRuntimeAuth) Required() bool { return true }
func (m mcpRuntimeAuth) Handle(_ context.Context, req *modules.RequestContext) error {
	req.APIKey = ""
	req.CredentialID = "credential"
	req.UserID = "user"
	req.AllowedTools = append([]string(nil), m.tools...)
	req.AccessGroupIDs = append([]string(nil), m.accessGroups...)
	req.RateLimitRPM = m.rpm
	return nil
}

type mcpBillingRecorder struct {
	phases   []string
	apiTypes []string
}

func (*mcpBillingRecorder) Name() string              { return "billing" }
func (*mcpBillingRecorder) Required() bool            { return true }
func (*mcpBillingRecorder) PostResponseEnabled() bool { return true }
func (m *mcpBillingRecorder) Handle(_ context.Context, req *modules.RequestContext) error {
	return m.record("reserve", req)
}
func (m *mcpBillingRecorder) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	return m.record("commit", req)
}
func (m *mcpBillingRecorder) HandleFailure(_ context.Context, req *modules.RequestContext, _ error) error {
	return m.record("cancel", req)
}
func (m *mcpBillingRecorder) record(phase string, req *modules.RequestContext) error {
	m.phases = append(m.phases, phase)
	m.apiTypes = append(m.apiTypes, req.Metadata["gateway.api_type"])
	return nil
}

type fakeMCPRuntimeClient struct {
	page   mcpclient.ToolPage
	err    error
	cursor string
	calls  int
}

func (c *fakeMCPRuntimeClient) ListTools(_ context.Context, cursor string) (mcpclient.ToolPage, error) {
	c.calls++
	c.cursor = cursor
	return c.page, c.err
}

func runtimeRegistry(t *testing.T, transport string) *MCPRegistry {
	t.Helper()
	registry := NewMCPRegistry()
	_, err := registry.PutServer("weather", MCPServer{Label: "Weather", ServerURL: "https://mcp.example.test/v1", Transport: transport, Tools: []string{"mcp:weather@https://mcp.example.test/v1"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestMCPRuntimeListsToolsWithACLRateLimitAndBilling(t *testing.T) {
	billing := &mcpBillingRecorder{}
	client := &fakeMCPRuntimeClient{page: mcpclient.ToolPage{Tools: []mcpclient.Tool{{Name: "forecast", InputSchema: json.RawMessage(`{"type":"object"}`)}}, NextCursor: "next"}}
	pipeline := modules.NewPipeline([]modules.Module{mcpRuntimeAuth{tools: []string{"mcp:weather@https://mcp.example.test/v1"}, rpm: 2}, billing})
	handler := NewHandler(pipeline, nil).WithMCPRegistry(runtimeRegistry(t, "streamable-http")).WithMCPRuntimeFactory(func(endpoint string) (MCPRuntimeClient, error) {
		if endpoint != "https://mcp.example.test/v1" {
			t.Fatalf("endpoint=%q", endpoint)
		}
		return client, nil
	})
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/mcp/servers/weather/tools?cursor=current", nil))
	if response.Code != http.StatusOK || client.calls != 1 || client.cursor != "current" || !strings.Contains(response.Body.String(), `"name":"forecast"`) || !strings.Contains(response.Body.String(), `"nextCursor":"next"`) {
		t.Fatalf("status=%d body=%s client=%+v", response.Code, response.Body.String(), client)
	}
	if strings.Join(billing.phases, ",") != "reserve,commit" || strings.Join(billing.apiTypes, ",") != "mcp_tools_list,mcp_tools_list" {
		t.Fatalf("billing phases=%v apiTypes=%v", billing.phases, billing.apiTypes)
	}
	if response.Header().Get("X-Execution-ID") == "" {
		t.Fatal("execution ID was not returned")
	}
}

func TestMCPRuntimeFailsClosedForPolicyTransportAndUpstream(t *testing.T) {
	for _, test := range []struct {
		name      string
		auth      mcpRuntimeAuth
		transport string
		clientErr error
		status    int
		code      string
		phases    string
	}{
		{name: "credential ACL", auth: mcpRuntimeAuth{tools: []string{"mcp:other@https://mcp.example.test/v1"}}, transport: "streamable-http", status: http.StatusForbidden, code: "tool_not_allowed"},
		{name: "legacy transport", auth: mcpRuntimeAuth{}, transport: "sse", status: http.StatusBadRequest, code: "unsupported_mcp_transport"},
		{name: "upstream", auth: mcpRuntimeAuth{}, transport: "streamable-http", clientErr: errors.New("unavailable"), status: http.StatusBadGateway, code: "mcp_server_failed", phases: "reserve,cancel"},
	} {
		t.Run(test.name, func(t *testing.T) {
			billing := &mcpBillingRecorder{}
			client := &fakeMCPRuntimeClient{err: test.clientErr}
			handler := NewHandler(modules.NewPipeline([]modules.Module{test.auth, billing}), nil).WithMCPRegistry(runtimeRegistry(t, test.transport)).WithMCPRuntimeFactory(func(string) (MCPRuntimeClient, error) { return client, nil })
			response := httptest.NewRecorder()
			Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/mcp/servers/weather/tools", nil))
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) || strings.Join(billing.phases, ",") != test.phases {
				t.Fatalf("status=%d body=%s phases=%v", response.Code, response.Body.String(), billing.phases)
			}
		})
	}
}

func TestMCPRuntimeEnforcesAccessGroupToolIntersection(t *testing.T) {
	access := NewAccessRegistry()
	if _, err := access.PutGroup("restricted", AccessGroup{Name: "Restricted", AllowedTools: []string{"mcp:other@https://mcp.example.test/v1"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	client := &fakeMCPRuntimeClient{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{mcpRuntimeAuth{accessGroups: []string{"restricted"}}}), nil).WithAccessRegistry(access).WithMCPRegistry(runtimeRegistry(t, "streamable-http")).WithMCPRuntimeFactory(func(string) (MCPRuntimeClient, error) { return client, nil })
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/mcp/servers/weather/tools", nil))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "access_group_tool_not_allowed") || client.calls != 0 {
		t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), client.calls)
	}
}

func TestMCPRuntimeConsumesCredentialRPMBeforeDiscovery(t *testing.T) {
	client := &fakeMCPRuntimeClient{page: mcpclient.ToolPage{Tools: []mcpclient.Tool{}}}
	handler := NewHandler(modules.NewPipeline([]modules.Module{mcpRuntimeAuth{rpm: 1}}), nil).WithMCPRegistry(runtimeRegistry(t, "streamable-http")).WithMCPRuntimeFactory(func(string) (MCPRuntimeClient, error) { return client, nil })
	router := Routes(handler)
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/v1/mcp/servers/weather/tools", nil))
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/v1/mcp/servers/weather/tools", nil))
	if first.Code != http.StatusOK || second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), "rate_limit_exceeded") || client.calls != 1 {
		t.Fatalf("first=%d second=%d body=%s calls=%d", first.Code, second.Code, second.Body.String(), client.calls)
	}
}
