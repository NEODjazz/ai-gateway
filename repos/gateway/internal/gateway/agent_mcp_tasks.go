package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/openai"
)

const agentApprovalMetadata = "ai_gateway_tool_approval"

// This state is private: public task reads expose Task, never this wrapper.
// Instructions and connector/provider credentials are deliberately not stored.
type agentMCPTaskState struct {
	Version         int                   `json:"version"`
	Profile         AgentProfile          `json:"profile"`
	Configuration   string                `json:"configuration"`
	InitialHash     string                `json:"initialHash"`
	MessageID       string                `json:"messageId"`
	MessageHash     string                `json:"messageHash"`
	RunID           string                `json:"runId"`
	Deadline        time.Time             `json:"deadline"`
	CancelRequested bool                  `json:"cancelRequested,omitempty"`
	Input           []any                 `json:"input"`
	Iterations      int                   `json:"iterations"`
	Calls           int                   `json:"calls"`
	Seen            map[string]bool       `json:"seen"`
	Servers         map[string]string     `json:"servers"`
	ToolDigest      string                `json:"toolDigest,omitempty"`
	Pending         []agentMCPPendingCall `json:"pending,omitempty"`
	Challenge       string                `json:"challenge,omitempty"`
}

type agentMCPPendingCall struct {
	Call     openai.ResponseOutputItem `json:"call"`
	Required bool                      `json:"required"`
	Approved *bool                     `json:"approved,omitempty"`
}

type agentApprovalDecision struct {
	ApprovalID string `json:"approval_id"`
	Choices    []struct {
		CallID   string `json:"call_id"`
		Approved *bool  `json:"approved"`
	} `json:"choices"`
}

type agentMCPTaskRun struct {
	stored a2astate.Task
	task   a2aTask
	state  *agentMCPTaskState
}

func agentMCPDigest(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func agentMCPConfiguration(profile AgentProfile) string {
	return agentMCPDigest(struct {
		ID, Model, Instructions, Policy string
		Tools                           []AgentMCPTool
		Generation                      *AgentGeneration
	}{profile.ID, profile.Model, profile.Instructions, profile.ToolPolicyID, profile.MCPTools, profile.Generation})
}

func agentMCPMessageHash(message a2aMessage) string {
	// Server-assigned context/task IDs are not part of initial retry identity.
	message.ContextID, message.TaskID = "", ""
	return agentMCPDigest(message)
}

func agentMCPCallApproved(r *http.Request, call openai.ResponseOutputItem) bool {
	auth, _ := r.Context().Value(agentMCPContextKey{}).(agentMCPAuthorization)
	return auth.approved[agentMCPDigest(call)]
}

func validAgentMCPTaskState(task a2aTask, state *agentMCPTaskState) bool {
	if state.Version != 1 || state.Profile.ID == "" || !validAgentMCPTools(state.Profile.MCPTools) || len(state.Profile.MCPTools) == 0 || len(state.Configuration) != 64 || len(state.InitialHash) != 64 || len(state.MessageHash) != 64 || state.MessageID == "" || !validFileToken(state.RunID, 128) || state.Deadline.IsZero() || state.Input == nil || state.Iterations < 0 || state.Iterations > 50 || state.Calls < 0 || state.Calls > 1000 || len(state.Seen) > 2500 || len(state.Pending) > agentMCPMaxTools || len(state.Servers) > agentMCPMaxTools {
		return false
	}
	if task.Status.State == "TASK_STATE_INPUT_REQUIRED" {
		if !validFileToken(state.Challenge, 128) || len(state.Pending) == 0 || task.Status.Message == nil {
			return false
		}
	} else if state.Challenge != "" {
		return false
	}
	for _, pending := range state.Pending {
		if pending.Call.Type != "function_call" || pending.Call.CallID == "" || len(pending.Call.CallID) > 128 || !state.Seen[pending.Call.CallID] {
			return false
		}
	}
	return true
}

func decodeAgentMCPState(payload []byte) (*agentMCPTaskState, error) {
	if _, _, err := decodeA2AStoredTask(payload); err != nil {
		return nil, err
	}
	var stored a2aStoredTask
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&stored) != nil {
		return nil, a2astate.ErrInvalid
	}
	return stored.AgentMCP, nil
}

func encodeAgentMCPTask(task a2aTask, state *agentMCPTaskState) ([]byte, error) {
	if !validStoredA2ATask(task) || !validAgentMCPTaskState(task, state) {
		return nil, a2astate.ErrInvalid
	}
	payload, err := json.Marshal(a2aStoredTask{Task: task, AgentMCP: state})
	if err != nil || len(payload) > a2astate.MaxPayloadBytes {
		return nil, a2astate.ErrInvalid
	}
	return payload, nil
}

func (h Handler) sendAgentMCPTask(w http.ResponseWriter, r *http.Request, request a2aRequest, profile AgentProfile) {
	if h.a2aTasks == nil || h.a2aTaskConfig.OwnerQuota < 1 || h.a2aTaskConfig.TTL <= 0 || h.mcp == nil || h.mcpCalls == nil || h.mcpRuntime == nil || h.audit == nil {
		h.writeA2AError(w, request.ID, http.StatusServiceUnavailable, -32603, "Durable agent task and MCP runtime services are required")
		return
	}
	if r.Context().Err() != nil {
		h.writeA2AError(w, request.ID, http.StatusRequestTimeout, -32603, "Agent execution was cancelled")
		return
	}
	identity, ok := h.authenticateA2AContinuation(w, r, request.ID)
	if !ok || !h.authorizeA2ATaskModel(w, request.ID, identity, profile.Model) {
		return
	}
	owner := fileOwnerKey(identity)
	taskID := request.Params.Message.TaskID
	initial := taskID == ""
	if initial {
		taskID = "task_" + agentMCPDigest([]string{owner, profile.ID, request.Params.Message.MessageID})
	}
	stored, err := h.a2aTasks.GetA2ATask(r.Context(), owner, profile.ID, taskID)
	messageHash := agentMCPMessageHash(request.Params.Message)
	var run agentMCPTaskRun
	if err == nil {
		state, decodeErr := decodeAgentMCPState(stored.Payload)
		task, taskErr := decodeA2ATask(stored.Payload)
		if decodeErr != nil || taskErr != nil || state == nil || task.ID != stored.ID || task.ContextID != stored.ContextID || task.Status.State != stored.State || stored.Model != profile.Model || state.Profile.ID != stored.AgentID || state.Profile.Model != stored.Model {
			h.writeA2ATaskStoreError(w, request.ID, a2astate.ErrInvalid)
			return
		}
		if (!initial || request.Params.Message.ContextID != "") && request.Params.Message.ContextID != task.ContextID {
			h.writeA2AError(w, request.ID, http.StatusConflict, -32602, "Task context does not match")
			return
		}
		if initial || state.MessageID == request.Params.Message.MessageID {
			expected := state.MessageHash
			if initial {
				expected = state.InitialHash
			}
			if expected != messageHash {
				h.writeA2AError(w, request.ID, http.StatusConflict, -32602, "Message ID was already used with different content")
				return
			}
			if !h.reconcileAgentMCPTask(w, r, request, stored, task, state) {
				writeAgentMCPReply(w, request, task)
			}
			return
		}
		if state.Configuration != agentMCPConfiguration(profile) || a2aHistoryContainsMessage(task.History, request.Params.Message.MessageID) {
			h.writeA2AError(w, request.ID, http.StatusConflict, -32602, "Task configuration or message conflicts with stored execution")
			return
		}
		run = agentMCPTaskRun{stored: stored, task: task, state: state}
		if task.Status.State == "TASK_STATE_INPUT_REQUIRED" {
			if err := applyAgentMCPApproval(request.Params.Message.Metadata, state); err != nil {
				h.writeA2AError(w, request.ID, http.StatusConflict, -32602, "Approval must match the pending task and all required calls")
				return
			}
		} else if task.Status.State == "TASK_STATE_COMPLETED" {
			if _, supplied := request.Params.Message.Metadata[agentApprovalMetadata]; supplied {
				h.writeA2AError(w, request.ID, http.StatusConflict, -32602, "Task has no pending approval")
				return
			}
			state.RunID, state.Iterations, state.Calls = newA2AID("run"), 0, 0
			state.Pending = nil
			// Stored native history retains tool call/result pairs, not only text artifacts.
			if !h.appendAgentMCPInput(w, r, request, &run, true) {
				return
			}
		} else {
			h.writeA2AError(w, request.ID, http.StatusConflict, -32602, "Task cannot be continued in its current state")
			return
		}
		request.Params.Message.ContextID, request.Params.Message.TaskID = task.ContextID, task.ID
		run.task.History = append(run.task.History, request.Params.Message)
		run.task.Artifacts = nil
		state.MessageID, state.MessageHash, state.Challenge = request.Params.Message.MessageID, messageHash, ""
		state.Deadline, state.CancelRequested = time.Now().UTC().Add(2*time.Minute), false
		run.task.Status = a2aTaskStatus{State: "TASK_STATE_WORKING", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
		if err := h.saveAgentMCPTask(r.Context(), &run); err != nil {
			h.writeAgentMCPStoreError(w, request.ID, err)
			return
		}
	} else if initial && errors.Is(err, a2astate.ErrNotFound) {
		if _, supplied := request.Params.Message.Metadata[agentApprovalMetadata]; supplied {
			h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Initial message cannot approve tools")
			return
		}
		contextID := request.Params.Message.ContextID
		if contextID == "" {
			contextID = newA2AID("ctx")
		}
		request.Params.Message.ContextID, request.Params.Message.TaskID = contextID, taskID
		state := &agentMCPTaskState{Version: 1, Profile: cloneAgentProfile(profile), Configuration: agentMCPConfiguration(profile), InitialHash: messageHash, MessageID: request.Params.Message.MessageID, MessageHash: messageHash, RunID: newA2AID("run"), Deadline: time.Now().UTC().Add(2 * time.Minute), Input: []any{}, Seen: map[string]bool{}, Servers: map[string]string{}}
		run = agentMCPTaskRun{stored: a2astate.Task{ID: taskID, OwnerKey: owner, AgentID: profile.ID, Model: profile.Model, ContextID: contextID}, task: a2aTask{ID: taskID, ContextID: contextID, Status: a2aTaskStatus{State: "TASK_STATE_WORKING", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}, History: []a2aMessage{request.Params.Message}}, state: state}
		if !h.appendAgentMCPInput(w, r, request, &run, true) {
			return
		}
		payload, encodeErr := encodeAgentMCPTask(run.task, state)
		if encodeErr != nil {
			h.writeAgentMCPStoreError(w, request.ID, encodeErr)
			return
		}
		run.stored.State, run.stored.Payload = run.task.Status.State, payload
		run.stored, err = h.a2aTasks.CreateA2ATask(r.Context(), run.stored, h.a2aTaskConfig.OwnerQuota, h.a2aTaskConfig.TTL)
		if err != nil {
			// A racing initial send owns execution. Never run effects after losing create.
			if errors.Is(err, a2astate.ErrConflict) || errors.Is(err, a2astate.ErrQuotaExceeded) {
				if existing, readErr := h.a2aTasks.GetA2ATask(r.Context(), owner, profile.ID, taskID); readErr == nil {
					previous, decodeErr := decodeAgentMCPState(existing.Payload)
					if decodeErr == nil && previous != nil && previous.InitialHash == messageHash {
						task, _ := decodeA2ATask(existing.Payload)
						writeAgentMCPReply(w, request, task)
						return
					}
				}
			}
			h.writeAgentMCPStoreError(w, request.ID, err)
			return
		}
	} else {
		h.writeA2ATaskStoreError(w, request.ID, err)
		return
	}
	// Reload encrypted instructions only after checking the immutable configuration digest.
	run.state.Profile.Instructions = profile.Instructions
	ctx, cancel := context.WithDeadline(r.Context(), run.state.Deadline)
	defer cancel()
	stream := request.Method == "SendStreamingMessage"
	if stream {
		if err := startAgentMCPStream(w, request.ID, run.task); err != nil {
			// A disconnected client must not start effects after its durable claim.
			cleanup, stop := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer stop()
			if err := h.failAgentMCPTask(cleanup, &run, true); err != nil {
				_ = writeA2AStreamError(w, request.ID, -32603, "Task status is temporarily unavailable; read the task before retrying")
			}
			return
		}
	}
	capture := newA2AResponseCapture()
	if ctx.Err() != nil {
		writeError(capture, http.StatusRequestTimeout, "agent_execution_cancelled", "Agent execution was cancelled")
	} else {
		h.runAgentMCPTask(agentMCPCapture{capture}, r.WithContext(ctx), request, &run)
	}
	if capture.status != http.StatusOK {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer stop()
		if a2aTaskPending(run.task.Status.State) {
			if err := h.failAgentMCPTask(cleanup, &run, errors.Is(ctx.Err(), context.Canceled)); err != nil {
				if stream {
					_ = writeA2AStreamError(w, request.ID, -32603, "Task status is temporarily unavailable; read the task before retrying")
				} else {
					h.writeAgentMCPStoreError(w, request.ID, err)
				}
				return
			}
		}
	}
	if stream {
		// Both success and failure states are durable before the final event. Model
		// responses are buffered per iteration; no partial tool step is published.
		_ = finishAgentMCPStream(w, request.ID, run.task)
		return
	}
	copyA2AResponse(w, capture, request.ID)
}

func (h Handler) failAgentMCPTask(ctx context.Context, run *agentMCPTaskRun, canceled bool) error {
	current, err := h.a2aTasks.GetA2ATask(ctx, run.stored.OwnerKey, run.stored.AgentID, run.stored.ID)
	if err != nil {
		return err
	}
	state, err := decodeAgentMCPState(current.Payload)
	if err != nil || state == nil || state.RunID != run.state.RunID || state.MessageID != run.state.MessageID || !a2aTaskPending(current.State) {
		return a2astate.ErrConflict
	}
	run.stored = current
	status, text := "TASK_STATE_FAILED", "Agent execution failed. Completed calls are not retried automatically."
	if canceled || state.CancelRequested || run.state.CancelRequested {
		status, text = "TASK_STATE_CANCELED", "Agent execution stopped. Completed or interrupted calls are not retried."
	}
	run.task.Status = agentMCPTaskStatus(run.task, status, text, nil)
	run.state.Pending, run.state.Challenge = nil, ""
	if _, err := encodeAgentMCPTask(run.task, run.state); err != nil {
		// A large result must not leave an unfinishable WORKING task. Preserve the
		// last bounded durable intent; actual call results remain in the MCP store.
		run.task, err = decodeA2ATask(current.Payload)
		if err != nil {
			return err
		}
		run.state = state
		run.state.Pending, run.state.Challenge = nil, ""
		run.task.Status = agentMCPTaskStatus(run.task, status, text, nil)
	}
	return h.saveAgentMCPTask(ctx, run)
}

func (h Handler) appendAgentMCPInput(w http.ResponseWriter, r *http.Request, request a2aRequest, run *agentMCPTaskRun, resolve bool) bool {
	parts := request.Params.Message.Parts
	if resolve {
		if err := h.resolveA2ARemoteParts(r.Context(), parts); err != nil {
			h.writeA2AError(w, request.ID, http.StatusBadGateway, -32005, "Remote content is unavailable")
			return false
		}
	}
	content := make([]any, 0, len(parts))
	for _, part := range parts {
		input, ok := a2aInputPart(part)
		if !ok {
			h.writeA2AError(w, request.ID, http.StatusBadRequest, -32005, "Content type is not supported")
			return false
		}
		content = append(content, input)
	}
	run.state.Input = append(run.state.Input, map[string]any{"role": "user", "content": content})
	return true
}

func applyAgentMCPApproval(metadata map[string]any, state *agentMCPTaskState) error {
	encoded, err := json.Marshal(metadata[agentApprovalMetadata])
	if err != nil {
		return a2astate.ErrInvalid
	}
	var decision agentApprovalDecision
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&decision) != nil || decoder.Decode(&struct{}{}) != io.EOF || decision.ApprovalID != state.Challenge || len(decision.Choices) > agentMCPMaxTools {
		return a2astate.ErrInvalid
	}
	choices := make(map[string]bool)
	for _, choice := range decision.Choices {
		if choice.Approved == nil {
			return a2astate.ErrInvalid
		}
		if _, duplicate := choices[choice.CallID]; duplicate {
			return a2astate.ErrInvalid
		}
		choices[choice.CallID] = *choice.Approved
	}
	for i := range state.Pending {
		if !state.Pending[i].Required {
			continue
		}
		approved, exists := choices[state.Pending[i].Call.CallID]
		if !exists {
			return a2astate.ErrInvalid
		}
		state.Pending[i].Approved = &approved
		delete(choices, state.Pending[i].Call.CallID)
	}
	if len(choices) != 0 {
		return a2astate.ErrInvalid
	}
	return nil
}

func (h Handler) saveAgentMCPTask(ctx context.Context, run *agentMCPTaskRun) error {
	payload, err := encodeAgentMCPTask(run.task, run.state)
	if err != nil {
		return err
	}
	updated := run.stored
	updated.State, updated.Payload = run.task.Status.State, payload
	updated, err = h.a2aTasks.UpdateA2ATask(ctx, updated, run.stored.UpdatedAt, h.a2aTaskConfig.TTL)
	if err == nil {
		run.stored = updated
	}
	return err
}

// A cancel update may interrupt a running claim; only that same run can observe
// it and persist results. No expired or concurrent worker takes over unknown work.
func (h Handler) checkpointAgentMCPTask(ctx context.Context, run *agentMCPTaskRun) error {
	current, err := h.a2aTasks.GetA2ATask(ctx, run.stored.OwnerKey, run.stored.AgentID, run.stored.ID)
	if err != nil {
		return err
	}
	if !current.UpdatedAt.Equal(run.stored.UpdatedAt) {
		state, err := decodeAgentMCPState(current.Payload)
		if err != nil || state == nil || state.RunID != run.state.RunID || !state.CancelRequested || current.State != "TASK_STATE_WORKING" {
			return a2astate.ErrConflict
		}
		run.stored, run.state.CancelRequested = current, true
	}
	if run.state.CancelRequested {
		return context.Canceled
	}
	return h.saveAgentMCPTask(ctx, run)
}

func writeAgentMCPTask(w http.ResponseWriter, id json.RawMessage, task a2aTask) {
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, a2aRPCResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{"task": task}})
}

func (h Handler) writeAgentMCPStoreError(w http.ResponseWriter, id json.RawMessage, err error) {
	switch {
	case errors.Is(err, a2astate.ErrConflict):
		h.writeA2AError(w, id, http.StatusConflict, -32602, "Task changed concurrently; read its status before retrying")
	case errors.Is(err, a2astate.ErrQuotaExceeded):
		h.writeA2AError(w, id, http.StatusTooManyRequests, -32603, "Task quota exceeded")
	case errors.Is(err, a2astate.ErrInvalid):
		h.writeA2AError(w, id, http.StatusRequestEntityTooLarge, -32603, "Agent task exceeds its bounded storage limit or contains invalid state")
	default:
		h.writeA2ATaskStoreError(w, id, err)
	}
}

func agentMCPTaskStatus(task a2aTask, state, text string, metadata map[string]any) a2aTaskStatus {
	return a2aTaskStatus{State: state, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Message: &a2aMessage{MessageID: newA2AID("msg"), ContextID: task.ContextID, TaskID: task.ID, Role: "ROLE_AGENT", Parts: []a2aPart{{Text: &text}}, Metadata: metadata}}
}

func (h Handler) reconcileAgentMCPTask(w http.ResponseWriter, r *http.Request, request a2aRequest, stored a2astate.Task, task a2aTask, state *agentMCPTaskState) bool {
	if !a2aTaskPending(task.Status.State) || time.Now().Before(state.Deadline) {
		return false
	}
	text, status := "Execution deadline elapsed. Interrupted work is not retried automatically.", "TASK_STATE_FAILED"
	if state.CancelRequested {
		text, status = "Execution stopped after cancellation was requested.", "TASK_STATE_CANCELED"
	}
	task.Status = agentMCPTaskStatus(task, status, text, nil)
	state.Pending, state.Challenge = nil, ""
	run := agentMCPTaskRun{stored: stored, task: task, state: state}
	if err := h.saveAgentMCPTask(r.Context(), &run); err != nil {
		h.writeAgentMCPStoreError(w, request.ID, err)
		return true
	}
	trimA2AHistory(&task, request.Params.HistoryLength)
	if request.Method == "GetTask" {
		writeJSON(w, http.StatusOK, a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: task})
	} else {
		writeAgentMCPReply(w, request, task)
	}
	return true
}

func (h Handler) cancelAgentMCPTask(w http.ResponseWriter, r *http.Request, request a2aRequest, stored a2astate.Task, task a2aTask, state *agentMCPTaskState) {
	if task.Status.State != "TASK_STATE_INPUT_REQUIRED" && !a2aTaskPending(task.Status.State) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32002, "Task cannot be canceled")
		return
	}
	state.CancelRequested = true
	if task.Status.State == "TASK_STATE_INPUT_REQUIRED" {
		task.Status = agentMCPTaskStatus(task, "TASK_STATE_CANCELED", "Pending tools were canceled without execution.", nil)
		state.Pending, state.Challenge = nil, ""
	} else {
		task.Status = agentMCPTaskStatus(task, "TASK_STATE_WORKING", "Cancellation requested; current execution must stop and settle before terminal status.", nil)
	}
	run := agentMCPTaskRun{stored: stored, task: task, state: state}
	if err := h.saveAgentMCPTask(r.Context(), &run); err != nil {
		h.writeAgentMCPStoreError(w, request.ID, err)
		return
	}
	writeAgentMCPTask(w, request.ID, task)
}
