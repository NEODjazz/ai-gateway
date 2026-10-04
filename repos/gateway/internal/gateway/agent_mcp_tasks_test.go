package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/modules"
)

func agentMCPApprovalHandler(t *testing.T) (Handler, *agentMCPTestProvider, *fakeMCPRuntimeClient) {
	t.Helper()
	llm := &agentMCPTestProvider{callCount: 1}
	policy := ToolPolicy{ID: "safe", Name: "Safe", AllowedTools: []string{"forecast"}, ApprovalRequired: []string{"forecast"}, MaxToolCalls: 3, Enabled: true}
	h, client, _ := agentMCPTestHandler(t, llm, policy, nil, 3, 4)
	return h, llm, client
}

func agentMCPResultTask(t *testing.T, response *httptest.ResponseRecorder) a2aTask {
	t.Helper()
	var payload struct {
		Result struct {
			Task a2aTask `json:"task"`
		} `json:"result"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Result.Task.ID == "" {
		t.Fatalf("missing task: status=%d body=%s", response.Code, response.Body.String())
	}
	return payload.Result.Task
}

func agentMCPTaskRPC(h Handler, method string, message a2aMessage, id string) *httptest.ResponseRecorder {
	params := map[string]any{"tenant": "research"}
	if method == "SendMessage" || method == "SendStreamingMessage" {
		params["message"] = message
	} else {
		params["id"] = id
	}
	encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "rpc", "method": method, "params": params})
	r := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(string(encoded)))
	r.Header.Set("A2A-Version", "1.0")
	r.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	Routes(h).ServeHTTP(w, r)
	return w
}

func agentMCPDecision(t *testing.T, task a2aTask, approved bool, messageID string) a2aMessage {
	t.Helper()
	if task.Status.Message == nil {
		t.Fatal("approval has no status message")
	}
	metadata := task.Status.Message.Metadata[agentApprovalMetadata].(map[string]any)
	calls := metadata["calls"].([]any)
	choices := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		choices = append(choices, map[string]any{"call_id": call.(map[string]any)["call_id"], "approved": approved})
	}
	text := "Review decision"
	return a2aMessage{MessageID: messageID, TaskID: task.ID, ContextID: task.ContextID, Role: "ROLE_USER", Parts: []a2aPart{{Text: &text}}, Metadata: map[string]any{agentApprovalMetadata: map[string]any{"approval_id": metadata["approval_id"], "choices": choices}}}
}

func TestAgentMCPDurableApprovalResumeAndReplay(t *testing.T) {
	for _, approved := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "decline"}[approved], func(t *testing.T) {
			h, llm, client := agentMCPApprovalHandler(t)
			profile, _ := h.agents.AgentProfile("research")
			profile.Instructions = "private saved instructions"
			_, _ = h.agents.PutAgentProfile(profile.ID, profile)
			first := agentMCPSend(h, "SendMessage", "")
			task := agentMCPResultTask(t, first)
			if task.Status.State != "TASK_STATE_INPUT_REQUIRED" || client.callCalls != 0 || len(llm.requests) != 1 {
				t.Fatal("approval executed tools or failed to pause")
			}
			for _, private := range []string{"private saved instructions", "mcp.example", "agentMcp", "runId", "test-key"} {
				if strings.Contains(first.Body.String(), private) {
					t.Fatalf("public task leaked %s", private)
				}
			}
			store := h.a2aTasks.(*a2aMemoryTaskStore)
			for _, stored := range store.tasks {
				if strings.Contains(string(stored.Payload), "private saved instructions") {
					t.Fatal("instructions persisted outside encrypted configuration")
				}
			}
			// A new handler has no local task execution state; it resumes from the shared store.
			replica := h
			decision := agentMCPDecision(t, task, approved, "decision-one")
			completed := agentMCPResultTask(t, agentMCPTaskRPC(replica, "SendMessage", decision, ""))
			if completed.Status.State != "TASK_STATE_COMPLETED" || len(llm.requests) != 2 || client.callCalls != map[bool]int{true: 1, false: 0}[approved] {
				t.Fatal("durable decision did not finish exactly once")
			}
			input, _ := json.Marshal(llm.requests[1].ResponseRequest.Input)
			if !strings.Contains(string(input), "function_call_output") || !approved && !strings.Contains(string(input), "declined") {
				t.Fatal("missing real typed tool output")
			}
			if agentMCPTaskRPC(replica, "SendMessage", decision, "").Code != http.StatusOK || len(llm.requests) != 2 {
				t.Fatal("identical decision retried effects")
			}
			decision.Metadata[agentApprovalMetadata].(map[string]any)["choices"] = []any{map[string]any{"call_id": "call_1_0", "approved": !approved}}
			if agentMCPTaskRPC(replica, "SendMessage", decision, "").Code != http.StatusConflict || len(llm.requests) != 2 {
				t.Fatal("conflicting decision reused message ID")
			}
			// Original initial retry also returns the known task without new billing.
			if agentMCPSend(h, "SendMessage", "").Code != http.StatusOK || len(llm.requests) != 2 {
				t.Fatal("initial retry reran inference")
			}
			text := "Next question"
			followup := a2aMessage{MessageID: "followup", TaskID: task.ID, ContextID: task.ContextID, Role: "ROLE_USER", Parts: []a2aPart{{Text: &text}}}
			continued := agentMCPResultTask(t, agentMCPTaskRPC(replica, "SendMessage", followup, ""))
			if continued.ID != task.ID || len(llm.requests) != 3 {
				t.Fatal("completed task lost continuity")
			}
			history, _ := json.Marshal(llm.requests[2].ResponseRequest.Input)
			if !strings.Contains(string(history), "function_call_output") || !strings.Contains(string(history), "The forecast is sunny") || !strings.Contains(string(history), text) {
				t.Fatal("continuation discarded native history")
			}
		})
	}
}

func TestAgentMCPMixedDecisionsRetainEveryTypedResult(t *testing.T) {
	h, llm, client := agentMCPApprovalHandler(t)
	llm.callCount = 2
	task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", ""))
	decision := agentMCPDecision(t, task, true, "mixed")
	choices := decision.Metadata[agentApprovalMetadata].(map[string]any)["choices"].([]map[string]any)
	choices[1]["approved"] = false
	completed := agentMCPResultTask(t, agentMCPTaskRPC(h, "SendMessage", decision, ""))
	if completed.Status.State != "TASK_STATE_COMPLETED" || client.callCalls != 1 || len(llm.requests) != 2 {
		t.Fatal("mixed decisions executed declined tool")
	}
	input, _ := json.Marshal(llm.requests[1].ResponseRequest.Input)
	if strings.Count(string(input), "function_call_output") != 2 || !strings.Contains(string(input), "declined") || !strings.Contains(string(input), "sunny") {
		t.Fatal("mixed step lost actual or declined output")
	}
}

func TestAgentMCPApprovalValidatesTaskAndCurrentAuthorization(t *testing.T) {
	for _, kind := range []string{"challenge", "missing decision", "unknown call", "context", "policy", "server", "schema", "configuration", "credential"} {
		t.Run(kind, func(t *testing.T) {
			h, llm, client := agentMCPApprovalHandler(t)
			task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", ""))
			decision := agentMCPDecision(t, task, true, "decision")
			switch kind {
			case "challenge":
				decision.Metadata[agentApprovalMetadata].(map[string]any)["approval_id"] = "old-challenge"
			case "missing decision":
				decision.Metadata = nil
			case "unknown call":
				decision.Metadata[agentApprovalMetadata].(map[string]any)["choices"] = []any{map[string]any{"call_id": "foreign", "approved": true}}
			case "context":
				decision.ContextID = "foreign"
			case "policy":
				_, _ = h.agents.PutToolPolicy("safe", ToolPolicy{Name: "Disabled", AllowedTools: []string{"forecast"}, MaxToolCalls: 3, Enabled: false})
			case "server":
				server, _ := h.mcp.Server("weather")
				server.ServerURL = "https://new.example.test"
				_, _ = h.mcp.PutServer("weather", server)
			case "schema":
				client.page.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{"other":{"type":"string"}}}`)
			case "configuration":
				profile, _ := h.agents.AgentProfile("research")
				profile.Instructions = "changed"
				_, _ = h.agents.PutAgentProfile("research", profile)
			case "credential":
				h.pipeline = modules.NewPipeline([]modules.Module{mcpRuntimeAuth{tools: []string{"unrelated"}}})
			}
			response := agentMCPTaskRPC(h, "SendMessage", decision, "")
			if response.Code == http.StatusOK || client.callCalls != 0 || len(llm.requests) != 1 {
				t.Fatalf("unsafe resume: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAgentMCPApprovalCASPrecedesToolAndModelEffects(t *testing.T) {
	h, llm, client := agentMCPApprovalHandler(t)
	task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", ""))
	entered, release := make(chan struct{}), make(chan struct{})
	llm.afterResponse = func() { close(entered); <-release }
	decision := agentMCPDecision(t, task, true, "decision")
	var response *httptest.ResponseRecorder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); response = agentMCPTaskRPC(h, "SendMessage", decision, "") }()
	<-entered
	// At this point the owner has already executed its single tool and is in final inference.
	duplicate := agentMCPTaskRPC(h, "SendMessage", decision, "")
	other := agentMCPDecision(t, task, true, "different-decision")
	conflict := agentMCPTaskRPC(h, "SendMessage", other, "")
	close(release)
	wg.Wait()
	if response.Code != http.StatusOK || duplicate.Code != http.StatusOK || conflict.Code != http.StatusConflict || client.callCalls != 1 || len(llm.requests) != 2 {
		t.Fatal("parallel approval ran duplicate effects")
	}
}

func TestAgentMCPInitialClaimPrecedesInferenceAndReplaysAtQuota(t *testing.T) {
	llm := &agentMCPTestProvider{callCount: 1}
	h, client, _ := agentMCPTestHandler(t, llm, ToolPolicy{}, nil, 3, 4)
	h.a2aTaskConfig.OwnerQuota = 1
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	llm.afterResponse = func() { once.Do(func() { close(entered); <-release }) }
	var response *httptest.ResponseRecorder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); response = agentMCPSend(h, "SendMessage", "") }()
	<-entered
	duplicate := agentMCPSend(h, "SendMessage", "")
	close(release)
	wg.Wait()
	if response.Code != http.StatusOK || duplicate.Code != http.StatusOK || client.callCalls != 1 || len(llm.requests) != 2 {
		t.Fatal("parallel initial message reran inference")
	}
	if agentMCPSendID(h, "SendMessage", "", "new-message").Code != http.StatusTooManyRequests {
		t.Fatal("task quota did not prevent new work")
	}
	if agentMCPSend(h, "SendMessage", "").Code != http.StatusOK || len(llm.requests) != 2 {
		t.Fatal("quota blocked safe replay or reran work")
	}
}

func TestAgentMCPCancellationAndExpiredWorkNeverRetryTools(t *testing.T) {
	h, llm, client := agentMCPApprovalHandler(t)
	task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", ""))
	canceled := agentMCPResultTask(t, agentMCPTaskRPC(h, "CancelTask", a2aMessage{}, task.ID))
	if canceled.Status.State != "TASK_STATE_CANCELED" || client.callCalls != 0 {
		t.Fatal("pending cancellation executed tools")
	}
	if agentMCPTaskRPC(h, "SendMessage", agentMCPDecision(t, task, true, "decision"), "").Code != http.StatusConflict || len(llm.requests) != 1 {
		t.Fatal("canceled approval resumed")
	}
	store := h.a2aTasks.(*a2aMemoryTaskStore)
	for id, stored := range store.tasks {
		state, _ := decodeAgentMCPState(stored.Payload)
		state.Deadline = time.Now().Add(-time.Hour)
		state.CancelRequested = false
		state.Challenge = ""
		task.Status = a2aTaskStatus{State: "TASK_STATE_WORKING"}
		stored.State = task.Status.State
		stored.Payload, _ = encodeAgentMCPTask(task, state)
		store.tasks[id] = stored
	}
	response := agentMCPTaskRPC(h, "GetTask", a2aMessage{}, task.ID)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "TASK_STATE_FAILED") || strings.Contains(response.Body.String(), "agentMcp") || client.callCalls != 0 || len(llm.requests) != 1 {
		t.Fatal("expired execution retried unknown work")
	}
}

func TestAgentMCPCancelRunningWaitsForActualExecution(t *testing.T) {
	llm := &agentMCPTestProvider{callCount: 1}
	h, client, _ := agentMCPTestHandler(t, llm, ToolPolicy{}, nil, 3, 4)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	llm.afterResponse = func() { once.Do(func() { close(entered); <-release }) }
	var response *httptest.ResponseRecorder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); response = agentMCPSend(h, "SendMessage", "") }()
	<-entered
	task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", ""))
	cancelled := agentMCPResultTask(t, agentMCPTaskRPC(h, "CancelTask", a2aMessage{}, task.ID))
	if cancelled.Status.State != "TASK_STATE_WORKING" {
		close(release)
		wg.Wait()
		t.Fatal("cancel claimed terminal status before execution stopped")
	}
	close(release)
	wg.Wait()
	if response.Code == http.StatusOK || client.callCalls != 0 || len(llm.requests) != 1 {
		t.Fatal("cancelled model result executed tools")
	}
	read := agentMCPTaskRPC(h, "GetTask", a2aMessage{}, task.ID)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), "TASK_STATE_CANCELED") {
		t.Fatal("settled cancellation was not persisted")
	}
}

func TestAgentMCPBoundsFailureStateAndStoredExecutionKinds(t *testing.T) {
	h, _, _ := agentMCPApprovalHandler(t)
	task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", ""))
	store := h.a2aTasks.(*a2aMemoryTaskStore)
	for _, stored := range store.tasks {
		state, _ := decodeAgentMCPState(stored.Payload)
		working := task
		working.Status = a2aTaskStatus{State: "TASK_STATE_WORKING"}
		state.Challenge = ""
		run := agentMCPTaskRun{stored: stored, task: working, state: state}
		// Establish a durable working claim, then simulate an oversized result.
		if err := h.saveAgentMCPTask(t.Context(), &run); err != nil {
			t.Fatal(err)
		}
		run.state.Input = append(run.state.Input, strings.Repeat("x", a2astate.MaxPayloadBytes))
		if err := h.failAgentMCPTask(t.Context(), &run, false); err != nil {
			t.Fatal("oversized execution could not persist bounded failure")
		}
		if run.task.Status.State != "TASK_STATE_FAILED" || len(run.stored.Payload) > a2astate.MaxPayloadBytes {
			t.Fatal("invalid failure state")
		}
		var wrapper a2aStoredTask
		_ = json.Unmarshal(run.stored.Payload, &wrapper)
		wrapper.Streaming = true
		corrupt, _ := json.Marshal(wrapper)
		if _, _, err := decodeA2AStoredTask(corrupt); err == nil {
			t.Fatal("mixed execution kinds accepted")
		}
		wrapper.Streaming = false
		wrapper.AgentMCP.Version = 2
		corrupt, _ = json.Marshal(wrapper)
		if _, _, err := decodeA2AStoredTask(corrupt); err == nil {
			t.Fatal("unknown loop state version accepted")
		}
	}
}

func TestAgentMCPApprovalStorageFailurePreventsEffects(t *testing.T) {
	h, llm, client := agentMCPApprovalHandler(t)
	task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", ""))
	h.a2aTasks.(*a2aMemoryTaskStore).updateErr = a2astate.ErrUnavailable
	if agentMCPTaskRPC(h, "SendMessage", agentMCPDecision(t, task, true, "decision"), "").Code != http.StatusServiceUnavailable || client.callCalls != 0 || len(llm.requests) != 1 {
		t.Fatal("storage failure executed pending tools")
	}
}
