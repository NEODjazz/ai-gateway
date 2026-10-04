package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/mcpclient"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

const agentMCPMaxTools = 32
const agentMCPMaxBytes = 2 << 20

type agentMCPContextKey struct{}
type agentMCPAuthorization struct {
	profile  AgentProfile
	bindings map[string]AgentMCPTool
	servers  map[string]string
	approved map[string]bool
}

func validAgentMCPTools(tools []AgentMCPTool) bool {
	if len(tools) > agentMCPMaxTools {
		return false
	}
	seen := make(map[AgentMCPTool]bool, len(tools))
	for _, tool := range tools {
		if !validMCPID(tool.ServerID) || tool.ToolName == "" || len(tool.ToolName) > 256 || strings.TrimSpace(tool.ToolName) != tool.ToolName || !utf8.ValidString(tool.ToolName) || strings.ContainsFunc(tool.ToolName, func(r rune) bool { return r < 0x20 || r == 0x7f }) || seen[tool] {
			return false
		}
		seen[tool] = true
	}
	return true
}

func agentMCPFunctionName(binding AgentMCPTool) string {
	digest := sha256.Sum256([]byte(binding.ServerID + "\x00" + binding.ToolName))
	return "mcp_" + hex.EncodeToString(digest[:16])
}

func (h Handler) agentToolMatches(grants []string, binding AgentMCPTool, connector string) bool {
	return len(grants) > 0 && (toolAllowed(binding.ToolName, grants) || h.mcpToolGrantAllowed(connector, connector+"#tool:"+binding.ToolName, grants))
}

// Materialized grants are an upper bound. A current policy may revoke access,
// require approval or lower limits without silently widening a saved profile.
func (h Handler) effectiveAgentMCPProfile(saved AgentProfile) (AgentProfile, error) {
	if h.agents == nil || h.mcp == nil {
		return AgentProfile{}, errInvalidAgentEntry
	}
	current, ok := h.agents.AgentProfile(saved.ID)
	if !ok || !current.Enabled || !current.ExecutionSupported || current.Model != saved.Model || current.Instructions != saved.Instructions || current.ToolPolicyID != saved.ToolPolicyID || !sameAgentMCPTools(current.MCPTools, saved.MCPTools) || agentMCPConfiguration(current) != agentMCPConfiguration(saved) {
		return AgentProfile{}, errInvalidAgentEntry
	}
	h.agents.mu.RLock()
	policy, ok := h.agents.policies[current.ToolPolicyID]
	policy.AllowedTools = append([]string(nil), policy.AllowedTools...)
	policy.DeniedTools = append([]string(nil), policy.DeniedTools...)
	policy.ApprovalRequired = append([]string(nil), policy.ApprovalRequired...)
	h.agents.mu.RUnlock()
	if !ok || !policy.Enabled {
		return AgentProfile{}, errInvalidAgentEntry
	}
	current.MaxToolCalls = min(saved.MaxToolCalls, current.MaxToolCalls, policy.MaxToolCalls)
	current.MaxIterations = min(saved.MaxIterations, current.MaxIterations)
	// Preserve both saved and current denies/approvals. Allowed grants are checked
	// individually below because intersecting wildcard strings is insufficient.
	current.DeniedTools = append(append(current.DeniedTools, saved.DeniedTools...), policy.DeniedTools...)
	current.ApprovalRequired = append(append(current.ApprovalRequired, saved.ApprovalRequired...), policy.ApprovalRequired...)
	for _, binding := range saved.MCPTools {
		server, exists := h.mcp.Server(binding.ServerID)
		if !exists || !server.Enabled {
			return AgentProfile{}, errInvalidAgentEntry
		}
		connector := "mcp:" + server.ID + "@" + server.ServerURL
		if !h.agentToolMatches(saved.AllowedTools, binding, connector) || !h.agentToolMatches(current.AllowedTools, binding, connector) || !h.agentToolMatches(policy.AllowedTools, binding, connector) || h.agentToolMatches(current.DeniedTools, binding, connector) {
			return AgentProfile{}, errInvalidAgentEntry
		}
	}
	return current, nil
}

func sameAgentMCPTools(a, b []AgentMCPTool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (h Handler) authorizeAgentResponseTools(w http.ResponseWriter, r *http.Request, req modules.RequestContext, identifiers []string, valid bool) bool {
	auth, ok := r.Context().Value(agentMCPContextKey{}).(agentMCPAuthorization)
	if !ok {
		return h.authorizeTools(w, req, identifiers, valid)
	}
	if !valid || h.mcp == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid agent tool definition")
		return false
	}
	if _, err := h.effectiveAgentMCPProfile(auth.profile); err != nil || !h.agentMCPServersUnchanged(r) {
		writeError(w, http.StatusForbidden, "agent_tool_policy_changed", "Agent MCP configuration or tool policy no longer permits execution")
		return false
	}
	for _, name := range identifiers {
		binding, found := auth.bindings[name]
		if !found {
			writeError(w, http.StatusForbidden, "tool_not_allowed", "Agent requested an unbound tool")
			return false
		}
		server, found := h.mcp.Server(binding.ServerID)
		if !found || !server.Enabled || !h.authorizeMCPTool(w, req, server, "mcp:"+server.ID+"@"+server.ServerURL, binding.ToolName) {
			return false
		}
	}
	return true
}

func agentMCPRequest(r *http.Request, method, path string, body []byte) *http.Request {
	request := r.Clone(r.Context())
	url := *r.URL
	url.Path = path
	url.RawPath = ""
	url.RawQuery = ""
	request.URL = &url
	request.Method = method
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Del("Idempotency-Key")
	return request
}

type agentMCPCapture struct{ *a2aResponseCapture }

func (w agentMCPCapture) Write(data []byte) (int, error) {
	if len(data) > agentMCPMaxBytes-w.body.Len() {
		return 0, errors.New("agent MCP response exceeds 2 MiB")
	}
	return w.a2aResponseCapture.Write(data)
}

func (h Handler) discoverAgentMCPTools(r *http.Request, profile AgentProfile) ([]openai.ResponseTool, map[string]AgentMCPTool, *a2aResponseCapture) {
	tools := make([]openai.ResponseTool, 0, len(profile.MCPTools))
	bindings := make(map[string]AgentMCPTool, len(profile.MCPTools))
	pages := make(map[string]map[string]mcpclient.Tool)
	total := 0
	for _, binding := range profile.MCPTools {
		definitions, loaded := pages[binding.ServerID]
		if !loaded {
			definitions = make(map[string]mcpclient.Tool)
			cursor := ""
			seen := map[string]bool{}
			for pageIndex := 0; ; pageIndex++ {
				capture := newA2AResponseCapture()
				if pageIndex >= 8 {
					writeError(capture, http.StatusBadGateway, "agent_tools_limit", "MCP discovery exceeded eight pages")
					return nil, nil, capture
				}
				request := agentMCPRequest(r, http.MethodGet, "/v1/mcp/servers/"+binding.ServerID+"/tools", nil)
				request.SetPathValue("id", binding.ServerID)
				query := request.URL.Query()
				if cursor != "" {
					query.Set("cursor", cursor)
				}
				request.URL.RawQuery = query.Encode()
				h.ListMCPServerTools(agentMCPCapture{capture}, request)
				if capture.status != http.StatusOK {
					return nil, nil, capture
				}
				var page mcpclient.ToolPage
				if json.Unmarshal(capture.body.Bytes(), &page) != nil || len(page.Tools) > 256 {
					capture = newA2AResponseCapture()
					writeError(capture, http.StatusBadGateway, "agent_tools_invalid", "MCP returned invalid tool discovery")
					return nil, nil, capture
				}
				total += capture.body.Len()
				if total > agentMCPMaxBytes {
					capture = newA2AResponseCapture()
					writeError(capture, http.StatusBadGateway, "agent_tools_limit", "MCP definitions exceed 2 MiB")
					return nil, nil, capture
				}
				for _, definition := range page.Tools {
					if _, duplicate := definitions[definition.Name]; duplicate {
						capture = newA2AResponseCapture()
						writeError(capture, http.StatusBadGateway, "agent_tools_invalid", "MCP returned duplicate tool names")
						return nil, nil, capture
					}
					definitions[definition.Name] = definition
				}
				if page.NextCursor == "" {
					break
				}
				if len(page.NextCursor) > 2048 || seen[page.NextCursor] {
					capture = newA2AResponseCapture()
					writeError(capture, http.StatusBadGateway, "agent_tools_invalid", "MCP returned an invalid pagination cursor")
					return nil, nil, capture
				}
				seen[page.NextCursor] = true
				cursor = page.NextCursor
			}
			pages[binding.ServerID] = definitions
		}
		definition, found := definitions[binding.ToolName]
		var schema map[string]any
		decoder := json.NewDecoder(bytes.NewReader(definition.InputSchema))
		decoder.UseNumber()
		if !found || decoder.Decode(&schema) != nil || schema == nil || decoder.Decode(&struct{}{}) != io.EOF {
			capture := newA2AResponseCapture()
			writeError(capture, http.StatusForbidden, "agent_tool_unavailable", "Selected agent MCP tool is unavailable to this credential")
			return nil, nil, capture
		}
		name := agentMCPFunctionName(binding)
		tools = append(tools, openai.ResponseTool{Type: "function", Name: name, Description: "MCP tool " + binding.ToolName + " on server " + binding.ServerID + ".\n" + definition.Description, Parameters: schema})
		bindings[name] = binding
	}
	return tools, bindings, nil
}

func (h Handler) serveAgentResponsesAs(w http.ResponseWriter, r *http.Request, request openai.ResponseRequest, profile AgentProfile, transform func(openai.ResponseResponse, modules.RequestContext) any) {
	h.serveResponsesAs(w, r, request, "a2a", transform, nil, nil, false)
}

func copyAgentMCPResponse(w http.ResponseWriter, capture *a2aResponseCapture) {
	if capture.status == 0 || capture.body.Len() == 0 {
		writeError(w, http.StatusBadGateway, "agent_response_invalid", "Agent received an invalid or oversized response")
		return
	}
	copyA2AHeaders(w, capture.header)
	w.WriteHeader(capture.status)
	_, _ = w.Write(capture.body.Bytes())
}

func (h Handler) validateAgentMCPCalls(w http.ResponseWriter, profile AgentProfile, bindings map[string]AgentMCPTool, calls []openai.ResponseOutputItem, seenCalls map[string]bool) ([]map[string]any, bool) {
	arguments := make([]map[string]any, len(calls))
	for i, call := range calls {
		binding, found := bindings[call.Name]
		if !found || call.CallID == "" || len(call.CallID) > 128 || seenCalls[call.CallID] || len(call.Arguments) > 1<<20 {
			writeError(w, http.StatusBadGateway, "agent_tool_call_invalid", "Model returned an invalid or repeated agent tool call")
			return nil, false
		}
		seenCalls[call.CallID] = true
		server, found := h.mcp.Server(binding.ServerID)
		if !found || h.agentToolMatches(profile.ApprovalRequired, binding, "mcp:"+server.ID+"@"+server.ServerURL) {
			writeError(w, http.StatusConflict, "agent_approval_required", "Agent tool requires approval; no tools in this step were executed")
			return nil, false
		}
		decoder := json.NewDecoder(strings.NewReader(call.Arguments))
		decoder.UseNumber()
		if decoder.Decode(&arguments[i]) != nil || arguments[i] == nil || decoder.Decode(&struct{}{}) != io.EOF {
			writeError(w, http.StatusBadGateway, "agent_tool_call_invalid", "Model returned invalid agent tool arguments")
			return nil, false
		}
	}
	return arguments, true
}

func (h Handler) executeAgentMCPCall(w http.ResponseWriter, r *http.Request, profile AgentProfile, binding AgentMCPTool, call openai.ResponseOutputItem, arguments map[string]any, executionID string, callsMade, iteration int) (string, bool) {
	if r.Context().Err() != nil {
		writeError(w, http.StatusRequestTimeout, "agent_execution_cancelled", "Agent execution was cancelled")
		return "", false
	}
	current, err := h.effectiveAgentMCPProfile(profile)
	server, found := h.mcp.Server(binding.ServerID)
	if err != nil || !found || !h.agentMCPServersUnchanged(r) || callsMade >= current.MaxToolCalls || iteration+1 >= current.MaxIterations || h.agentToolMatches(current.ApprovalRequired, binding, "mcp:"+server.ID+"@"+server.ServerURL) && !agentMCPCallApproved(r, call) {
		writeError(w, http.StatusForbidden, "agent_tool_policy_changed", "Agent tool policy no longer permits execution")
		return "", false
	}
	body, err := json.Marshal(mcpToolCallRequest{Arguments: arguments})
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent_tool_call_invalid", "Invalid agent tool arguments")
		return "", false
	}
	toolRequest := agentMCPRequest(r, http.MethodPost, "/v1/mcp/servers/"+binding.ServerID+"/tools", body)
	toolRequest.SetPathValue("id", binding.ServerID)
	toolRequest.SetPathValue("tool", binding.ToolName)
	digest := sha256.Sum256([]byte(executionID + "\x00" + call.CallID))
	toolRequest.Header.Set("Idempotency-Key", "agent-"+hex.EncodeToString(digest[:]))
	toolCapture := newA2AResponseCapture()
	h.CallMCPServerTool(agentMCPCapture{toolCapture}, toolRequest)
	if toolCapture.status != http.StatusOK || toolCapture.body.Len() == 0 {
		copyAgentMCPResponse(w, toolCapture)
		return "", false
	}
	return toolCapture.body.String(), true
}

func (h Handler) agentMCPServersUnchanged(r *http.Request) bool {
	auth, ok := r.Context().Value(agentMCPContextKey{}).(agentMCPAuthorization)
	if !ok || h.mcp == nil {
		return false
	}
	for id, expected := range auth.servers {
		server, found := h.mcp.Server(id)
		if !found || !server.Enabled || server.ServerURL+"\x00"+server.Transport != expected {
			return false
		}
	}
	return true
}
