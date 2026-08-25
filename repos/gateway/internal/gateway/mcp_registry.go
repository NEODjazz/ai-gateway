package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
)

type MCPServer struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	ServerURL   string   `json:"server_url"`
	Transport   string   `json:"transport"`
	Tools       []string `json:"tools,omitempty"`
	Enabled     bool     `json:"enabled"`
}
type MCPToolset struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tools       []string `json:"tools"`
	Enabled     bool     `json:"enabled"`
}
type MCPRegistry struct {
	mu       sync.RWMutex
	servers  map[string]MCPServer
	toolsets map[string]MCPToolset
}

var errInvalidMCPRegistryEntry = errors.New("invalid MCP registry entry")

func NewMCPRegistry() *MCPRegistry {
	return &MCPRegistry{servers: map[string]MCPServer{}, toolsets: map[string]MCPToolset{}}
}
func (h Handler) WithMCPRegistry(registry *MCPRegistry) Handler { h.mcp = registry; return h }
func (r *MCPRegistry) Servers() []MCPServer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]MCPServer, 0, len(r.servers))
	for _, server := range r.servers {
		server.Tools = append([]string(nil), server.Tools...)
		result = append(result, server)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (r *MCPRegistry) Toolsets() []MCPToolset {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]MCPToolset, 0, len(r.toolsets))
	for _, toolset := range r.toolsets {
		toolset.Tools = append([]string(nil), toolset.Tools...)
		result = append(result, toolset)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (r *MCPRegistry) PutServer(id string, server MCPServer) (MCPServer, error) {
	id = strings.TrimSpace(id)
	server.Label = strings.TrimSpace(server.Label)
	server.Description = strings.TrimSpace(server.Description)
	server.Transport = strings.TrimSpace(server.Transport)
	parsed, err := url.Parse(server.ServerURL)
	if !validMCPID(id) || server.Label == "" || len(server.Label) > 128 || len(server.Description) > 1024 || err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (server.Transport != "streamable-http" && server.Transport != "sse") || !validMCPTools(server.Tools) {
		return MCPServer{}, errInvalidMCPRegistryEntry
	}
	parsed.Scheme = "https"
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	server.ID = id
	server.ServerURL = parsed.String()
	server.Tools = uniqueStrings(server.Tools)
	r.mu.Lock()
	r.servers[id] = server
	r.mu.Unlock()
	return server, nil
}
func (r *MCPRegistry) PutToolset(id string, toolset MCPToolset) (MCPToolset, error) {
	id = strings.TrimSpace(id)
	toolset.Name = strings.TrimSpace(toolset.Name)
	toolset.Description = strings.TrimSpace(toolset.Description)
	if !validMCPID(id) || toolset.Name == "" || len(toolset.Name) > 256 || len(toolset.Description) > 1024 || !validMCPTools(toolset.Tools) {
		return MCPToolset{}, errInvalidMCPRegistryEntry
	}
	toolset.ID = id
	toolset.Tools = uniqueStrings(toolset.Tools)
	r.mu.Lock()
	r.toolsets[id] = toolset
	r.mu.Unlock()
	return toolset, nil
}
func (r *MCPRegistry) ToolsetAllows(id, identifier string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	toolset, ok := r.toolsets[id]
	if !ok || !toolset.Enabled {
		return false
	}
	if strings.HasPrefix(identifier, "mcp:") && !r.enabledServerAllows(identifier) {
		return false
	}
	for _, grant := range toolset.Tools {
		if toolAllowed(identifier, []string{grant}) {
			return true
		}
	}
	return false
}

func (r *MCPRegistry) enabledServerAllows(identifier string) bool {
	for _, server := range r.servers {
		if !server.Enabled {
			continue
		}
		for _, grant := range server.Tools {
			if toolAllowed(identifier, []string{grant}) {
				return true
			}
		}
	}
	return false
}
func validMCPID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}
func validMCPTools(values []string) bool {
	if len(values) > 256 {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 512 {
			return false
		}
	}
	return true
}
func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func (h Handler) ListMCPServers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.mcp == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "MCP registry is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": h.mcp.Servers()})
}
func (h Handler) ListMCPToolsets(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.mcp == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "MCP registry is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": h.mcp.Toolsets()})
}
func (h Handler) UpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.mcp == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "MCP registry is unavailable")
		return
	}
	var input struct {
		Label       string   `json:"label"`
		Description string   `json:"description,omitempty"`
		ServerURL   string   `json:"server_url"`
		Transport   string   `json:"transport"`
		Tools       []string `json:"tools,omitempty"`
		Enabled     bool     `json:"enabled"`
	}
	if !decodeMCPJSON(w, r, &input) {
		return
	}
	id := r.PathValue("id")
	event := AuditEvent{Action: "mcp_server.update", TargetType: "mcp_server", TargetID: id}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := h.mcp.PutServer(id, MCPServer{Label: input.Label, Description: input.Description, ServerURL: input.ServerURL, Transport: input.Transport, Tools: input.Tools, Enabled: input.Enabled})
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid MCP server")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}
func (h Handler) UpdateMCPToolset(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.mcp == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "MCP registry is unavailable")
		return
	}
	var input struct {
		Name        string   `json:"name"`
		Description string   `json:"description,omitempty"`
		Tools       []string `json:"tools"`
		Enabled     bool     `json:"enabled"`
	}
	if !decodeMCPJSON(w, r, &input) {
		return
	}
	id := r.PathValue("id")
	event := AuditEvent{Action: "mcp_toolset.update", TargetType: "mcp_toolset", TargetID: id}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := h.mcp.PutToolset(id, MCPToolset{Name: input.Name, Description: input.Description, Tools: input.Tools, Enabled: input.Enabled})
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid MCP toolset")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}
func decodeMCPJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid MCP registry entry")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid MCP registry entry")
		return false
	}
	return true
}
