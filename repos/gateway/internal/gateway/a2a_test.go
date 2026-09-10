package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type a2aTestProvider struct{ request modules.RequestContext }

type a2aMemoryTaskStore struct {
	mu    sync.Mutex
	tasks map[string]a2astate.Task
}

func (s *a2aMemoryTaskStore) CreateA2ATask(_ context.Context, task a2astate.Task, quota int, ttl time.Duration) (a2astate.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tasks) >= quota {
		return a2astate.Task{}, a2astate.ErrQuotaExceeded
	}
	if _, exists := s.tasks[task.ID]; exists {
		return a2astate.Task{}, a2astate.ErrConflict
	}
	task.CreatedAt = time.Now().UTC()
	task.UpdatedAt = task.CreatedAt
	task.ExpiresAt = task.CreatedAt.Add(ttl)
	task.Payload = append([]byte(nil), task.Payload...)
	s.tasks[task.ID] = task
	return task, nil
}

func (s *a2aMemoryTaskStore) GetA2ATask(_ context.Context, owner, agent, id string) (a2astate.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, found := s.tasks[id]
	if !found || task.OwnerKey != owner || task.AgentID != agent {
		return a2astate.Task{}, a2astate.ErrNotFound
	}
	task.Payload = append([]byte(nil), task.Payload...)
	return task, nil
}

func (s *a2aMemoryTaskStore) ListA2ATasks(_ context.Context, owner, agent string, options a2astate.ListOptions) ([]a2astate.Task, string, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var tasks []a2astate.Task
	for _, task := range s.tasks {
		if task.OwnerKey == owner && task.AgentID == agent && task.Model == options.Model && (options.ContextID == "" || task.ContextID == options.ContextID) && (options.State == "" || task.State == options.State) {
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID > tasks[j].ID })
	total := len(tasks)
	if len(tasks) > options.Limit {
		next := tasks[options.Limit-1].ID
		return tasks[:options.Limit], next, total, nil
	}
	return tasks, "", total, nil
}

func (*a2aTestProvider) ChatCompletions(context.Context, modules.RequestContext) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}
func (*a2aTestProvider) StreamChatCompletions(context.Context, modules.RequestContext, provider.ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	return openai.ChatCompletionResponse{}, false, nil
}
func (p *a2aTestProvider) Responses(_ context.Context, request modules.RequestContext) (openai.ResponseResponse, error) {
	p.request = request
	return openai.ResponseResponse{ID: "resp_agent", Model: request.ResponseRequest.Model, Status: "completed", OutputText: "hello from agent"}, nil
}
func (*a2aTestProvider) StreamResponses(context.Context, modules.RequestContext, provider.ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	return openai.ResponseResponse{}, false, nil
}
func (*a2aTestProvider) Models() []openai.Model {
	return []openai.Model{{ID: "test-model", Object: "model"}}
}

func a2aTestHandler(t *testing.T) (http.Handler, *a2aTestProvider, *lifecycleBillingModule) {
	return a2aTestHandlerWithTasks(t, nil)
}

func a2aTestHandlerWithTasks(t *testing.T, tasks a2astate.Store) (http.Handler, *a2aTestProvider, *lifecycleBillingModule) {
	t.Helper()
	registry := NewAgentRegistry()
	if _, err := registry.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"weather"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutAgentProfile("research", AgentProfile{Name: "Research", Description: "Answers questions", Model: "test-model", ToolPolicyID: "safe", MaxIterations: 3, Tags: []string{"research"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	llm := &a2aTestProvider{}
	billing := &lifecycleBillingModule{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"test-model"}}, billing}), llm).WithAgentRegistry(registry)
	if tasks != nil {
		handler = handler.WithA2ATaskStore(tasks, A2ATaskRuntimeConfig{OwnerQuota: 10, TTL: time.Hour})
	}
	return Routes(handler), llm, billing
}

func TestA2AAgentCardDeclaresOnlyImplementedCapabilities(t *testing.T) {
	router, _, _ := a2aTestHandler(t)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://gateway.example/a2a/research/.well-known/agent-card.json", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	router.ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{`"url":"https://gateway.example/a2a/research"`, `"protocolBinding":"JSONRPC"`, `"protocolVersion":"1.0"`, `"tenant":"research"`, `"streaming":false`, `"pushNotifications":false`, `"httpAuthSecurityScheme"`, `"schemes":{"bearer":{"list":[]}}`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("card missing %s: %s", expected, body)
		}
	}
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(body, "test-model") || strings.Contains(body, "weather") {
		t.Fatalf("unsafe agent card: status=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
}

func TestA2ASendMessageUsesResponsesPolicyAndBillingPath(t *testing.T) {
	router, llm, billing := a2aTestHandler(t)
	body := `{"jsonrpc":"2.0","id":"rpc-1","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-message","contextId":"context-1","role":"ROLE_USER","parts":[{"text":"hello"}]},"configuration":{"acceptedOutputModes":["text/plain"]}}}`
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	request.Header.Set("X-Request-ID", "external-correlation")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("X-Execution-ID") == "" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	result := envelope["result"].(map[string]any)
	message := result["message"].(map[string]any)
	if envelope["id"] != "rpc-1" || message["role"] != "ROLE_AGENT" || message["contextId"] != "context-1" || message["messageId"] != "resp_agent" || message["parts"].([]any)[0].(map[string]any)["text"] != "hello from agent" {
		t.Fatalf("unexpected envelope: %v", envelope)
	}
	if llm.request.ResponseRequest == nil || llm.request.ResponseRequest.Model != "test-model" || llm.request.ResponseRequest.User != "" || llm.request.Metadata["gateway.api_type"] != "a2a" || llm.request.RequestID == "external-correlation" {
		t.Fatalf("request bypassed shared execution semantics: %+v", llm.request)
	}
	if billing.calls != 1 {
		t.Fatalf("billing preflight calls=%d", billing.calls)
	}
}

func TestA2ARejectsUnsupportedProtocolFeatures(t *testing.T) {
	router, _, _ := a2aTestHandler(t)
	tests := []struct {
		name, version, body, code string
	}{
		{"version", "0.3", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{}}`, `"code":-32009`},
		{"method", "1.0", `{"jsonrpc":"2.0","id":1,"method":"UnknownMethod","params":{}}`, `"code":-32601`},
		{"task lifecycle", "1.0", `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"tenant":"research","id":"task_missing"}}`, `"code":-32004`},
		{"task", "1.0", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"tenant":"research","message":{"messageId":"m","taskId":"t","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`, `"code":-32004`},
		{"binary", "1.0", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"tenant":"research","message":{"messageId":"m","role":"ROLE_USER","parts":[{"raw":"AA==","mediaType":"application/octet-stream"}]}}}`, `"code":-32005`},
		{"push", "1.0", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"tenant":"research","message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hello"}]},"configuration":{"pushNotificationConfig":{}}}}`, `"code":-32003`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(test.body))
			request.Header.Set("A2A-Version", test.version)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code < 400 || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestA2ACompletedTaskLifecycleUsesDurableOwnerScope(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, _, billing := a2aTestHandlerWithTasks(t, store)
	send := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"send","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-message","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`))
	send.Header.Set("A2A-Version", "1.0")
	send.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, send)
	if response.Code != http.StatusOK || billing.calls != 1 {
		t.Fatalf("send status=%d billing=%d body=%s", response.Code, billing.calls, response.Body.String())
	}
	var envelope struct {
		Result struct {
			Task a2aTask `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	task := envelope.Result.Task
	if task.ID == "" || task.ContextID == "" || task.Status.State != "TASK_STATE_COMPLETED" || len(task.History) != 2 || len(task.Artifacts) != 1 {
		t.Fatalf("unexpected task: %+v", task)
	}

	get := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"get","method":"GetTask","params":{"tenant":"research","id":"`+task.ID+`","historyLength":1}}`))
	get.Header.Set("A2A-Version", "1.0")
	get.Header.Set("Authorization", "Bearer key")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, get)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"`+task.ID+`"`) || strings.Count(response.Body.String(), `"role":`) != 1 {
		t.Fatalf("get status=%d body=%s", response.Code, response.Body.String())
	}

	list := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"list","method":"ListTasks","params":{"tenant":"research","pageSize":1,"includeArtifacts":false}}`))
	list.Header.Set("A2A-Version", "1.0")
	list.Header.Set("Authorization", "Bearer key")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, list)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"totalSize":1`) || strings.Contains(response.Body.String(), `"artifacts"`) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}

	cancel := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"cancel","method":"CancelTask","params":{"tenant":"research","id":"`+task.ID+`"}}`))
	cancel.Header.Set("A2A-Version", "1.0")
	cancel.Header.Set("Authorization", "Bearer key")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, cancel)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":-32002`) {
		t.Fatalf("cancel status=%d body=%s", response.Code, response.Body.String())
	}
	if billing.calls != 1 {
		t.Fatalf("resource operations must not enter inference billing: %d", billing.calls)
	}

	store.mu.Lock()
	stored := store.tasks[task.ID]
	stored.Model = "restricted-model"
	store.tasks[task.ID] = stored
	store.mu.Unlock()
	get = httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"get-denied","method":"GetTask","params":{"tenant":"research","id":"`+task.ID+`"}}`))
	get.Header.Set("A2A-Version", "1.0")
	get.Header.Set("Authorization", "Bearer key")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, get)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "Task access is not allowed") {
		t.Fatalf("model authorization status=%d body=%s", response.Code, response.Body.String())
	}
}
