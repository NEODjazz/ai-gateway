package gateway

import (
	"errors"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func (h Handler) ListMCPServerTools(w http.ResponseWriter, r *http.Request) {
	for key, values := range r.URL.Query() {
		if key != "cursor" || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported query parameter "+key)
			return
		}
	}
	if len(r.URL.Query().Get("cursor")) > 2048 {
		writeError(w, http.StatusBadRequest, "invalid_request", "MCP cursor exceeds limit")
		return
	}
	serverID := strings.TrimSpace(r.PathValue("id"))
	if !validMCPID(serverID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid MCP server ID")
		return
	}
	req := modules.RequestContext{
		APIKey:    bearerToken(r.Header.Get("Authorization")),
		RequestID: executionID(w),
		SessionID: sessionID(r),
		Request:   openai.ChatCompletionRequest{Provider: "mcp", Model: serverID},
		Metadata:  map[string]string{"gateway.api_type": "mcp_tools_list", "provider.endpoint.name": serverID, "provider.endpoint.type": "mcp"},
	}
	if err := h.pipeline.RunAuthentication(r.Context(), &req); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return
		}
		writeError(w, http.StatusBadGateway, "module_failed", "authentication failed")
		return
	}
	req.APIKey = ""
	if !h.prepareAccessGroups(w, &req) {
		return
	}
	if h.mcp == nil || h.mcpRuntime == nil {
		writeError(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP runtime is unavailable")
		return
	}
	server, found := h.mcp.Server(serverID)
	if !found || !server.Enabled {
		writeError(w, http.StatusNotFound, "mcp_server_not_found", "MCP server not found")
		return
	}
	if server.Transport != "streamable-http" {
		writeError(w, http.StatusBadRequest, "unsupported_mcp_transport", "MCP runtime requires streamable-http transport")
		return
	}
	identifier := "mcp:" + server.ID + "@" + server.ServerURL
	if !h.authorizeTools(w, req, []string{identifier}, true) || !h.authorizeRateLimit(w, r.Context(), req, 0) {
		return
	}
	if err := h.pipeline.RunBillingLifecycle(r.Context(), &req, "reserve", nil); err != nil {
		writeMCPBillingFailure(w, err)
		return
	}
	client, err := h.mcpRuntime(server.ServerURL)
	if err != nil {
		req.Metadata["provider.status"] = "error"
		req.Metadata["provider.failure_class"] = "configuration"
		_ = h.pipeline.RunBillingLifecycle(r.Context(), &req, "cancel", err)
		writeError(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP runtime is unavailable")
		return
	}
	page, err := client.ListTools(r.Context(), r.URL.Query().Get("cursor"))
	if err != nil {
		req.Metadata["provider.status"] = "error"
		req.Metadata["provider.failure_class"] = "upstream"
		_ = h.pipeline.RunBillingLifecycle(r.Context(), &req, "cancel", err)
		writeError(w, http.StatusBadGateway, "mcp_server_failed", "MCP server request failed")
		return
	}
	req.Metadata["provider.status"] = "ok"
	if err := h.pipeline.RunBillingLifecycle(r.Context(), &req, "commit", nil); err != nil {
		writeMCPBillingFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func writeMCPBillingFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, modules.ErrBudgetExceeded) {
		writeError(w, http.StatusTooManyRequests, "budget_exceeded", "budget exceeded")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "billing_unavailable", "billing is unavailable")
}
