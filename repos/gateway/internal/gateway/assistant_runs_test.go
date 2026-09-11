package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/assistantstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type memoryAssistantRunStore struct {
	*memoryAssistantThreadStore
	runs             map[string]assistantstate.RunRecord
	steps            map[string]assistantstate.RunStepRecord
	transitionCalls  int
	lastTransitionTo string
}

func (s *memoryAssistantRunStore) CreateRun(_ context.Context, record assistantstate.RunRecord, quota int) (assistantstate.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.threads[record.OwnerKey+"/"+record.ThreadID]; !found {
		return assistantstate.RunRecord{}, assistantstate.ErrNotFound
	}
	count := 0
	for _, existing := range s.runs {
		if existing.OwnerKey == record.OwnerKey {
			count++
		}
		if existing.OwnerKey == record.OwnerKey && existing.ThreadID == record.ThreadID && assistantstate.ActiveRunStatus(existing.Status) {
			return assistantstate.RunRecord{}, assistantstate.ErrConflict
		}
	}
	if count >= quota {
		return assistantstate.RunRecord{}, assistantstate.ErrQuotaExceeded
	}
	s.clock++
	record.Revision = 1
	record.CreatedAt = time.Unix(s.clock, 0).UTC()
	record.UpdatedAt = record.CreatedAt
	record.Snapshot = append([]byte(nil), record.Snapshot...)
	s.runs[record.OwnerKey+"/"+record.ThreadID+"/"+record.ID] = record
	return record, nil
}

func (s *memoryAssistantRunStore) ListRuns(_ context.Context, owner, threadID string, options assistantstate.RunPageOptions) ([]assistantstate.RunRecord, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.threads[owner+"/"+threadID]; !found {
		return nil, "", assistantstate.ErrNotFound
	}
	var values []assistantstate.RunRecord
	for _, record := range s.runs {
		if record.OwnerKey == owner && record.ThreadID == threadID {
			values = append(values, record)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if options.Order == "asc" {
			return values[i].CreatedAt.Before(values[j].CreatedAt)
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
	if options.After != "" || options.Before != "" {
		return nil, "", assistantstate.ErrNotFound
	}
	next := ""
	if len(values) > options.Limit {
		next = values[options.Limit-1].ID
		values = values[:options.Limit]
	}
	return values, next, nil
}

func (s *memoryAssistantRunStore) GetRun(_ context.Context, owner, threadID, id string) (assistantstate.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.runs[owner+"/"+threadID+"/"+id]
	if !found {
		return assistantstate.RunRecord{}, assistantstate.ErrNotFound
	}
	return record, nil
}

func (s *memoryAssistantRunStore) TransitionRun(_ context.Context, record assistantstate.RunRecord, expectedStatus string, expectedRevision int64) (assistantstate.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transitionCalls++
	s.lastTransitionTo = record.Status
	key := record.OwnerKey + "/" + record.ThreadID + "/" + record.ID
	current, found := s.runs[key]
	if !found {
		return assistantstate.RunRecord{}, assistantstate.ErrNotFound
	}
	if current.Status != expectedStatus || current.Revision != expectedRevision || !assistantstate.ValidRunTransition(current.Status, record.Status) {
		return assistantstate.RunRecord{}, assistantstate.ErrConflict
	}
	record.Revision++
	record.UpdatedAt = current.UpdatedAt.Add(time.Second)
	record.Snapshot = append([]byte(nil), record.Snapshot...)
	s.runs[key] = record
	return record, nil
}

func (*memoryAssistantRunStore) CreateRunStep(context.Context, assistantstate.RunStepRecord, int) (assistantstate.RunStepRecord, error) {
	return assistantstate.RunStepRecord{}, assistantstate.ErrUnavailable
}
func (s *memoryAssistantRunStore) ListRunSteps(_ context.Context, owner, threadID, runID string, options assistantstate.RunPageOptions) ([]assistantstate.RunStepRecord, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.runs[owner+"/"+threadID+"/"+runID]; !found {
		return nil, "", assistantstate.ErrNotFound
	}
	var values []assistantstate.RunStepRecord
	for _, step := range s.steps {
		if step.OwnerKey == owner && step.ThreadID == threadID && step.RunID == runID {
			values = append(values, step)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if options.Order == "asc" {
			return values[i].CreatedAt.Before(values[j].CreatedAt)
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
	return values, "", nil
}
func (s *memoryAssistantRunStore) GetRunStep(_ context.Context, owner, threadID, runID, id string) (assistantstate.RunStepRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step, found := s.steps[owner+"/"+threadID+"/"+runID+"/"+id]
	if !found {
		return assistantstate.RunStepRecord{}, assistantstate.ErrNotFound
	}
	return step, nil
}
func (*memoryAssistantRunStore) UpdateRunStep(context.Context, assistantstate.RunStepRecord, string, int64) (assistantstate.RunStepRecord, error) {
	return assistantstate.RunStepRecord{}, assistantstate.ErrUnavailable
}
func (s *memoryAssistantRunStore) CompleteRun(_ context.Context, run assistantstate.RunRecord, expectedStatus string, expectedRevision int64, step assistantstate.RunStepRecord, message *assistantstate.MessageRecord, messageQuota, stepQuota int) (assistantstate.RunRecord, assistantstate.RunStepRecord, *assistantstate.MessageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := run.OwnerKey + "/" + run.ThreadID + "/" + run.ID
	current, found := s.runs[key]
	if !found {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrNotFound
	}
	if current.Status != expectedStatus || current.Revision != expectedRevision {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrConflict
	}
	stepCount, messageCount := 0, 0
	for _, existing := range s.steps {
		if existing.OwnerKey == run.OwnerKey && existing.ThreadID == run.ThreadID && existing.RunID == run.ID {
			stepCount++
		}
	}
	for _, existing := range s.messages {
		if existing.OwnerKey == run.OwnerKey && existing.ThreadID == run.ThreadID {
			messageCount++
		}
	}
	if stepCount >= stepQuota || message != nil && messageCount >= messageQuota {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrQuotaExceeded
	}
	run.Revision++
	run.UpdatedAt = current.UpdatedAt.Add(time.Second)
	step.Revision = 1
	step.CreatedAt, step.UpdatedAt = run.UpdatedAt, run.UpdatedAt
	s.runs[key] = run
	s.steps[run.OwnerKey+"/"+run.ThreadID+"/"+run.ID+"/"+step.ID] = step
	if message == nil {
		return run, step, nil, nil
	}
	created := *message
	created.Revision = 1
	created.CreatedAt, created.UpdatedAt = run.UpdatedAt, run.UpdatedAt
	s.messages[created.OwnerKey+"/"+created.ThreadID+"/"+created.ID] = created
	return run, step, &created, nil
}

type assistantRunProvider struct {
	request             modules.RequestContext
	created             openai.ResponseResponse
	retrieved           openai.ResponseResponse
	cancelled           openai.ResponseResponse
	settled             bool
	cancelCalls         int
	retrieveCalls       int
	settleCalls         int
	lastRetrievedStatus string
	lastSettledValue    bool
	responseCalls       int
}

func (*assistantRunProvider) ChatCompletions(context.Context, modules.RequestContext) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}
func (*assistantRunProvider) StreamChatCompletions(context.Context, modules.RequestContext, provider.ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	return openai.ChatCompletionResponse{}, false, nil
}
func (p *assistantRunProvider) Responses(_ context.Context, request modules.RequestContext) (openai.ResponseResponse, error) {
	p.responseCalls++
	p.request = request
	return p.created, nil
}
func (*assistantRunProvider) StreamResponses(context.Context, modules.RequestContext, provider.ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	return openai.ResponseResponse{}, false, nil
}
func (*assistantRunProvider) Models() []openai.Model {
	return []openai.Model{{ID: "model-a", Object: "model"}, {ID: "model-b", Object: "model"}}
}
func (*assistantRunProvider) ResolveResponseResource(_ context.Context, _ modules.RequestContext, _ string) (string, error) {
	return "model-a", nil
}
func (p *assistantRunProvider) RetrieveResponse(context.Context, modules.RequestContext, string) (openai.ResponseResponse, error) {
	p.retrieveCalls++
	p.lastRetrievedStatus = p.retrieved.Status
	return p.retrieved, nil
}
func (p *assistantRunProvider) CancelResponse(context.Context, modules.RequestContext, string) (openai.ResponseResponse, error) {
	p.cancelCalls++
	return p.cancelled, nil
}
func (p *assistantRunProvider) BackgroundResponseSettled(context.Context, modules.RequestContext, string) (bool, error) {
	p.settleCalls++
	p.lastSettledValue = p.settled
	return p.settled, nil
}

func newAssistantRunTestHandler(t *testing.T, user string, provider *assistantRunProvider) (*memoryAssistantRunStore, http.Handler) {
	t.Helper()
	base := &memoryAssistantThreadStore{
		memoryAssistantStore: &memoryAssistantStore{records: map[string]assistantstate.Record{}},
		threads:              map[string]assistantstate.ThreadRecord{}, messages: map[string]assistantstate.MessageRecord{},
	}
	store := &memoryAssistantRunStore{memoryAssistantThreadStore: base, runs: map[string]assistantstate.RunRecord{}, steps: map[string]assistantstate.RunStepRecord{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&assistantAuthModule{user: user}}), provider).
		WithAssistantStore(store, AssistantRuntimeConfig{
			OwnerQuota: 10, ThreadOwnerQuota: 10, MessageThreadQuota: 100,
			RunOwnerQuota: 10, RunStepQuota: 10, RunRetention: time.Hour,
		}))
	return store, handler
}

func TestAssistantRunExecutesThreadAndReconcilesAfterSettlement(t *testing.T) {
	provider := &assistantRunProvider{created: openai.ResponseResponse{ID: "resp_run", Model: "model-a", Status: "queued"}}
	store, handler := newAssistantRunTestHandler(t, "user", provider)
	assistant := assistantRequest(t, handler, http.MethodPost, "/v1/assistants", `{"model":"model-a","instructions":"base","tools":[{"type":"function","function":{"name":"lookup","description":"find","parameters":{"type":"object"}}}]}`)
	assistantID := responseString(t, assistant, "id")
	thread := assistantRequest(t, handler, http.MethodPost, "/v1/threads", `{"messages":[{"role":"user","content":"question"}]}`)
	threadID := responseString(t, thread, "id")
	created := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/runs", `{"assistant_id":"`+assistantID+`","additional_instructions":"extra","max_completion_tokens":77}`)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"status":"queued"`) {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	runID := responseString(t, created, "id")
	if !provider.request.ResponseRequest.Background || provider.request.ResponseRequest.MaxOutputTokens == nil || *provider.request.ResponseRequest.MaxOutputTokens != 77 || provider.request.ResponseRequest.Instructions != "base\n\nextra" {
		t.Fatalf("effective request=%+v", provider.request.ResponseRequest)
	}
	input, ok := provider.request.ResponseRequest.Input.([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input=%#v", provider.request.ResponseRequest.Input)
	}
	provider.retrieved = openai.ResponseResponse{ID: "resp_run", Model: "model-a", Status: "completed"}
	if pending := assistantRequest(t, handler, http.MethodGet, "/v1/threads/"+threadID+"/runs/"+runID, ""); pending.Code != http.StatusOK || !strings.Contains(pending.Body.String(), `"status":"queued"`) {
		t.Fatalf("unsettled status=%d body=%s", pending.Code, pending.Body.String())
	}
	provider.settled = true
	completed := assistantRequest(t, handler, http.MethodGet, "/v1/threads/"+threadID+"/runs/"+runID, "")
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"status":"completed"`) || !strings.Contains(completed.Body.String(), `"response_id":"resp_run"`) {
		t.Fatalf("completed status=%d retrieves=%d retrieved=%s settlements=%d settled=%t transitions=%d target=%s body=%s", completed.Code, provider.retrieveCalls, provider.lastRetrievedStatus, provider.settleCalls, provider.lastSettledValue, store.transitionCalls, store.lastTransitionTo, completed.Body.String())
	}
	listed := assistantRequest(t, handler, http.MethodGet, "/v1/threads/"+threadID+"/runs?order=asc&limit=1", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), runID) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
}

func TestAssistantRunActiveConflictCancellationAndOwnerIsolation(t *testing.T) {
	provider := &assistantRunProvider{created: openai.ResponseResponse{ID: "resp_pending", Model: "model-a", Status: "queued"}, cancelled: openai.ResponseResponse{ID: "resp_pending", Model: "model-a", Status: "cancelled"}, settled: true}
	store, handler := newAssistantRunTestHandler(t, "user-a", provider)
	assistantID := responseString(t, assistantRequest(t, handler, http.MethodPost, "/v1/assistants", `{"model":"model-a"}`), "id")
	threadID := responseString(t, assistantRequest(t, handler, http.MethodPost, "/v1/threads", `{"messages":[{"role":"user","content":"question"}]}`), "id")
	created := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/runs", `{"assistant_id":"`+assistantID+`"}`)
	runID := responseString(t, created, "id")
	if conflict := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/runs", `{"assistant_id":"`+assistantID+`"}`); conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	other := Routes(NewHandler(modules.NewPipeline([]modules.Module{&assistantAuthModule{user: "user-b"}}), provider).
		WithAssistantStore(store, AssistantRuntimeConfig{OwnerQuota: 10, ThreadOwnerQuota: 10, MessageThreadQuota: 100, RunOwnerQuota: 10, RunStepQuota: 10, RunRetention: time.Hour}))
	if hidden := assistantRequest(t, other, http.MethodGet, "/v1/threads/"+threadID+"/runs/"+runID, ""); hidden.Code != http.StatusNotFound {
		t.Fatalf("cross-owner status=%d body=%s", hidden.Code, hidden.Body.String())
	}
	cancelled := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/runs/"+runID+"/cancel", `{}`)
	if cancelled.Code != http.StatusOK || !strings.Contains(cancelled.Body.String(), `"status":"cancelled"`) || provider.cancelCalls != 1 {
		t.Fatalf("cancel status=%d calls=%d body=%s", cancelled.Code, provider.cancelCalls, cancelled.Body.String())
	}
}

func TestAssistantRunRequiresAllToolOutputsAndContinuesDurably(t *testing.T) {
	provider := &assistantRunProvider{created: openai.ResponseResponse{
		ID: "resp_tools", Model: "model-a", Status: "completed",
		Output: []openai.ResponseOutputItem{
			{Type: "function_call", CallID: "call_weather", Name: "lookup", Arguments: `{"city":"Paris"}`},
			{Type: "function_call", CallID: "call_time", Name: "lookup", Arguments: `{"zone":"UTC"}`},
		},
	}}
	_, handler := newAssistantRunTestHandler(t, "user", provider)
	assistantID := responseString(t, assistantRequest(t, handler, http.MethodPost, "/v1/assistants", `{"model":"model-a","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`), "id")
	threadID := responseString(t, assistantRequest(t, handler, http.MethodPost, "/v1/threads", `{"messages":[{"role":"user","content":"question"}]}`), "id")
	created := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/runs", `{"assistant_id":"`+assistantID+`"}`)
	runID := responseString(t, created, "id")
	if !strings.Contains(created.Body.String(), `"status":"requires_action"`) || !strings.Contains(created.Body.String(), `"call_weather"`) {
		t.Fatalf("required action body=%s", created.Body.String())
	}
	missing := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/runs/"+runID+"/submit_tool_outputs", `{"tool_outputs":[{"tool_call_id":"call_weather","output":"sunny"}]}`)
	if missing.Code != http.StatusBadRequest || provider.responseCalls != 1 {
		t.Fatalf("missing output status=%d calls=%d body=%s", missing.Code, provider.responseCalls, missing.Body.String())
	}
	provider.created = openai.ResponseResponse{ID: "resp_continue", Model: "model-a", Status: "queued"}
	continued := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/runs/"+runID+"/submit_tool_outputs", `{"tool_outputs":[{"tool_call_id":"call_time","output":"12:00"},{"tool_call_id":"call_weather","output":"sunny"}]}`)
	if continued.Code != http.StatusOK || !strings.Contains(continued.Body.String(), `"status":"queued"`) || !strings.Contains(continued.Body.String(), `"required_action":null`) {
		t.Fatalf("continued status=%d body=%s", continued.Code, continued.Body.String())
	}
	if provider.responseCalls != 2 || provider.request.ResponseRequest.PreviousResponse != "resp_tools" {
		t.Fatalf("continuation calls=%d request=%+v", provider.responseCalls, provider.request.ResponseRequest)
	}
	outputs, ok := provider.request.ResponseRequest.Input.([]any)
	if !ok || len(outputs) != 2 {
		t.Fatalf("outputs=%#v", provider.request.ResponseRequest.Input)
	}
}

func TestAssistantRunRejectsMalformedProviderToolCalls(t *testing.T) {
	response := openai.ResponseResponse{Output: []openai.ResponseOutputItem{{Type: "function_call", CallID: "bad/id", Name: "lookup", Arguments: `{}`}}}
	if action, err := assistantRunAction(response); !errors.Is(err, errAssistantRunProviderPayload) || action != nil {
		t.Fatalf("action=%+v err=%v", action, err)
	}
	response.Output = []openai.ResponseOutputItem{
		{Type: "function_call", CallID: "call_a", Name: "lookup", Arguments: `{}`},
		{Type: "function_call", CallID: "call_a", Name: "lookup", Arguments: `{}`},
	}
	if _, err := assistantRunAction(response); !errors.Is(err, errAssistantRunProviderPayload) {
		t.Fatalf("duplicate call err=%v", err)
	}
}

func TestAssistantRunCompletionAtomicallyCreatesMessageAndStep(t *testing.T) {
	provider := &assistantRunProvider{created: openai.ResponseResponse{ID: "resp_answer", Model: "model-a", Status: "queued"}}
	_, handler := newAssistantRunTestHandler(t, "user", provider)
	assistantID := responseString(t, assistantRequest(t, handler, http.MethodPost, "/v1/assistants", `{"model":"model-a"}`), "id")
	threadID := responseString(t, assistantRequest(t, handler, http.MethodPost, "/v1/threads", `{"messages":[{"role":"user","content":"question"}]}`), "id")
	runID := responseString(t, assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+threadID+"/runs", `{"assistant_id":"`+assistantID+`"}`), "id")
	provider.retrieved = openai.ResponseResponse{ID: "resp_answer", Model: "model-a", Status: "completed", OutputText: "answer"}
	provider.settled = true
	completed := assistantRequest(t, handler, http.MethodGet, "/v1/threads/"+threadID+"/runs/"+runID, "")
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"status":"completed"`) {
		t.Fatalf("complete status=%d body=%s", completed.Code, completed.Body.String())
	}
	messages := assistantRequest(t, handler, http.MethodGet, "/v1/threads/"+threadID+"/messages?order=asc&limit=10", "")
	if messages.Code != http.StatusOK || !strings.Contains(messages.Body.String(), `"value":"answer"`) || !strings.Contains(messages.Body.String(), `"assistant_id":"`+assistantID+`"`) || !strings.Contains(messages.Body.String(), `"run_id":"`+runID+`"`) {
		t.Fatalf("messages status=%d body=%s", messages.Code, messages.Body.String())
	}
	steps := assistantRequest(t, handler, http.MethodGet, "/v1/threads/"+threadID+"/runs/"+runID+"/steps?order=asc&limit=10", "")
	if steps.Code != http.StatusOK || !strings.Contains(steps.Body.String(), `"type":"message_creation"`) {
		t.Fatalf("steps status=%d body=%s", steps.Code, steps.Body.String())
	}
	var page struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(steps.Body.Bytes(), &page) != nil || len(page.Data) != 1 {
		t.Fatalf("step page=%s", steps.Body.String())
	}
	stepID, _ := page.Data[0]["id"].(string)
	step := assistantRequest(t, handler, http.MethodGet, "/v1/threads/"+threadID+"/runs/"+runID+"/steps/"+stepID, "")
	if step.Code != http.StatusOK || !strings.Contains(step.Body.String(), `"object":"thread.run.step"`) {
		t.Fatalf("step status=%d body=%s", step.Code, step.Body.String())
	}
}

func responseString(t *testing.T, response *httptest.ResponseRecorder, key string) string {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("response status=%d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if json.Unmarshal(response.Body.Bytes(), &payload) != nil {
		t.Fatalf("invalid response: %s", response.Body.String())
	}
	value, _ := payload[key].(string)
	if value == "" {
		t.Fatalf("missing %s: %s", key, response.Body.String())
	}
	return value
}

var _ assistantstate.RunStore = (*memoryAssistantRunStore)(nil)
var _ provider.ResponseResourceProvider = (*assistantRunProvider)(nil)
var _ provider.ResponseCancellationProvider = (*assistantRunProvider)(nil)
var _ provider.BackgroundResponseSettlementProvider = (*assistantRunProvider)(nil)
