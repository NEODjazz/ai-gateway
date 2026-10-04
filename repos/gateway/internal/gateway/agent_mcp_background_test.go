package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
)

func agentMCPBackgroundHandler(t *testing.T) (Handler, *agentMCPTestProvider, *fakeMCPRuntimeClient, *a2aPushTestStore) {
	t.Helper()
	h, llm, client := agentMCPApprovalHandler(t)
	store := &a2aPushTestStore{a2aMemoryTaskStore: h.a2aTasks.(*a2aMemoryTaskStore)}
	h = h.WithA2ATaskStore(store, h.a2aTaskConfig)
	var err error
	h, err = h.WithAgentMCPBackground(store, []byte("synthetic-background-key-material"))
	if err != nil {
		t.Fatal(err)
	}
	return h, llm, client, store
}

func readBackgroundAgentTask(t *testing.T, h Handler, id string) a2aTask {
	t.Helper()
	response := agentMCPTaskRPC(h, "GetTask", a2aMessage{}, id)
	var envelope struct {
		Result a2aTask `json:"result"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Result.ID == "" {
		t.Fatal("background task read failed")
	}
	return envelope.Result
}

func backgroundAgentDecision(h Handler, decision a2aMessage) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "decision", "method": "SendMessage", "params": map[string]any{"tenant": "research", "message": decision, "configuration": map[string]bool{"returnImmediately": true}}})
	r := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer test-key")
	r.Header.Set("A2A-Version", "1.0")
	w := httptest.NewRecorder()
	Routes(h).ServeHTTP(w, r)
	return w
}

func TestAgentMCPBackgroundQueueApprovalAndReplay(t *testing.T) {
	h, llm, client, store := agentMCPBackgroundHandler(t)
	llm.toolArguments = `{"city":"Paris","count":9007199254740993}`
	queued := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", `,"configuration":{"returnImmediately":true}`))
	if queued.Status.State != "TASK_STATE_SUBMITTED" || len(llm.requests) != 0 || client.calls != 0 || store.job == nil {
		t.Fatal("queue executed effects or lost its outbox job")
	}
	for _, payload := range [][]byte{store.job.Payload, store.tasks[queued.ID].Payload} {
		if strings.Contains(string(payload), "test-key") || strings.Contains(string(payload), "Bearer") {
			t.Fatal("plaintext execution credential persisted")
		}
	}
	oldExecutionID := store.job.ExecutionID
	if agentMCPResultTask(t, agentMCPSend(h, "SendMessage", `,"configuration":{"returnImmediately":true}`)).Status.State != "TASK_STATE_SUBMITTED" || store.job.ExecutionID != oldExecutionID {
		t.Fatal("queue replay created another job")
	}
	// An independent handler consumes durable state after the original HTTP request ended.
	replica := h
	if count, err := replica.ProcessAgentMCPBackground(t.Context()); err != nil || count != 1 {
		t.Fatalf("process count=%d err=%v", count, err)
	}
	review := readBackgroundAgentTask(t, h, queued.ID)
	if review.Status.State != "TASK_STATE_INPUT_REQUIRED" || client.callCalls != 0 || len(llm.requests) != 1 || store.job != nil {
		t.Fatal("worker did not pause before tools")
	}
	raw := agentMCPTaskRPC(h, "GetTask", a2aMessage{}, queued.ID).Body.String()
	if !strings.Contains(raw, `"count":9007199254740993`) {
		t.Fatal("background review rounded arguments")
	}
	decision := agentMCPDecision(t, review, true, "approve-background")
	next := agentMCPResultTask(t, backgroundAgentDecision(h, decision))
	if next.Status.State != "TASK_STATE_SUBMITTED" || client.callCalls != 0 || store.job == nil || store.job.ExecutionID == oldExecutionID {
		t.Fatal("approval did not queue independent work before effects")
	}
	if _, err := replica.ProcessAgentMCPBackground(t.Context()); err != nil {
		t.Fatal(err)
	}
	completed := readBackgroundAgentTask(t, h, queued.ID)
	if completed.Status.State != "TASK_STATE_COMPLETED" || len(llm.requests) != 2 || client.callCalls != 1 || client.callArgs["count"] != json.Number("9007199254740993") {
		t.Fatal("background continuation lost actual effects or exact arguments")
	}
	if agentMCPResultTask(t, backgroundAgentDecision(h, decision)).Status.State != "TASK_STATE_COMPLETED" || store.job != nil || len(llm.requests) != 2 {
		t.Fatal("decision replay repeated background work")
	}
}

func TestAgentMCPBackgroundCancelBeforeWorkerHasNoEffects(t *testing.T) {
	h, llm, client, store := agentMCPBackgroundHandler(t)
	task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", `,"configuration":{"returnImmediately":true}`))
	canceled := agentMCPResultTask(t, agentMCPTaskRPC(h, "CancelTask", a2aMessage{}, task.ID))
	if canceled.Status.State != "TASK_STATE_CANCELED" {
		t.Fatal("queued cancellation was not terminal")
	}
	if _, err := h.ProcessAgentMCPBackground(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) != 0 || client.calls != 0 || store.job != nil {
		t.Fatal("canceled queued work executed effects")
	}
}

func TestAgentMCPBackgroundStartedClaimNeverRepeatsEffects(t *testing.T) {
	h, llm, client, store := agentMCPBackgroundHandler(t)
	task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", `,"configuration":{"returnImmediately":true}`))
	stored := store.tasks[task.ID]
	state, err := decodeAgentMCPState(stored.Payload)
	if err != nil {
		t.Fatal(err)
	}
	state.WorkerStarted = true
	state.Iterations = 1
	task.Status.State = "TASK_STATE_WORKING"
	state.Deadline = time.Now().Add(time.Hour)
	stored.State = task.Status.State
	stored.Payload, err = encodeAgentMCPTask(task, state)
	if err != nil {
		t.Fatal(err)
	}
	store.tasks[task.ID] = stored
	if _, err := h.ProcessAgentMCPBackground(t.Context()); err == nil || store.retry <= 0 {
		t.Fatal("active started claim was restarted or discarded")
	}
	state.Deadline = time.Now().Add(-time.Minute)
	stored.Payload, err = encodeAgentMCPTask(task, state)
	if err != nil {
		t.Fatal(err)
	}
	store.tasks[task.ID] = stored
	if _, err := h.ProcessAgentMCPBackground(t.Context()); err != nil {
		t.Fatal(err)
	}
	failed := readBackgroundAgentTask(t, h, task.ID)
	if failed.Status.State != "TASK_STATE_FAILED" || len(llm.requests) != 0 || client.calls != 0 || store.job != nil {
		t.Fatal("expired unknown execution was repeated")
	}
}

type backgroundAgentAuth struct {
	err          error
	organization string
	user         string
	calls        int
	denyAt       int
}

func (*backgroundAgentAuth) Name() string   { return "auth" }
func (*backgroundAgentAuth) Required() bool { return true }
func (m *backgroundAgentAuth) Handle(_ context.Context, req *modules.RequestContext) error {
	m.calls++
	if m.err != nil && (m.denyAt == 0 || m.calls >= m.denyAt) {
		return m.err
	}
	req.APIKey = ""
	req.CredentialID = "credential"
	req.UserID = m.user
	req.OrganizationID = m.organization
	return nil
}

func TestAgentMCPBackgroundRevalidatesCredentialsAndOwner(t *testing.T) {
	for _, kind := range []string{"revoked", "unavailable", "user changed", "organization changed", "revoked before model"} {
		t.Run(kind, func(t *testing.T) {
			h, llm, client, store := agentMCPBackgroundHandler(t)
			auth := &backgroundAgentAuth{user: "user", organization: "org-a"}
			h.pipeline = modules.NewPipeline([]modules.Module{auth})
			task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", `,"configuration":{"returnImmediately":true}`))
			switch kind {
			case "revoked":
				auth.err = modules.ErrUnauthorized
			case "unavailable":
				auth.err = errors.New("auth unavailable")
			case "user changed":
				auth.user = "other"
			case "organization changed":
				auth.organization = "org-b"
			case "revoked before model":
				auth.err = modules.ErrUnauthorized
				auth.denyAt = auth.calls + 3
			}
			_, err := h.ProcessAgentMCPBackground(t.Context())
			if kind == "unavailable" {
				if err == nil || store.retry <= 0 || store.tasks[task.ID].State != "TASK_STATE_SUBMITTED" {
					t.Fatal("authorization outage lost queued work")
				}
			} else {
				if err != nil || store.tasks[task.ID].State != "TASK_STATE_FAILED" || store.job != nil {
					t.Fatal("unauthorized background work did not fail closed")
				}
			}
			if len(llm.requests) != 0 || client.callCalls != 0 {
				t.Fatal("unauthorized background model/tool effects ran")
			}
		})
	}
}

func TestAgentMCPBackgroundEncryptionAndPrerequisites(t *testing.T) {
	h, _, _, store := agentMCPBackgroundHandler(t)
	if _, err := h.WithAgentMCPBackground(nil, []byte("synthetic-background-key-material")); err == nil {
		t.Fatal("missing jobs accepted")
	}
	if _, err := h.WithAgentMCPBackground(store, []byte("short")); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := h.WithA2ATaskStore(store.a2aMemoryTaskStore, h.a2aTaskConfig).WithAgentMCPBackground(store, []byte("synthetic-background-key-material")); err == nil {
		t.Fatal("non-atomic outbox accepted")
	}
	_ = agentMCPResultTask(t, agentMCPSend(h, "SendMessage", `,"configuration":{"returnImmediately":true}`))
	job := *store.job
	for _, tamper := range []string{"owner", "endpoint", "execution", "resource", "ciphertext", "nonce", "key"} {
		t.Run(tamper, func(t *testing.T) {
			copy := job
			checker := h
			switch tamper {
			case "owner":
				copy.OwnerKey = "other"
			case "endpoint":
				copy.EndpointID = "other"
			case "execution":
				copy.ExecutionID = "other"
			case "resource":
				copy.ResourceID = "other"
			case "ciphertext":
				copy.Payload = []byte(`{"Nonce":"AAAA","Ciphertext":"AAAA"}`)
			case "nonce":
				copy.Payload = []byte(`{"Nonce":"","Ciphertext":"AAAA"}`)
			case "key":
				var err error
				checker, err = h.WithAgentMCPBackground(store, []byte("different-synthetic-key-material"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := checker.decodeAgentMCPJob(copy); err == nil {
				t.Fatal("tampered execution credential accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	RunAgentMCPBackgroundWorker(ctx, h)
}
