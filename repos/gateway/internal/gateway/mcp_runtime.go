package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/mcpstate"
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
	server, bearerToken, found := h.mcp.ServerRuntime(serverID)
	if !found || !server.Enabled {
		writeError(w, http.StatusNotFound, "mcp_server_not_found", "MCP server not found")
		return
	}
	if server.Transport != "streamable-http" {
		writeError(w, http.StatusBadRequest, "unsupported_mcp_transport", "MCP runtime requires streamable-http transport")
		return
	}
	identifier := "mcp:" + server.ID + "@" + server.ServerURL
	if !h.authorizeMCPConnector(w, req, identifier) || !h.authorizeRateLimit(w, r.Context(), req, 0) {
		return
	}
	if err := h.resourceBillingPipeline().RunBillingLifecycle(r.Context(), &req, "reserve", nil); err != nil {
		writeMCPBillingFailure(w, err)
		return
	}
	client, err := h.mcpRuntimeClient(server.ServerURL, bearerToken)
	if err != nil {
		req.Metadata["provider.status"] = "error"
		req.Metadata["provider.failure_class"] = "configuration"
		_ = h.resourceBillingPipeline().RunBillingLifecycle(r.Context(), &req, "cancel", err)
		writeError(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP runtime is unavailable")
		return
	}
	page, err := client.ListTools(r.Context(), r.URL.Query().Get("cursor"))
	if err != nil {
		h.invalidateMCPRuntimeClient(server.ServerURL, bearerToken)
		req.Metadata["provider.status"] = "error"
		req.Metadata["provider.failure_class"] = "upstream"
		_ = h.resourceBillingPipeline().RunBillingLifecycle(r.Context(), &req, "cancel", err)
		writeError(w, http.StatusBadGateway, "mcp_server_failed", "MCP server request failed")
		return
	}
	filtered := page.Tools[:0]
	for _, tool := range page.Tools {
		if h.mcpToolAllowed(req, server, identifier, tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	page.Tools = filtered
	req.Metadata["provider.status"] = "ok"
	if err := h.resourceBillingPipeline().RunBillingLifecycle(r.Context(), &req, "commit", nil); err != nil {
		writeMCPBillingFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

type mcpToolCallRequest struct {
	Arguments map[string]any `json:"arguments"`
}

func (h Handler) CallMCPServerTool(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	idempotencyValues := r.Header.Values("Idempotency-Key")
	if len(idempotencyValues) != 1 || !validIdempotencyKey(idempotencyValues[0]) {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "a single visible ASCII Idempotency-Key of at most 128 characters is required")
		return
	}
	var input mcpToolCallRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, (1<<20)+1))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil || input.Arguments == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "arguments must be a JSON object")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain exactly one JSON value")
		return
	}
	serverID := strings.TrimSpace(r.PathValue("id"))
	toolName := strings.TrimSpace(r.PathValue("tool"))
	if !validMCPID(serverID) || toolName == "" || len(toolName) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid MCP server ID or tool name")
		return
	}
	req := modules.RequestContext{
		APIKey:       bearerToken(r.Header.Get("Authorization")),
		RequestID:    executionID(w),
		SessionID:    sessionID(r),
		Request:      openai.ChatCompletionRequest{Provider: "mcp", Model: serverID},
		ToolRequests: 1,
		Metadata:     map[string]string{"gateway.api_type": "mcp_tools_call", "provider.endpoint.name": serverID, "provider.endpoint.type": "mcp"},
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
	if h.mcp == nil || h.mcpRuntime == nil || h.mcpCalls == nil {
		writeError(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP runtime is unavailable")
		return
	}
	if h.audit == nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	server, bearerToken, found := h.mcp.ServerRuntime(serverID)
	if !found || !server.Enabled {
		writeError(w, http.StatusNotFound, "mcp_server_not_found", "MCP server not found")
		return
	}
	if server.Transport != "streamable-http" {
		writeError(w, http.StatusBadRequest, "unsupported_mcp_transport", "MCP runtime requires streamable-http transport")
		return
	}
	identifier := "mcp:" + server.ID + "@" + server.ServerURL
	if !h.authorizeMCPTool(w, req, server, identifier, toolName) || !h.authorizeRateLimit(w, r.Context(), req, 0) {
		return
	}
	encodedArguments, err := json.Marshal(input.Arguments)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "arguments must be a JSON object")
		return
	}
	hash := sha256.Sum256(encodedArguments)
	scope := req.CredentialID + "\x1f" + serverID + "\x1f" + toolName
	key := idempotencyValues[0]
	record, owner, err := h.mcpCalls.Claim(r.Context(), mcpstate.Record{ScopeKey: scope, IdempotencyKey: key, RequestHash: hex.EncodeToString(hash[:]), ExecutionID: req.RequestID})
	if errors.Is(err, mcpstate.ErrConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used with different arguments")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "mcp_state_unavailable", "MCP execution state is unavailable")
		return
	}
	if !owner {
		w.Header().Set("X-Execution-ID", record.ExecutionID)
		if record.State == mcpstate.StateCompleted {
			writeMCPStoredResponse(w, record.HTTPStatus, record.Response)
			return
		}
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusConflict, "idempotency_in_progress", "an MCP tool call with this Idempotency-Key is in progress")
		return
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "mcp.tool.call", TargetType: "mcp_tool", TargetID: serverID + "/" + toolName, Details: map[string]any{"server_id": serverID, "tool": toolName}}
	if !h.auditMutation(r.Context(), audit, event) {
		_ = h.mcpCalls.Release(r.Context(), scope, key, req.RequestID)
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := h.resourceBillingPipeline().RunBillingLifecycle(r.Context(), &req, "reserve", nil); err != nil {
		_ = h.mcpCalls.Release(r.Context(), scope, key, req.RequestID)
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeMCPBillingFailure(w, err)
		return
	}
	client, err := h.mcpRuntimeClient(server.ServerURL, bearerToken)
	if err != nil {
		_ = h.resourceBillingPipeline().RunBillingLifecycle(r.Context(), &req, "cancel", err)
		_ = h.mcpCalls.Release(r.Context(), scope, key, req.RequestID)
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP runtime is unavailable")
		return
	}
	result, err := client.CallTool(r.Context(), toolName, input.Arguments)
	if err != nil {
		h.invalidateMCPRuntimeClient(server.ServerURL, bearerToken)
		req.Metadata["provider.status"] = "error"
		req.Metadata["provider.failure_class"] = "upstream"
		_ = h.resourceBillingPipeline().RunBillingLifecycle(r.Context(), &req, "cancel", err)
		payload := mcpErrorPayload("mcp_server_failed", "MCP server request failed")
		_ = h.mcpCalls.Complete(r.Context(), scope, key, req.RequestID, http.StatusBadGateway, payload)
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeMCPStoredResponse(w, http.StatusBadGateway, payload)
		return
	}
	req.Metadata["provider.status"] = "ok"
	if err := h.resourceBillingPipeline().RunBillingLifecycle(r.Context(), &req, "commit", nil); err != nil {
		payload := mcpErrorPayload("billing_unavailable", "billing is unavailable")
		_ = h.mcpCalls.Complete(r.Context(), scope, key, req.RequestID, http.StatusServiceUnavailable, payload)
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeMCPStoredResponse(w, http.StatusServiceUnavailable, payload)
		return
	}
	payload, err := json.Marshal(result)
	if err != nil || h.mcpCalls.Complete(r.Context(), scope, key, req.RequestID, http.StatusOK, payload) != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusServiceUnavailable, "mcp_state_unavailable", "MCP execution result could not be persisted")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeMCPStoredResponse(w, http.StatusOK, payload)
}

func (h Handler) authorizeMCPConnector(w http.ResponseWriter, req modules.RequestContext, connector string) bool {
	if !h.mcpConnectorAllowed(connector, req.AllowedTools) {
		writeError(w, http.StatusForbidden, "tool_not_allowed", "credential is not allowed to use this MCP connector")
		return false
	}
	if req.AccessGroupsEvaluated && !h.mcpConnectorAllowed(connector, req.AccessGroupTools) {
		writeError(w, http.StatusForbidden, "access_group_tool_not_allowed", "assigned access groups do not allow the requested MCP connector")
		return false
	}
	return true
}

func (h Handler) authorizeMCPTool(w http.ResponseWriter, req modules.RequestContext, server MCPServer, connector, tool string) bool {
	identifier := connector + "#tool:" + tool
	if !mcpToolAllowedByGrants(connector, identifier, server.Tools) {
		writeError(w, http.StatusForbidden, "mcp_tool_not_registered", "MCP tool is not allowed by the server registry")
		return false
	}
	if !h.mcpToolGrantAllowed(connector, identifier, req.AllowedTools) {
		writeError(w, http.StatusForbidden, "tool_not_allowed", "credential is not allowed to use the requested MCP tool")
		return false
	}
	if req.AccessGroupsEvaluated && !h.mcpToolGrantAllowed(connector, identifier, req.AccessGroupTools) {
		writeError(w, http.StatusForbidden, "access_group_tool_not_allowed", "assigned access groups do not allow the requested MCP tool")
		return false
	}
	return true
}

func (h Handler) mcpToolAllowed(req modules.RequestContext, server MCPServer, connector, tool string) bool {
	identifier := connector + "#tool:" + tool
	return mcpToolAllowedByGrants(connector, identifier, server.Tools) &&
		h.mcpToolGrantAllowed(connector, identifier, req.AllowedTools) &&
		(!req.AccessGroupsEvaluated || h.mcpToolGrantAllowed(connector, identifier, req.AccessGroupTools))
}

func mcpToolAllowedByGrants(connector, identifier string, grants []string) bool {
	return toolAllowed(connector, grants) || toolAllowed(identifier, grants)
}

func (h Handler) mcpToolGrantAllowed(connector, identifier string, grants []string) bool {
	if mcpToolAllowedByGrants(connector, identifier, grants) {
		return true
	}
	if h.mcp == nil {
		return false
	}
	for _, grant := range grants {
		if strings.HasPrefix(grant, "toolset:") && (h.mcp.ToolsetAllows(strings.TrimPrefix(grant, "toolset:"), connector) || h.mcp.ToolsetAllows(strings.TrimPrefix(grant, "toolset:"), identifier)) {
			return true
		}
	}
	return false
}

func (h Handler) mcpConnectorAllowed(connector string, grants []string) bool {
	if len(grants) == 0 {
		return true
	}
	for _, grant := range grants {
		if mcpGrantAllowsConnector(grant, connector) {
			return true
		}
		if strings.HasPrefix(grant, "toolset:") && h.mcp != nil && h.mcp.ToolsetAllowsConnector(strings.TrimPrefix(grant, "toolset:"), connector) {
			return true
		}
	}
	return false
}

func validIdempotencyKey(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index := range len(value) {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func mcpErrorPayload(code, message string) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": message}, "ts": time.Now().UTC()})
	return payload
}

func writeMCPStoredResponse(w http.ResponseWriter, status int, payload json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func writeMCPBillingFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, modules.ErrBudgetExceeded) {
		writeError(w, http.StatusTooManyRequests, "budget_exceeded", "budget exceeded")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "billing_unavailable", "billing is unavailable")
}
