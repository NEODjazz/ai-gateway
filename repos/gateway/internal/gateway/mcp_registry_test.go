package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMCPRegistryValidatesSafeMetadata(t *testing.T) {
	registry := NewMCPRegistry()
	server, err := registry.PutServer("weather", MCPServer{
		Label: "Weather production", ServerURL: "https://MCP.EXAMPLE.test/v1/", Transport: "streamable-http",
		Tools: []string{"mcp:weather@https://mcp.example.test/v1", "mcp:weather@https://mcp.example.test/v1"}, Enabled: true,
	})
	if err != nil || server.ServerURL != "https://mcp.example.test/v1" || len(server.Tools) != 1 {
		t.Fatalf("unexpected server: %#v err=%v", server, err)
	}
	for _, unsafe := range []string{
		"http://mcp.example.test", "https://user:secret@mcp.example.test", "https://mcp.example.test?token=secret", "https://mcp.example.test#secret",
	} {
		if _, err := registry.PutServer("unsafe", MCPServer{Label: "Unsafe", ServerURL: unsafe, Transport: "sse"}); err == nil {
			t.Fatalf("unsafe URL accepted: %s", unsafe)
		}
	}
}

func TestMCPToolsetGrantExpandsAtAuthorization(t *testing.T) {
	registry := NewMCPRegistry()
	_, err := registry.PutServer("weather", MCPServer{Label: "Weather", ServerURL: "https://mcp.example.test/v1", Transport: "streamable-http", Tools: []string{"mcp:weather-prod@https://mcp.example.test/v1"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.PutToolset("weather", MCPToolset{Name: "Weather tools", Tools: []string{"mcp:weather-prod@https://mcp.example.test/v1"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{mcp: registry}
	if !handler.toolAllowed("mcp:weather-prod@https://mcp.example.test/v1", []string{"toolset:weather"}) {
		t.Fatal("matching toolset grant was denied")
	}
	if handler.toolAllowed("mcp:finance@https://mcp.example.test/v1", []string{"toolset:weather"}) {
		t.Fatal("non-matching toolset grant was allowed")
	}
	_, _ = registry.PutServer("weather", MCPServer{Label: "Weather", ServerURL: "https://mcp.example.test/v1", Transport: "streamable-http", Tools: []string{"*"}, Enabled: false})
	if handler.toolAllowed("mcp:weather-prod@https://mcp.example.test/v1", []string{"toolset:weather"}) {
		t.Fatal("tool on disabled MCP server was allowed")
	}
	_, _ = registry.PutServer("weather", MCPServer{Label: "Weather", ServerURL: "https://mcp.example.test/v1", Transport: "streamable-http", Tools: []string{"*"}, Enabled: true})
	_, _ = registry.PutToolset("weather", MCPToolset{Name: "Weather tools", Tools: []string{"*"}, Enabled: false})
	if handler.toolAllowed("mcp:weather-prod@https://mcp.example.test/v1", []string{"toolset:weather"}) {
		t.Fatal("disabled toolset grant was allowed")
	}
}

func TestMCPToolsetSpecificGrantAllowsConnectorDiscovery(t *testing.T) {
	registry := runtimeRegistry(t, "streamable-http")
	connector := "mcp:weather@https://mcp.example.test/v1"
	if _, err := registry.PutToolset("forecast-only", MCPToolset{Name: "Forecast only", Tools: []string{connector + "#tool:forecast"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if !registry.ToolsetAllowsConnector("forecast-only", connector) || !registry.ToolsetAllows("forecast-only", connector+"#tool:forecast") || registry.ToolsetAllows("forecast-only", connector+"#tool:delete_city") {
		t.Fatal("tool-specific toolset grant was not isolated")
	}
}

func TestMCPRegistryAdminAPIExcludesCredentials(t *testing.T) {
	handler := NewHandler(modulesPipeline("admin"), nil).WithMCPRegistry(NewMCPRegistry())
	router := Routes(handler)
	put := httptest.NewRecorder()
	router.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/admin/v1/mcp/servers/weather", strings.NewReader(`{"label":"Weather","server_url":"https://mcp.example.test","transport":"sse","tools":["mcp:weather@https://mcp.example.test"],"enabled":true}`)))
	if put.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", put.Code, put.Body.String())
	}
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/mcp/servers", nil))
	body := list.Body.String()
	if list.Code != http.StatusOK || !strings.Contains(body, `"id":"weather"`) || strings.Contains(body, "header") || strings.Contains(body, "api_key") || strings.Contains(body, "secret") {
		t.Fatalf("unsafe MCP response: status=%d body=%s", list.Code, body)
	}
}

func TestMCPRegistryAdminCredentialLifecycleNeverReturnsSecret(t *testing.T) {
	registry := NewMCPRegistry()
	handler := NewHandler(modulesPipeline("admin"), nil).WithMCPRegistry(registry)
	router := Routes(handler)
	call := func(body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/admin/v1/mcp/servers/weather", strings.NewReader(body)))
		return response
	}
	created := call(`{"label":"Weather","server_url":"https://mcp.example.test","transport":"streamable-http","tools":[],"enabled":true,"bearer_token":"server-secret"}`)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"credential_configured":true`) || strings.Contains(created.Body.String(), "server-secret") {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	_, secret, found := registry.ServerRuntime("weather")
	if !found || secret != "server-secret" {
		t.Fatalf("stored credential found=%t value=%q", found, secret)
	}
	preserved := call(`{"label":"Weather updated","server_url":"https://mcp.example.test","transport":"streamable-http","tools":[],"enabled":true}`)
	_, secret, _ = registry.ServerRuntime("weather")
	if preserved.Code != http.StatusOK || secret != "server-secret" {
		t.Fatalf("preserve status=%d credential=%q", preserved.Code, secret)
	}
	cleared := call(`{"label":"Weather updated","server_url":"https://mcp.example.test","transport":"streamable-http","tools":[],"enabled":true,"bearer_token":""}`)
	_, secret, _ = registry.ServerRuntime("weather")
	if cleared.Code != http.StatusOK || strings.Contains(cleared.Body.String(), `"credential_configured":true`) || secret != "" {
		t.Fatalf("clear status=%d body=%s credential=%q", cleared.Code, cleared.Body.String(), secret)
	}
	invalid := call("{\"label\":\"Weather\",\"server_url\":\"https://mcp.example.test\",\"transport\":\"streamable-http\",\"enabled\":true,\"bearer_token\":\"line\\nbreak\"}")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestMCPRegistryReferencesProtectDeletes(t *testing.T) {
	registry := NewMCPRegistry()
	if _, err := registry.PutServer("weather", MCPServer{Label: "Weather", ServerURL: "https://mcp.example.test", Transport: "streamable-http", Tools: []string{"mcp:weather@https://mcp.example.test"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutToolset("weather-read", MCPToolset{Name: "Weather read", Tools: []string{"mcp:weather@https://mcp.example.test"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	access := NewAccessRegistry()
	if _, err := access.PutGroup("operators", AccessGroup{Name: "Operators", AllowedTools: []string{"toolset:weather-read"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	management := &recordingManagementClient{page: &VirtualKeyPage{Data: []VirtualKeyMetadata{{ID: "vk-weather", AllowedTools: []string{"toolset:weather-read"}}}, Total: 1, Limit: 500}}
	handler := NewHandler(modulesPipeline("admin"), nil).WithMCPRegistry(registry).WithAccessRegistry(access).WithManagement(management)
	router := Routes(handler)

	servers := httptest.NewRecorder()
	router.ServeHTTP(servers, httptest.NewRequest(http.MethodGet, "/admin/v1/mcp/servers?expand=references", nil))
	if servers.Code != http.StatusOK || !strings.Contains(servers.Body.String(), `"toolset_ids":["weather-read"]`) {
		t.Fatalf("server references status=%d body=%s", servers.Code, servers.Body.String())
	}
	toolsets := httptest.NewRecorder()
	router.ServeHTTP(toolsets, httptest.NewRequest(http.MethodGet, "/admin/v1/mcp/toolsets?expand=references", nil))
	body := toolsets.Body.String()
	if toolsets.Code != http.StatusOK || !strings.Contains(body, `"access_group_ids":["operators"]`) || !strings.Contains(body, `"virtual_key_ids":["vk-weather"]`) {
		t.Fatalf("toolset references status=%d body=%s", toolsets.Code, body)
	}

	blockedServer := httptest.NewRecorder()
	router.ServeHTTP(blockedServer, httptest.NewRequest(http.MethodDelete, "/admin/v1/mcp/servers/weather", nil))
	if blockedServer.Code != http.StatusConflict {
		t.Fatalf("referenced server delete status=%d body=%s", blockedServer.Code, blockedServer.Body.String())
	}
	blockedToolset := httptest.NewRecorder()
	router.ServeHTTP(blockedToolset, httptest.NewRequest(http.MethodDelete, "/admin/v1/mcp/toolsets/weather-read", nil))
	if blockedToolset.Code != http.StatusConflict {
		t.Fatalf("referenced toolset delete status=%d body=%s", blockedToolset.Code, blockedToolset.Body.String())
	}

	if err := access.DeleteGroup("operators"); err != nil {
		t.Fatal(err)
	}
	revokedAt := time.Now()
	management.page = &VirtualKeyPage{Data: []VirtualKeyMetadata{{ID: "vk-revoked", AllowedTools: []string{"toolset:weather-read"}, RevokedAt: &revokedAt}}, Total: 1, Limit: 500}
	deletedToolset := httptest.NewRecorder()
	router.ServeHTTP(deletedToolset, httptest.NewRequest(http.MethodDelete, "/admin/v1/mcp/toolsets/weather-read", nil))
	if deletedToolset.Code != http.StatusNoContent {
		t.Fatalf("unreferenced toolset delete status=%d body=%s", deletedToolset.Code, deletedToolset.Body.String())
	}
	deletedServer := httptest.NewRecorder()
	router.ServeHTTP(deletedServer, httptest.NewRequest(http.MethodDelete, "/admin/v1/mcp/servers/weather", nil))
	if deletedServer.Code != http.StatusNoContent {
		t.Fatalf("unreferenced server delete status=%d body=%s", deletedServer.Code, deletedServer.Body.String())
	}
}
