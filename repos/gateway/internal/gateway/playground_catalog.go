package gateway

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type playgroundResource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type playgroundToolset struct {
	playgroundResource
	ServerIDs  []string            `json:"server_ids"`
	ToolGrants map[string][]string `json:"tool_grants"`
}

type playgroundAgent struct {
	playgroundResource
	Model              string `json:"model"`
	ExecutionSupported bool   `json:"execution_supported"`
}

type playgroundCatalog struct {
	MCPServers  []playgroundResource `json:"mcp_servers"`
	MCPToolsets []playgroundToolset  `json:"mcp_toolsets"`
	Policies    []string             `json:"policies"`
	PolicyError string               `json:"policy_error,omitempty"`
	Tags        []string             `json:"tags"`
	Agents      []playgroundAgent    `json:"agents"`
	Truncated   bool                 `json:"truncated"`
}

// PlaygroundCatalog exposes selectable registry names under the request's own
// identity. It never invokes MCP or exposes registry credentials or endpoints.
func (h Handler) PlaygroundCatalog(w http.ResponseWriter, r *http.Request) {
	for key, values := range r.URL.Query() {
		if key != "model" || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "only a single model query parameter is supported")
			return
		}
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if len(model) > 256 || strings.ContainsAny(model, "\r\n\x00") {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is invalid")
		return
	}
	req := modules.RequestContext{APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), Request: openai.ChatCompletionRequest{Model: model}}
	if err := h.pipeline.RunAuthentication(r.Context(), &req); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
		} else {
			writeError(w, http.StatusServiceUnavailable, "authentication_unavailable", "authentication is unavailable")
		}
		return
	}
	req.APIKey = ""
	if h.adminState != nil && h.adminState.Refresh(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "admin_state_unavailable", "resource configuration is unavailable")
		return
	}
	if !h.prepareAccessGroups(w, &req) || model != "" && !h.authorizeModel(w, req, model) || !h.authorizeRateLimit(w, r.Context(), req, 0) {
		return
	}
	result := playgroundCatalog{MCPServers: []playgroundResource{}, MCPToolsets: []playgroundToolset{}, Policies: []string{}, Tags: uniqueStrings(req.Tags), Agents: []playgroundAgent{}}
	if result.Tags == nil {
		result.Tags = []string{}
	}
	slices.Sort(result.Tags)
	if h.mcp != nil {
		for _, server := range h.mcp.Servers() {
			connector := "mcp:" + server.ID + "@" + server.ServerURL
			if !server.Enabled || req.ToolAccessRestricted && len(req.AllowedTools) == 0 || !h.mcpConnectorAllowed(connector, req.AllowedTools) || req.AccessGroupsEvaluated && (len(req.AccessGroupTools) == 0 || !h.mcpConnectorAllowed(connector, req.AccessGroupTools)) {
				continue
			}
			result.MCPServers = append(result.MCPServers, playgroundResource{ID: server.ID, Name: server.Label})
		}
		for _, toolset := range h.mcp.Toolsets() {
			if !toolset.Enabled || !h.playgroundToolAllowed(req, "toolset:"+toolset.ID) {
				continue
			}
			item := playgroundToolset{playgroundResource: playgroundResource{ID: toolset.ID, Name: toolset.Name}, ServerIDs: []string{}, ToolGrants: map[string][]string{}}
			for _, resource := range result.MCPServers {
				server, found := h.mcp.Server(resource.ID)
				if found && h.mcp.ToolsetAllowsConnector(toolset.ID, "mcp:"+server.ID+"@"+server.ServerURL) {
					if len(item.ServerIDs) < 256 {
						item.ServerIDs = append(item.ServerIDs, server.ID)
						connector := "mcp:" + server.ID + "@" + server.ServerURL
						patterns := []string{}
						for _, grant := range toolset.Tools {
							if toolAllowed(connector, []string{grant}) {
								patterns = []string{"*"}
								break
							}
							if strings.HasPrefix(grant, connector+"#tool:") {
								patterns = append(patterns, strings.TrimPrefix(grant, connector+"#tool:"))
							}
						}
						item.ToolGrants[server.ID] = patterns
					}
				}
			}
			if len(item.ServerIDs) > 0 {
				result.MCPToolsets = append(result.MCPToolsets, item)
			}
		}
	}
	controller, available := h.guardrailController()
	if !available {
		result.PolicyError = "Guardrail policy discovery is unavailable."
	} else if hasRole(req.Roles, "admin") {
		for _, policy := range controller.ListGuardrailPolicies() {
			if policy.Enabled {
				result.Policies = append(result.Policies, policy.Name)
			}
		}
	} else if h.access == nil {
		result.PolicyError = "Policy attachment discovery is unavailable."
	} else {
		match := guardrailPolicyMatch(req, model)
		resolution := h.resolvePolicyAttachmentSet(h.access.MatchingPolicyAttachments(match), match)
		if !resolution.Enforceable {
			result.PolicyError = "An attached policy is missing or disabled."
		} else {
			result.Policies = append(result.Policies, resolution.EffectivePolicies...)
		}
	}
	slices.Sort(result.Policies)
	if h.agents != nil {
		for _, profile := range h.agents.AgentProfiles() {
			if !profile.Enabled || !requestModelAllowed(req, profile.Model) || req.AccessGroupsEvaluated && (len(req.AccessGroupModels) == 0 || !modelAllowed(profile.Model, req.AccessGroupModels)) {
				continue
			}
			if h.access != nil {
				if allowed, _ := h.access.TagModelAllowed(req.Tags, profile.Model); !allowed {
					continue
				}
			}
			result.Agents = append(result.Agents, playgroundAgent{playgroundResource: playgroundResource{ID: profile.ID, Name: profile.Name}, Model: profile.Model, ExecutionSupported: profile.ExecutionSupported})
		}
	}
	const maximumEntries = 256
	result.Truncated = len(result.MCPServers) > maximumEntries || len(result.MCPToolsets) > maximumEntries || len(result.Policies) > maximumEntries || len(result.Tags) > maximumEntries || len(result.Agents) > maximumEntries
	result.MCPServers = result.MCPServers[:min(len(result.MCPServers), maximumEntries)]
	result.MCPToolsets = result.MCPToolsets[:min(len(result.MCPToolsets), maximumEntries)]
	result.Policies = result.Policies[:min(len(result.Policies), maximumEntries)]
	result.Tags = result.Tags[:min(len(result.Tags), maximumEntries)]
	result.Agents = result.Agents[:min(len(result.Agents), maximumEntries)]
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (h Handler) playgroundToolAllowed(req modules.RequestContext, identifier string) bool {
	return (!req.ToolAccessRestricted || len(req.AllowedTools) > 0) && h.toolAllowed(identifier, req.AllowedTools) &&
		(!req.AccessGroupsEvaluated || len(req.AccessGroupTools) > 0 && h.toolAllowed(identifier, req.AccessGroupTools))
}
