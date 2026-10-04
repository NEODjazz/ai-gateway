package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func (h Handler) runAgentMCPTask(w http.ResponseWriter, r *http.Request, rpc a2aRequest, run *agentMCPTaskRun) {
	state := run.state
	profile, err := h.effectiveAgentMCPProfile(state.Profile)
	if err != nil {
		writeError(w, http.StatusForbidden, "agent_tool_policy_changed", "Agent configuration or policy no longer permits execution")
		return
	}
	servers := make(map[string]string)
	for _, binding := range profile.MCPTools {
		server, ok := h.mcp.Server(binding.ServerID)
		if !ok {
			writeError(w, http.StatusForbidden, "agent_tool_unavailable", "Agent MCP server is unavailable")
			return
		}
		servers[server.ID] = server.ServerURL + "\x00" + server.Transport
	}
	if len(state.Servers) > 0 && agentMCPDigest(state.Servers) != agentMCPDigest(servers) {
		writeError(w, http.StatusForbidden, "agent_tool_policy_changed", "Agent MCP server configuration changed")
		return
	}
	state.Servers = servers
	tools, bindings, failure := h.discoverAgentMCPTools(r, profile)
	if failure != nil {
		copyAgentMCPResponse(w, failure)
		return
	}
	digest := agentMCPDigest(tools)
	if state.ToolDigest != "" && state.ToolDigest != digest {
		writeError(w, http.StatusConflict, "agent_tool_schema_changed", "MCP tool definitions changed; start a new conversation")
		return
	}
	state.ToolDigest = digest
	auth := agentMCPAuthorization{profile: state.Profile, bindings: bindings, servers: servers, approved: map[string]bool{}}
	r = r.WithContext(context.WithValue(r.Context(), agentMCPContextKey{}, auth))
	for {
		profile, err = h.effectiveAgentMCPProfile(state.Profile)
		if err != nil || !h.agentMCPServersUnchanged(r) {
			writeError(w, http.StatusForbidden, "agent_tool_policy_changed", "Agent configuration or tool policy changed during execution")
			return
		}
		if len(state.Pending) > 0 {
			if !h.executeAgentMCPPending(w, r, run, profile, bindings) {
				return
			}
			if run.task.Status.State == "TASK_STATE_INPUT_REQUIRED" {
				writeAgentMCPTask(w, rpc.ID, run.task)
				return
			}
		}
		if state.Iterations >= profile.MaxIterations {
			writeError(w, http.StatusConflict, "agent_execution_limit", "Agent reached its model iteration limit")
			return
		}
		state.Iterations++
		if err := h.checkpointAgentMCPTask(r.Context(), run); err != nil {
			h.writeAgentMCPStoreError(w, rpc.ID, err)
			return
		}
		request := openai.ResponseRequest{Model: profile.Model, Instructions: profile.Instructions, Input: state.Input, Tools: tools}
		if profile.Generation != nil {
			request.Temperature, request.MaxOutputTokens = profile.Generation.Temperature, profile.Generation.MaxOutputTokens
		}
		capture := newA2AResponseCapture()
		var response openai.ResponseResponse
		h.serveResponsesAs(agentMCPCapture{capture}, r, request, "a2a", func(result openai.ResponseResponse, _ modules.RequestContext) any { response = result; return result }, nil, nil, false)
		if capture.status != http.StatusOK || capture.body.Len() == 0 {
			copyAgentMCPResponse(w, capture)
			return
		}
		copyA2AHeaders(w, capture.header)
		if response.Status != "completed" {
			writeError(w, http.StatusBadGateway, "agent_response_incomplete", "Agent model did not complete its synchronous response")
			return
		}
		var calls []openai.ResponseOutputItem
		for _, output := range response.Output {
			switch output.Type {
			case "function_call":
				calls = append(calls, output)
			case "message", "reasoning":
			default:
				writeError(w, http.StatusBadGateway, "agent_tool_call_invalid", "Model returned unsupported agent output")
				return
			}
		}
		if len(calls) == 0 {
			if _, err := h.effectiveAgentMCPProfile(state.Profile); err != nil || !h.agentMCPServersUnchanged(r) {
				writeError(w, http.StatusForbidden, "agent_tool_policy_changed", "Agent configuration or policy changed during execution")
				return
			}
			for _, output := range response.Output {
				state.Input = append(state.Input, output)
			}
			text := responseOutputText(response)
			if len(response.Output) == 0 {
				state.Input = append(state.Input, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": *text}}})
			}
			message := a2aMessage{MessageID: newA2AID("msg"), ContextID: run.task.ContextID, TaskID: run.task.ID, Role: "ROLE_AGENT", Parts: []a2aPart{{Text: text}}}
			run.task.History = append(run.task.History, message)
			run.task.Artifacts = []a2aArtifact{{ArtifactID: newA2AID("artifact"), Parts: message.Parts}}
			run.task.Status = a2aTaskStatus{State: "TASK_STATE_COMPLETED", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
			// Observe cancellation after the provider settles, before publishing success.
			if err := h.checkpointAgentMCPTask(r.Context(), run); err != nil {
				h.writeAgentMCPStoreError(w, rpc.ID, err)
				run.task.Status.State = "TASK_STATE_WORKING"
				return
			}
			writeAgentMCPTask(w, rpc.ID, run.task)
			return
		}
		profile, err = h.effectiveAgentMCPProfile(state.Profile)
		if err != nil || !h.agentMCPServersUnchanged(r) {
			writeError(w, http.StatusForbidden, "agent_tool_policy_changed", "Agent configuration or policy changed during execution")
			return
		}
		if state.Iterations >= profile.MaxIterations || len(calls) > profile.MaxToolCalls-state.Calls || len(calls) > agentMCPMaxTools {
			writeError(w, http.StatusConflict, "agent_execution_limit", "Agent reached its tool-call or model iteration limit; no further tools were executed")
			return
		}
		// Approval is a state transition, not an exemption from validation.
		validationProfile := profile
		validationProfile.ApprovalRequired = nil
		if _, valid := h.validateAgentMCPCalls(w, validationProfile, bindings, calls, state.Seen); !valid {
			return
		}
		for _, output := range response.Output {
			state.Input = append(state.Input, output)
		}
		for _, call := range calls {
			state.Pending = append(state.Pending, agentMCPPendingCall{Call: call})
		}
		if err := h.checkpointAgentMCPTask(r.Context(), run); err != nil {
			h.writeAgentMCPStoreError(w, rpc.ID, err)
			return
		}
	}
}

func (h Handler) executeAgentMCPPending(w http.ResponseWriter, r *http.Request, run *agentMCPTaskRun, profile AgentProfile, bindings map[string]AgentMCPTool) bool {
	state := run.state
	if state.Iterations >= profile.MaxIterations || len(state.Pending) > profile.MaxToolCalls-state.Calls {
		writeError(w, http.StatusConflict, "agent_execution_limit", "Agent tool-call or model iteration limit no longer permits this step")
		return false
	}
	needsApproval := false
	for i := range state.Pending {
		pending := &state.Pending[i]
		binding, found := bindings[pending.Call.Name]
		server, exists := h.mcp.Server(binding.ServerID)
		if !found || !exists {
			writeError(w, http.StatusForbidden, "agent_tool_unavailable", "Pending tool is unavailable")
			return false
		}
		if h.agentToolMatches(profile.ApprovalRequired, binding, "mcp:"+server.ID+"@"+server.ServerURL) {
			pending.Required = true
		}
		if pending.Required && pending.Approved == nil {
			needsApproval = true
		}
	}
	if needsApproval {
		state.Challenge = newA2AID("approval")
		calls := make([]map[string]any, 0, len(state.Pending))
		for _, pending := range state.Pending {
			if !pending.Required {
				continue
			}
			binding := bindings[pending.Call.Name]
			arguments := json.RawMessage(pending.Call.Arguments)
			if !json.Valid(arguments) {
				writeError(w, http.StatusBadGateway, "agent_tool_call_invalid", "Pending tool arguments are invalid")
				return false
			}
			calls = append(calls, map[string]any{"call_id": pending.Call.CallID, "server_id": binding.ServerID, "tool_name": binding.ToolName, "arguments": arguments})
		}
		run.task.Status = agentMCPTaskStatus(run.task, "TASK_STATE_INPUT_REQUIRED", "Review and approve or decline the pending tool calls before this agent continues.", map[string]any{agentApprovalMetadata: map[string]any{"approval_id": state.Challenge, "calls": calls}})
		if err := h.checkpointAgentMCPTask(r.Context(), run); err != nil {
			h.writeAgentMCPStoreError(w, nil, err)
			run.task.Status.State = "TASK_STATE_WORKING"
			state.Challenge = ""
			return false
		}
		return true
	}
	for len(state.Pending) > 0 {
		pending := state.Pending[0]
		if err := h.checkpointAgentMCPTask(r.Context(), run); err != nil {
			h.writeAgentMCPStoreError(w, nil, err)
			return false
		}
		output := `{"isError":true,"content":[{"type":"text","text":"Tool execution declined by the user."}]}`
		if !pending.Required || pending.Approved != nil && *pending.Approved {
			var arguments map[string]any
			decoder := json.NewDecoder(strings.NewReader(pending.Call.Arguments))
			decoder.UseNumber()
			if decoder.Decode(&arguments) != nil || arguments == nil {
				writeError(w, http.StatusBadGateway, "agent_tool_call_invalid", "Pending arguments are invalid")
				return false
			}
			auth, _ := r.Context().Value(agentMCPContextKey{}).(agentMCPAuthorization)
			if pending.Required {
				auth.approved[agentMCPDigest(pending.Call)] = true
			}
			var executed bool
			output, executed = h.executeAgentMCPCall(w, r, state.Profile, bindings[pending.Call.Name], pending.Call, arguments, state.RunID, state.Calls, state.Iterations-1)
			delete(auth.approved, agentMCPDigest(pending.Call))
			if !executed {
				return false
			}
			state.Calls++
		}
		state.Input = append(state.Input, map[string]any{"type": "function_call_output", "call_id": pending.Call.CallID, "output": output})
		state.Pending = state.Pending[1:]
		if err := h.checkpointAgentMCPTask(r.Context(), run); err != nil {
			h.writeAgentMCPStoreError(w, nil, err)
			return false
		}
	}
	return true
}
