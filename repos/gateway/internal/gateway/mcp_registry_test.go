package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
