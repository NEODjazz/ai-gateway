package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"ai-gateway-gateway/internal/mcpclient"
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

type MCPServerReferences struct {
	ToolsetIDs []string `json:"toolset_ids"`
}

type MCPToolsetReferences struct {
	AccessGroupIDs []string `json:"access_group_ids"`
	VirtualKeyIDs  []string `json:"virtual_key_ids"`
}
type MCPRegistry struct {
	mu       sync.RWMutex
	servers  map[string]MCPServer
	toolsets map[string]MCPToolset
}

var (
	errInvalidMCPRegistryEntry = errors.New("invalid MCP registry entry")
	errMCPRegistryNotFound     = errors.New("MCP registry entry not found")
	errMCPRegistryInUse        = errors.New("MCP registry entry is in use")
)

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
func (r *MCPRegistry) Server(id string) (MCPServer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	server, ok := r.servers[id]
	server.Tools = append([]string(nil), server.Tools...)
	return server, ok
}

type MCPRuntimeClient interface {
	ListTools(context.Context, string) (mcpclient.ToolPage, error)
}

type MCPRuntimeFactory func(string) (MCPRuntimeClient, error)

func (h Handler) WithMCPRuntimeFactory(factory MCPRuntimeFactory) Handler {
	h.mcpRuntime = factory
	return h
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

func (r *MCPRegistry) ServerReferences(id string) (MCPServerReferences, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.serverReferencesLocked(id)
}

func (r *MCPRegistry) serverReferencesLocked(id string) (MCPServerReferences, bool) {
	server, ok := r.servers[id]
	if !ok {
		return MCPServerReferences{}, false
	}
	result := MCPServerReferences{ToolsetIDs: []string{}}
	for toolsetID, toolset := range r.toolsets {
		for _, identifier := range toolset.Tools {
			if toolAllowed(identifier, server.Tools) {
				result.ToolsetIDs = append(result.ToolsetIDs, toolsetID)
				break
			}
		}
	}
	sort.Strings(result.ToolsetIDs)
	return result, true
}

func (r *MCPRegistry) DeleteServer(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	references, exists := r.serverReferencesLocked(id)
	if !exists {
		return errMCPRegistryNotFound
	}
	if len(references.ToolsetIDs) != 0 {
		return errMCPRegistryInUse
	}
	delete(r.servers, id)
	return nil
}

func (r *MCPRegistry) DeleteToolset(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.toolsets[id]; !ok {
		return false
	}
	delete(r.toolsets, id)
	return true
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
	expand := strings.TrimSpace(r.URL.Query().Get("expand"))
	if !allowedValue(expand, "", "references") {
		writeError(w, http.StatusBadRequest, "invalid_request", "expand must be references")
		return
	}
	servers := h.mcp.Servers()
	response := map[string]any{"data": servers}
	if expand == "references" {
		references := make(map[string]MCPServerReferences, len(servers))
		for _, server := range servers {
			references[server.ID], _ = h.mcp.ServerReferences(server.ID)
		}
		response["references"] = references
	}
	writeJSON(w, http.StatusOK, response)
}
func (h Handler) ListMCPToolsets(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.mcp == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "MCP registry is unavailable")
		return
	}
	expand := strings.TrimSpace(r.URL.Query().Get("expand"))
	if !allowedValue(expand, "", "references") {
		writeError(w, http.StatusBadRequest, "invalid_request", "expand must be references")
		return
	}
	toolsets := h.mcp.Toolsets()
	response := map[string]any{"data": toolsets}
	if expand == "references" {
		references, err := h.mcpToolsetReferences(r.Context(), managementAudit(req), toolsets)
		if err != nil {
			writeManagementFailure(w, err)
			return
		}
		response["references"] = references
	}
	writeJSON(w, http.StatusOK, response)
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

func (h Handler) DeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.mcp == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "MCP registry is unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !validMCPID(id) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid MCP server ID")
		return
	}
	references, exists := h.mcp.ServerReferences(id)
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "MCP server not found")
		return
	}
	event := AuditEvent{Action: "mcp_server.delete", TargetType: "mcp_server", TargetID: id}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if len(references.ToolsetIDs) != 0 {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusConflict, "resource_in_use", "MCP server is referenced by one or more toolsets")
		return
	}
	if err := h.mcp.DeleteServer(id); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		if errors.Is(err, errMCPRegistryInUse) {
			writeError(w, http.StatusConflict, "resource_in_use", "MCP server is referenced by one or more toolsets")
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "MCP server not found")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) DeleteMCPToolset(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.mcp == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "MCP registry is unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !validMCPID(id) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid MCP toolset ID")
		return
	}
	toolsets := h.mcp.Toolsets()
	found := false
	for _, toolset := range toolsets {
		if toolset.ID == id {
			toolsets = []MCPToolset{toolset}
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "MCP toolset not found")
		return
	}
	event := AuditEvent{Action: "mcp_toolset.delete", TargetType: "mcp_toolset", TargetID: id}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	references, err := h.mcpToolsetReferences(r.Context(), audit, toolsets)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeManagementFailure(w, err)
		return
	}
	reference := references[id]
	if len(reference.AccessGroupIDs) != 0 || len(reference.VirtualKeyIDs) != 0 {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusConflict, "resource_in_use", "MCP toolset is assigned to virtual keys or access groups")
		return
	}
	h.mcp.DeleteToolset(id)
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) mcpToolsetReferences(ctx context.Context, audit ManagementAudit, toolsets []MCPToolset) (map[string]MCPToolsetReferences, error) {
	result := make(map[string]MCPToolsetReferences, len(toolsets))
	for _, toolset := range toolsets {
		result[toolset.ID] = MCPToolsetReferences{AccessGroupIDs: []string{}, VirtualKeyIDs: []string{}}
	}
	if h.access != nil {
		for _, group := range h.access.Groups() {
			for _, grant := range group.AllowedTools {
				id := strings.TrimPrefix(grant, "toolset:")
				if grant != "toolset:"+id {
					continue
				}
				reference, exists := result[id]
				if exists {
					reference.AccessGroupIDs = append(reference.AccessGroupIDs, group.ID)
					result[id] = reference
				}
			}
		}
	}
	keys, err := h.allVirtualKeys(ctx, audit)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		if key.RevokedAt != nil {
			continue
		}
		for _, grant := range key.AllowedTools {
			id := strings.TrimPrefix(grant, "toolset:")
			if grant != "toolset:"+id {
				continue
			}
			reference, exists := result[id]
			if exists {
				reference.VirtualKeyIDs = append(reference.VirtualKeyIDs, key.ID)
				result[id] = reference
			}
		}
	}
	for id, reference := range result {
		reference.AccessGroupIDs = uniqueStrings(reference.AccessGroupIDs)
		reference.VirtualKeyIDs = uniqueStrings(reference.VirtualKeyIDs)
		sort.Strings(reference.AccessGroupIDs)
		sort.Strings(reference.VirtualKeyIDs)
		result[id] = reference
	}
	return result, nil
}

func (h Handler) allVirtualKeys(ctx context.Context, audit ManagementAudit) ([]VirtualKeyMetadata, error) {
	if h.management == nil {
		return nil, errors.New("management service is not configured")
	}
	if pager, ok := h.management.(interface {
		ListVirtualKeysPage(context.Context, ManagementAudit, VirtualKeyListFilter) (VirtualKeyPage, error)
	}); ok {
		const pageSize = 500
		result := []VirtualKeyMetadata{}
		for offset := 0; ; {
			page, err := pager.ListVirtualKeysPage(ctx, audit, VirtualKeyListFilter{Limit: pageSize, Offset: offset, SortBy: "key", SortOrder: "asc"})
			if err != nil {
				return nil, err
			}
			result = append(result, page.Data...)
			if len(page.Data) == 0 || offset+len(page.Data) >= page.Total {
				return result, nil
			}
			offset += len(page.Data)
		}
	}
	keys, err := h.management.ListVirtualKeys(ctx, audit, 500)
	if err != nil {
		return nil, err
	}
	if len(keys) == 500 {
		return nil, errors.New("management service cannot prove complete virtual-key references")
	}
	return keys, nil
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
