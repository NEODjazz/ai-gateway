package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type a2aPushTestStore struct {
	*a2aMemoryTaskStore
	job       *asyncstate.Job
	retry     time.Duration
	completed bool
}

func (s *a2aPushTestStore) CreateA2ATaskWithJob(ctx context.Context, task a2astate.Task, quota int, ttl time.Duration, job asyncstate.Job) (a2astate.Task, error) {
	created, err := s.CreateA2ATask(ctx, task, quota, ttl)
	if err == nil {
		copy := job
		s.job = &copy
	}
	return created, err
}

func (s *a2aPushTestStore) UpdateA2ATaskWithJob(ctx context.Context, task a2astate.Task, expected time.Time, ttl time.Duration, job asyncstate.Job) (a2astate.Task, error) {
	updated, err := s.UpdateA2ATask(ctx, task, expected, ttl)
	if err == nil {
		copy := job
		s.job = &copy
	}
	return updated, err
}

func (s *a2aPushTestStore) EnqueueAsyncJob(context.Context, asyncstate.Job) (bool, error) {
	return false, errors.New("unexpected direct enqueue")
}
func (s *a2aPushTestStore) HasAsyncJob(context.Context, string, string, string) (bool, error) {
	return s.job != nil, nil
}
func (s *a2aPushTestStore) ClaimAsyncJobs(_ context.Context, kind string, _ int, _ time.Duration) ([]asyncstate.Job, error) {
	if s.job == nil || s.job.Kind != kind {
		return nil, nil
	}
	s.job.Attempts++
	s.job.LeaseGeneration++
	return []asyncstate.Job{*s.job}, nil
}
func (s *a2aPushTestStore) RetryAsyncJob(_ context.Context, _, _ string, generation int64, delay time.Duration) error {
	if s.job == nil || s.job.LeaseGeneration != generation {
		return asyncstate.ErrLeaseLost
	}
	s.retry = delay
	return nil
}
func (s *a2aPushTestStore) CompleteAsyncJob(_ context.Context, _, _ string, generation int64) error {
	if s.job == nil || s.job.LeaseGeneration != generation {
		return asyncstate.ErrLeaseLost
	}
	s.completed, s.job = true, nil
	return nil
}

func newA2APushTestHandler(t *testing.T, store *a2aPushTestStore) (Handler, *a2aTestProvider, *lifecycleBillingModule) {
	t.Helper()
	registry := NewAgentRegistry()
	if _, err := registry.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"weather"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutAgentProfile("research", AgentProfile{Name: "Research", Description: "Answers", Model: "test-model", ToolPolicyID: "safe", MaxIterations: 3, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	provider := &a2aTestProvider{}
	billing := &lifecycleBillingModule{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"test-model"}}, billing}), provider).
		WithAgentRegistry(registry).
		WithA2ATaskStore(store, A2ATaskRuntimeConfig{OwnerQuota: 10, TTL: time.Hour})
	configured, err := handler.WithA2APushNotifications(store, []byte("stable-test-push-key"))
	if err != nil {
		t.Fatal(err)
	}
	return configured, provider, billing
}

func TestA2APushConfigurationIsEncryptedAndDelivered(t *testing.T) {
	store := &a2aPushTestStore{a2aMemoryTaskStore: &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}}
	handler, _, billing := newA2APushTestHandler(t, store)
	var delivered string
	handler.a2aHTTPClient = a2aHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		delivered = string(body)
		if request.URL.String() != "https://client.example/hook" || request.Header.Get("X-A2A-Notification-Token") != "notification-secret" || request.Header.Get("Authorization") != "Bearer auth-secret" || request.Header.Get("Content-Type") != "application/a2a+json" {
			t.Fatalf("incorrect push request: url=%s headers=%v", request.URL, request.Header)
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	body := `{"jsonrpc":"2.0","id":"push-rpc","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client","role":"ROLE_USER","parts":[{"text":"hello"}]},"configuration":{"pushNotificationConfig":{"url":"https://client.example/hook","token":"notification-secret","authentication":{"schemes":["Bearer"],"credentials":"auth-secret"}}}}}`
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || billing.calls != 1 || store.job == nil {
		t.Fatalf("status=%d billing=%d job=%+v body=%s", response.Code, billing.calls, store.job, response.Body.String())
	}
	secret, decodeErr := handler.decodeA2APushJob(*store.job)
	if decodeErr != nil || store.job.ExecutionID == "" || store.job.ExecutionID == secret.RequestID {
		t.Fatalf("push job did not receive an independent execution ID: %+v", store.job)
	}
	persisted := string(store.job.Payload)
	for _, secret := range []string{"client.example", "notification-secret", "auth-secret"} {
		if strings.Contains(persisted, secret) {
			t.Fatalf("plaintext secret %q persisted in job: %s", secret, persisted)
		}
	}
	if count, err := handler.ProcessA2APushNotifications(t.Context()); err != nil || count != 1 || !store.completed || !strings.Contains(delivered, `"state":"TASK_STATE_COMPLETED"`) {
		t.Fatalf("count=%d completed=%t delivered=%s err=%v", count, store.completed, delivered, err)
	}
	card := httptest.NewRecorder()
	Routes(handler).ServeHTTP(card, httptest.NewRequest(http.MethodGet, "/a2a/research/.well-known/agent-card.json", nil))
	if !strings.Contains(card.Body.String(), `"pushNotifications":true`) {
		t.Fatalf("configured capability missing: %s", card.Body.String())
	}
}

func TestA2APushFailureKeepsDurableJobForRetry(t *testing.T) {
	store := &a2aPushTestStore{a2aMemoryTaskStore: &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}}
	handler, _, _ := newA2APushTestHandler(t, store)
	handler.a2aHTTPClient = a2aHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("retry")), Request: request}, nil
	})
	task := a2aTask{ID: "task_retry", ContextID: "context", Status: a2aTaskStatus{State: "TASK_STATE_COMPLETED"}, History: []a2aMessage{{MessageID: "m", ContextID: "context", TaskID: "task_retry", Role: "ROLE_USER", Parts: []a2aPart{{Text: a2aStringPointer("hello")}}}}, Artifacts: []a2aArtifact{{ArtifactID: "artifact", Parts: []a2aPart{{Text: a2aStringPointer("done")}}}}}
	payload, _ := json.Marshal(task)
	stored, _ := store.CreateA2ATask(t.Context(), a2astate.Task{ID: task.ID, OwnerKey: "credential:credential", AgentID: "research", Model: "test-model", ContextID: task.ContextID, State: task.Status.State, Payload: payload}, 10, time.Hour)
	job, err := handler.newA2APushJob(modules.RequestContext{CredentialID: "credential", RequestID: "exec_retry"}, stored, a2aPushConfig{URL: "https://client.example/hook"})
	if err != nil {
		t.Fatal(err)
	}
	store.job = &job
	if count, err := handler.ProcessA2APushNotifications(t.Context()); err == nil || count != 1 || store.job == nil || store.retry != time.Second || store.completed {
		t.Fatalf("count=%d retry=%s completed=%t job=%+v err=%v", count, store.retry, store.completed, store.job, err)
	}
}

func TestA2APushWorkerReconcilesBackgroundTaskAfterBillingSettlement(t *testing.T) {
	store := &a2aPushTestStore{a2aMemoryTaskStore: &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}}
	handler, upstream, _ := newA2APushTestHandler(t, store)
	upstream.response = openai.ResponseResponse{ID: "resp_background_push", Model: "test-model", Status: "queued"}
	upstream.retrieved = openai.ResponseResponse{ID: "resp_background_push", Model: "test-model", Status: "completed", OutputText: "finished"}
	upstream.backgroundSettled = true
	deliveries := 0
	handler.a2aHTTPClient = a2aHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		deliveries++
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"state":"TASK_STATE_COMPLETED"`) || !strings.Contains(string(body), "finished") {
			t.Fatalf("terminal task was not delivered: %s", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})
	returnImmediately := true
	body := `{"jsonrpc":"2.0","id":"push-background","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client","role":"ROLE_USER","parts":[{"text":"work"}]},"configuration":{"returnImmediately":` + strconv.FormatBool(returnImmediately) + `,"pushNotificationConfig":{"url":"https://client.example/hook"}}}}`
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.job == nil {
		t.Fatalf("status=%d job=%+v body=%s", response.Code, store.job, response.Body.String())
	}
	if count, err := handler.ProcessA2APushNotifications(t.Context()); err != nil || count != 1 || deliveries != 1 || !store.completed || upstream.retrieveCalls != 1 {
		t.Fatalf("count=%d deliveries=%d completed=%t retrieves=%d err=%v", count, deliveries, store.completed, upstream.retrieveCalls, err)
	}
}

func TestA2APushValidationAndVaultBinding(t *testing.T) {
	for _, config := range []a2aPushConfig{
		{URL: "http://client.example/hook"},
		{URL: "https://user@client.example/hook"},
		{URL: "https://client.example/hook#fragment"},
		{URL: "https://client.example/hook", Token: "secret\r\nInjected: true"},
		{URL: "https://client.example/hook", Authentication: &a2aPushAuthentication{Schemes: []string{"Digest"}, Credentials: "secret"}},
	} {
		if config.validate() == nil {
			t.Errorf("accepted invalid config: %+v", config)
		}
	}
	if _, err := newA2APushVault([]byte("short")); err == nil {
		t.Fatal("accepted short encryption key")
	}
	store := &a2aPushTestStore{a2aMemoryTaskStore: &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}}
	handler, _, _ := newA2APushTestHandler(t, store)
	task := a2astate.Task{ID: "task", OwnerKey: "owner", AgentID: "research"}
	job, err := handler.newA2APushJob(modules.RequestContext{CredentialID: "credential", RequestID: "execution"}, task, a2aPushConfig{URL: "https://client.example/hook"})
	if err != nil {
		t.Fatal(err)
	}
	job.OwnerKey = "other-owner"
	if _, err := handler.decodeA2APushJob(job); err == nil {
		t.Fatal("encrypted job decrypted in a different owner scope")
	}
}
