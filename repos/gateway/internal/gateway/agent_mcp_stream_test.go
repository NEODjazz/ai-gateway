package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/modules"
)

func TestAgentMCPStreamingHTTPFlushAndDisconnect(t *testing.T) {
	llm := &agentMCPTestProvider{callCount: 1}
	h, client, _ := agentMCPTestHandler(t, llm, ToolPolicy{}, nil, 2, 3)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	llm.afterResponse = func() { close(entered); <-release }
	cancelled := make(chan (<-chan struct{}), 1)
	llm.observeContext = func(ctx context.Context) { cancelled <- ctx.Done() }
	deadline, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	await := func(done <-chan struct{}, stage string) {
		t.Helper()
		select {
		case <-done:
		case <-deadline.Done():
			t.Fatalf("streaming HTTP test stalled while waiting for %s", stage)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("no loopback test port available")
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		Routes(h).ServeHTTP(w, r)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(func() { unblock(); server.Close() })
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"rpc","method":"SendStreamingMessage","params":{"tenant":"research","message":{"messageId":"http-stream","role":"ROLE_USER","parts":[{"text":"Check weather"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("A2A-Version", "1.0")
	r.Header.Set("Authorization", "Bearer test-key")
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	response, err := httpClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, "TASK_STATE_WORKING") || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("first task event was buffered until execution completed")
	}
	await(entered, "model generation")
	requestDone := <-cancelled
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	// The execution context derives from the HTTP context. Waiting for the
	// parent alone can release the provider before cancellation reaches the child.
	await(requestDone, "execution cancellation")
	unblock()
	await(finished, "handler completion")
	if len(llm.requests) != 1 || client.callCalls != 0 {
		t.Fatal("disconnected task continued tools or model iterations")
	}
	for _, stored := range h.a2aTasks.(*a2aMemoryTaskStore).tasks {
		if stored.State != "TASK_STATE_CANCELED" {
			t.Fatal("HTTP disconnect did not settle the durable task")
		}
	}
}

func agentMCPStreamTask(t *testing.T, response *httptest.ResponseRecorder) a2aTask {
	t.Helper()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("missing stream: status=%d body=%s", response.Code, response.Body.String())
	}
	var task a2aTask
	for _, line := range strings.Split(response.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			JSONRPC string       `json:"jsonrpc"`
			ID      string       `json:"id"`
			Error   *a2aRPCError `json:"error"`
			Result  struct {
				Task     *a2aTask `json:"task"`
				Artifact *struct {
					TaskID, ContextID string
					Artifact          a2aArtifact
				} `json:"artifactUpdate"`
				Status *struct {
					TaskID, ContextID string
					Status            a2aTaskStatus
				} `json:"statusUpdate"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil || event.JSONRPC != "2.0" || event.ID != "rpc" || event.Error != nil {
			t.Fatalf("invalid stream event: %s", line)
		}
		if event.Result.Task != nil {
			task = *event.Result.Task
		}
		if update := event.Result.Artifact; update != nil {
			if update.TaskID != task.ID || update.ContextID != task.ContextID {
				t.Fatal("artifact crossed task scope")
			}
			task.Artifacts = []a2aArtifact{update.Artifact}
		}
		if update := event.Result.Status; update != nil {
			if update.TaskID != task.ID || update.ContextID != task.ContextID {
				t.Fatal("status crossed task scope")
			}
			task.Status = update.Status
		}
	}
	if task.ID == "" {
		t.Fatal("stream did not identify its durable task")
	}
	return task
}

type agentMCPFlushObserver struct {
	*httptest.ResponseRecorder
	first    func()
	writeErr error
}

func (w *agentMCPFlushObserver) Flush() {
	if w.first != nil {
		first := w.first
		w.first = nil
		first()
	}
	w.ResponseRecorder.Flush()
}
func (w *agentMCPFlushObserver) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseRecorder.Write(p)
}

func TestAgentMCPStreamingDurableClaimAndBilling(t *testing.T) {
	llm := &agentMCPTestProvider{callCount: 1}
	h, client, billing := agentMCPTestHandler(t, llm, ToolPolicy{}, nil, 2, 3)
	w := &agentMCPFlushObserver{ResponseRecorder: httptest.NewRecorder()}
	flushed := false
	w.first = func() {
		flushed = true
		if len(llm.requests) != 0 || client.calls != 0 {
			t.Fatal("stream opened after execution effects")
		}
		task := agentMCPStreamTask(t, w.ResponseRecorder)
		stored, err := h.a2aTasks.GetA2ATask(t.Context(), fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"}), "research", task.ID)
		if err != nil || stored.State != "TASK_STATE_WORKING" {
			t.Fatal("first event preceded durable claim")
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"rpc","method":"SendStreamingMessage","params":{"tenant":"research","message":{"messageId":"initial","role":"ROLE_USER","parts":[{"text":"Check weather"}]}}}`))
	r.Header.Set("A2A-Version", "1.0")
	r.Header.Set("Authorization", "Bearer test-key")
	Routes(h).ServeHTTP(w, r)
	task := agentMCPStreamTask(t, w.ResponseRecorder)
	if !flushed || task.Status.State != "TASK_STATE_COMPLETED" || client.callCalls != 1 || len(llm.requests) != 2 || len(task.Artifacts) != 1 {
		t.Fatal("stream did not complete the bounded model/tool loop")
	}
	if strings.Join(billing.apiTypes, ",") != "mcp_tools_list,mcp_tools_list,a2a,mcp_tools_call,mcp_tools_call,a2a" {
		t.Fatal("stream changed per-step billing")
	}
	if strings.Contains(w.Body.String(), "agentMcp") || strings.Contains(w.Body.String(), "mcp.example") {
		t.Fatal("private state leaked into stream")
	}
	profile, _ := h.agents.AgentProfile("research")
	if h.a2aAgentCard(r, profile)["capabilities"].(map[string]any)["streaming"] != true {
		t.Fatal("working stream was not advertised")
	}
}

func TestAgentMCPStreamingApprovalContinuationAndReplay(t *testing.T) {
	for _, approved := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "decline"}[approved], func(t *testing.T) {
			h, llm, client := agentMCPApprovalHandler(t)
			first := agentMCPStreamTask(t, agentMCPSend(h, "SendStreamingMessage", ""))
			if first.Status.State != "TASK_STATE_INPUT_REQUIRED" || client.callCalls != 0 {
				t.Fatal("stream did not pause before approval")
			}
			decision := agentMCPDecision(t, first, approved, "stream-decision")
			completed := agentMCPStreamTask(t, agentMCPTaskRPC(h, "SendStreamingMessage", decision, ""))
			if completed.ID != first.ID || completed.Status.State != "TASK_STATE_COMPLETED" || len(llm.requests) != 2 {
				t.Fatal("stream lost approval/task continuity")
			}
			wantCalls := 0
			if approved {
				wantCalls = 1
			}
			if client.callCalls != wantCalls {
				t.Fatal("stream ignored the approval decision")
			}
			if agentMCPStreamTask(t, agentMCPTaskRPC(h, "SendStreamingMessage", decision, "")).Status.State != "TASK_STATE_COMPLETED" || len(llm.requests) != 2 || client.callCalls != wantCalls {
				t.Fatal("stream replay repeated effects")
			}
			if agentMCPStreamTask(t, agentMCPSend(h, "SendStreamingMessage", "")).ID != first.ID || len(llm.requests) != 2 {
				t.Fatal("initial stream replay reran inference")
			}
		})
	}
}

func TestAgentMCPStreamingFailureAndDisconnection(t *testing.T) {
	for _, mode := range []string{"tool failure", "cancel after flush", "failed initial write", "failed persistence"} {
		t.Run(mode, func(t *testing.T) {
			llm := &agentMCPTestProvider{callCount: 1}
			h, client, _ := agentMCPTestHandler(t, llm, ToolPolicy{}, nil, 2, 3)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			w := &agentMCPFlushObserver{ResponseRecorder: httptest.NewRecorder()}
			switch mode {
			case "tool failure":
				client.callErr = errors.New("private transport failure")
			case "cancel after flush":
				w.first = cancel
			case "failed initial write":
				w.writeErr = io.ErrClosedPipe
			case "failed persistence":
				llm.afterResponse = func() { h.a2aTasks.(*a2aMemoryTaskStore).updateErr = a2astate.ErrUnavailable }
			}
			r := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"rpc","method":"SendStreamingMessage","params":{"tenant":"research","message":{"messageId":"initial","role":"ROLE_USER","parts":[{"text":"Check weather"}]}}}`)).WithContext(ctx)
			r.Header.Set("A2A-Version", "1.0")
			r.Header.Set("Authorization", "Bearer test-key")
			Routes(h).ServeHTTP(w, r)
			if mode == "failed persistence" {
				if !strings.Contains(w.Body.String(), `"error"`) || strings.Contains(w.Body.String(), "TASK_STATE_COMPLETED") || client.callCalls != 0 {
					t.Fatal("failed settlement claimed success")
				}
				return
			}
			store := h.a2aTasks.(*a2aMemoryTaskStore)
			if len(store.tasks) != 1 {
				t.Fatal("stream lost its durable task")
			}
			for _, stored := range store.tasks {
				want := "TASK_STATE_FAILED"
				if mode != "tool failure" {
					want = "TASK_STATE_CANCELED"
				}
				if stored.State != want {
					t.Fatalf("stored %s, want %s", stored.State, want)
				}
				if mode != "failed initial write" && agentMCPStreamTask(t, w.ResponseRecorder).Status.State != want {
					t.Fatal("stream disagrees with durable state")
				}
			}
			if mode != "tool failure" && (len(llm.requests) != 0 || client.calls != 0) {
				t.Fatal("disconnected stream started effects")
			}
			if strings.Contains(w.Body.String(), "private transport failure") {
				t.Fatal("private failure exposed")
			}
		})
	}
}

func TestAgentMCPStreamingExpiredReplayReconcilesWithoutEffects(t *testing.T) {
	h, llm, client := agentMCPApprovalHandler(t)
	first := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", ""))
	store := h.a2aTasks.(*a2aMemoryTaskStore)
	for key, stored := range store.tasks {
		task, _ := decodeA2ATask(stored.Payload)
		state, _ := decodeAgentMCPState(stored.Payload)
		task.Status = a2aTaskStatus{State: "TASK_STATE_WORKING"}
		state.Challenge = ""
		state.Deadline = time.Now().Add(-time.Hour)
		stored.State = task.Status.State
		stored.Payload, _ = encodeAgentMCPTask(task, state)
		store.tasks[key] = stored
	}
	replayed := agentMCPStreamTask(t, agentMCPSend(h, "SendStreamingMessage", ""))
	if replayed.ID != first.ID || replayed.Status.State != "TASK_STATE_FAILED" || len(llm.requests) != 1 || client.callCalls != 0 {
		t.Fatal("expired stream replay executed unknown effects")
	}
}

func TestAgentMCPStreamingFailsClosedForRuntimeAndToolGrants(t *testing.T) {
	for _, mode := range []string{"task store", "runtime", "audit", "tool grants"} {
		t.Run(mode, func(t *testing.T) {
			llm := &agentMCPTestProvider{callCount: 1}
			grants := []string(nil)
			if mode == "tool grants" {
				grants = []string{"unrelated"}
			}
			h, client, _ := agentMCPTestHandler(t, llm, ToolPolicy{}, grants, 2, 3)
			switch mode {
			case "task store":
				h.a2aTasks = nil
			case "runtime":
				h.mcpRuntime = nil
			case "audit":
				h.audit = nil
			}
			response := agentMCPSend(h, "SendStreamingMessage", "")
			if len(llm.requests) != 0 || client.calls != 0 {
				t.Fatal("unavailable or denied stream started effects")
			}
			if mode == "tool grants" {
				if agentMCPStreamTask(t, response).Status.State != "TASK_STATE_FAILED" {
					t.Fatal("denied tool claimed success")
				}
			} else {
				if response.Code < 400 || strings.Contains(response.Header().Get("Content-Type"), "event-stream") {
					t.Fatal("unavailable runtime opened a stream")
				}
				profile, _ := h.agents.AgentProfile("research")
				if h.a2aAgentCard(httptest.NewRequest("GET", "/", nil), profile)["capabilities"].(map[string]any)["streaming"] != false {
					t.Fatal("unavailable runtime advertised streaming")
				}
			}
		})
	}
}
