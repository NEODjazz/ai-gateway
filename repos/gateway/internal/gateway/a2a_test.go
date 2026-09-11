package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type a2aTestProvider struct {
	request           modules.RequestContext
	response          openai.ResponseResponse
	retrieved         openai.ResponseResponse
	canceled          openai.ResponseResponse
	retrieveCalls     int
	cancellationCalls int
	stream            bool
	preStreamFailure  bool
	streamFailure     bool
	terminalFailure   bool
	backgroundSettled bool
}

type a2aCredentialAuth struct{}

func (*a2aCredentialAuth) Name() string   { return "auth" }
func (*a2aCredentialAuth) Required() bool { return true }
func (*a2aCredentialAuth) Handle(_ context.Context, req *modules.RequestContext) error {
	if req.APIKey != "key" {
		return modules.ErrUnauthorized
	}
	req.APIKey = ""
	req.CredentialID = "credential"
	req.UserID = "user"
	req.AllowedModels = []string{"test-model"}
	return nil
}

type a2aMemoryTaskStore struct {
	mu        sync.Mutex
	tasks     map[string]a2astate.Task
	updateErr error
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

func (s *a2aMemoryTaskStore) UpdateA2ATask(_ context.Context, task a2astate.Task, expected time.Time, ttl time.Duration) (a2astate.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.tasks[task.ID]
	if !found || current.OwnerKey != task.OwnerKey || current.AgentID != task.AgentID {
		return a2astate.Task{}, a2astate.ErrNotFound
	}
	if !current.UpdatedAt.Equal(expected) {
		return a2astate.Task{}, a2astate.ErrConflict
	}
	if s.updateErr != nil {
		return a2astate.Task{}, s.updateErr
	}
	task.CreatedAt = current.CreatedAt
	task.UpdatedAt = current.UpdatedAt.Add(time.Nanosecond)
	task.ExpiresAt = task.UpdatedAt.Add(ttl)
	task.Payload = append([]byte(nil), task.Payload...)
	s.tasks[task.ID] = task
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
	if p.response.ID != "" {
		return p.response, nil
	}
	return openai.ResponseResponse{ID: "resp_agent", Model: request.ResponseRequest.Model, Status: "completed", OutputText: "hello from agent"}, nil
}
func (p *a2aTestProvider) StreamResponses(_ context.Context, request modules.RequestContext, write provider.ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	if !p.stream {
		return openai.ResponseResponse{}, false, nil
	}
	p.request = request
	if p.preStreamFailure {
		return openai.ResponseResponse{}, true, errors.New("upstream stream failed")
	}
	response := openai.ResponseResponse{ID: "resp_stream", Object: "response", Model: request.ResponseRequest.Model, Status: "completed", OutputText: "hello live"}
	created, _ := json.Marshal(map[string]any{"type": "response.created", "response": openai.ResponseResponse{ID: response.ID, Object: "response", Model: response.Model, Status: "in_progress"}})
	delta, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "delta": "hello live"})
	completed, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
	for _, event := range []struct {
		name    string
		payload []byte
	}{{"response.created", created}, {"response.output_text.delta", delta}, {"response.completed", completed}} {
		if err := write(event.name, string(event.payload)); err != nil {
			return openai.ResponseResponse{}, true, err
		}
		if p.streamFailure && event.name == "response.output_text.delta" {
			return openai.ResponseResponse{}, true, errors.New("upstream stream failed")
		}
		if p.terminalFailure && event.name == "response.completed" {
			return openai.ResponseResponse{}, true, errors.New("post-response settlement failed")
		}
	}
	return response, true, nil
}
func (*a2aTestProvider) Models() []openai.Model {
	return []openai.Model{{ID: "test-model", Object: "model"}}
}
func (*a2aTestProvider) ResolveResponseResource(context.Context, modules.RequestContext, string) (string, error) {
	return "test-model", nil
}
func (p *a2aTestProvider) RetrieveResponse(context.Context, modules.RequestContext, string) (openai.ResponseResponse, error) {
	p.retrieveCalls++
	return p.retrieved, nil
}
func (p *a2aTestProvider) CancelResponse(context.Context, modules.RequestContext, string) (openai.ResponseResponse, error) {
	p.cancellationCalls++
	return p.canceled, nil
}
func (p *a2aTestProvider) BackgroundResponseSettled(context.Context, modules.RequestContext, string) (bool, error) {
	return p.backgroundSettled, nil
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
		handler = handler.WithA2ATaskStore(tasks, A2ATaskRuntimeConfig{OwnerQuota: 10, TTL: time.Hour, SubscriptionLimit: 2, SubscriptionDuration: time.Second, SubscriptionPoll: time.Millisecond})
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
	for _, expected := range []string{`"url":"https://gateway.example/a2a/research"`, `"protocolBinding":"JSONRPC"`, `"protocolVersion":"1.0"`, `"tenant":"research"`, `"streaming":false`, `"pushNotifications":false`, `"extendedAgentCard":true`, `"httpAuthSecurityScheme"`, `"schemes":{"bearer":{"list":[]}}`, `"image/png"`, `"audio/wav"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("card missing %s: %s", expected, body)
		}
	}
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(body, "test-model") || strings.Contains(body, "weather") {
		t.Fatalf("unsafe agent card: status=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
}

func TestA2AExtendedAgentCardRequiresAuthenticationAndModelAccess(t *testing.T) {
	registry := NewAgentRegistry()
	if _, err := registry.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"weather"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutAgentProfile("research", AgentProfile{Name: "Research", Description: "Answers questions", Model: "test-model", ToolPolicyID: "safe", MaxIterations: 3, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	billing := &lifecycleBillingModule{}
	router := Routes(NewHandler(modules.NewPipeline([]modules.Module{&a2aCredentialAuth{}, billing}), &a2aTestProvider{}).WithAgentRegistry(registry))
	call := func(token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "http://gateway.example/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"card","method":"GetExtendedAgentCard","params":{"tenant":"research"}}`))
		request.Header.Set("A2A-Version", "1.0")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	unauthorized := call("")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	authorized := call("key")
	if authorized.Code != http.StatusOK || authorized.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(authorized.Body.String(), `"extendedAgentCard":true`) || strings.Contains(authorized.Body.String(), "test-model") || strings.Contains(authorized.Body.String(), "weather") {
		t.Fatalf("authorized status=%d headers=%v body=%s", authorized.Code, authorized.Header(), authorized.Body.String())
	}
	if billing.calls != 0 {
		t.Fatalf("card lookup entered inference billing: %d", billing.calls)
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

func TestA2ASendMessageAcceptsBoundedInlineImages(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, llm, billing := a2aTestHandlerWithTasks(t, store)
	body := `{"jsonrpc":"2.0","id":"rpc-image","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-image","role":"ROLE_USER","parts":[{"text":"describe"},{"raw":"iVBORw0KGgo=","mediaType":"image/png"}]}}}`
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || billing.calls != 1 {
		t.Fatalf("status=%d billing=%d body=%s", response.Code, billing.calls, response.Body.String())
	}
	input := llm.request.ResponseRequest.Input.([]any)
	parts := input[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("input=%#v", input)
	}
	image := parts[1].(map[string]any)
	if image["type"] != "input_image" || image["image_url"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("input=%#v", input)
	}
	var envelope struct {
		Result struct {
			Task a2aTask `json:"task"`
		} `json:"result"`
	}
	if json.Unmarshal(response.Body.Bytes(), &envelope) != nil {
		t.Fatal("decode task")
	}
	stored := store.tasks[envelope.Result.Task.ID]
	decoded, err := decodeA2ATask(stored.Payload)
	if err != nil || decoded.History[0].Parts[1].Raw == nil || *decoded.History[0].Parts[1].Raw != "iVBORw0KGgo=" {
		t.Fatalf("stored task=%+v err=%v", decoded, err)
	}
}

type a2aHTTPDoerFunc func(*http.Request) (*http.Response, error)

func (f a2aHTTPDoerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func a2aStringPointer(value string) *string { return &value }

func TestA2ASendMessageFetchesAuthorizedRemoteImageIntoPolicyPath(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	registry := NewAgentRegistry()
	if _, err := registry.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"weather"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutAgentProfile("research", AgentProfile{Name: "Research", Description: "Answers questions", Model: "test-model", ToolPolicyID: "safe", MaxIterations: 3, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	llm := &a2aTestProvider{}
	billing := &lifecycleBillingModule{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"test-model"}}, billing}), llm).
		WithAgentRegistry(registry).
		WithA2ATaskStore(store, A2ATaskRuntimeConfig{OwnerQuota: 10, TTL: time.Hour})
	fetches := 0
	handler.a2aHTTPClient = a2aHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		fetches++
		if request.URL.String() != "https://media.example/image.png" || request.Header.Get("Authorization") != "" || !strings.Contains(request.Header.Get("Accept"), "image/png") {
			t.Fatalf("unsafe remote request: url=%s headers=%v", request.URL, request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\n")),
			Request:    request,
		}, nil
	})
	router := Routes(handler)
	body := `{"jsonrpc":"2.0","id":"rpc-remote","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-remote","role":"ROLE_USER","parts":[{"url":"https://media.example/image.png","mediaType":"image/png","filename":"image.png"}]}}}`
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || fetches != 1 || billing.calls != 1 {
		t.Fatalf("status=%d fetches=%d billing=%d body=%s", response.Code, fetches, billing.calls, response.Body.String())
	}
	input := llm.request.ResponseRequest.Input.([]any)
	image := input[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if image["image_url"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("remote image did not enter shared policy path: %#v", input)
	}
	var envelope struct {
		Result struct {
			Task a2aTask `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeA2ATask(store.tasks[envelope.Result.Task.ID].Payload)
	if err != nil || decoded.History[0].Parts[0].URL != nil || decoded.History[0].Parts[0].Raw == nil {
		t.Fatalf("remote URL was not replaced before persistence: task=%+v err=%v", decoded, err)
	}
}

func TestA2ASendMessageRejectsRemoteImageBeforeNetworkWithoutAuthorization(t *testing.T) {
	registry := NewAgentRegistry()
	if _, err := registry.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"weather"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutAgentProfile("research", AgentProfile{Name: "Research", Description: "Answers questions", Model: "test-model", ToolPolicyID: "safe", MaxIterations: 3, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(modules.NewPipeline([]modules.Module{&a2aCredentialAuth{}}), &a2aTestProvider{}).WithAgentRegistry(registry)
	handler.a2aHTTPClient = a2aHTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("remote URL fetched before authorization")
		return nil, nil
	})
	router := Routes(handler)
	body := `{"jsonrpc":"2.0","id":"rpc-remote","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-remote","role":"ROLE_USER","parts":[{"url":"https://media.example/image.png","mediaType":"image/png"}]}}}`
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
	request.Header.Set("A2A-Version", "1.0")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestA2ASendMessageAcceptsValidatedInlineAudio(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	registry := NewAgentRegistry()
	if _, err := registry.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"weather"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutAgentProfile("research", AgentProfile{Name: "Research", Description: "Answers questions", Model: "test-model", ToolPolicyID: "safe", MaxIterations: 3, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	llm := &a2aTestProvider{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"test-model"}}, &lifecycleBillingModule{}}), llm).
		WithAgentRegistry(registry).
		WithA2ATaskStore(store, A2ATaskRuntimeConfig{OwnerQuota: 10, TTL: time.Hour}))
	body := `{"jsonrpc":"2.0","id":"audio","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-audio","role":"ROLE_USER","parts":[{"raw":"UklGRgAAAABXQVZF","mediaType":"audio/wav","filename":"sample.wav"}]}}}`
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	input := llm.request.ResponseRequest.Input.([]any)
	audio := input[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if audio["type"] != "input_audio" || audio["input_audio"].(map[string]any)["format"] != "wav" {
		t.Fatalf("audio input=%#v", input)
	}

	invalid := strings.Replace(body, "UklGRgAAAABXQVZF", "aW52YWxpZA==", 1)
	badRequest := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(invalid))
	badRequest.Header.Set("A2A-Version", "1.0")
	badRequest.Header.Set("Authorization", "Bearer key")
	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, badRequest)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", bad.Code, bad.Body.String())
	}
}

func TestA2ARemoteImageValidationAndLimits(t *testing.T) {
	invalid := []a2aPart{
		{URL: a2aStringPointer("http://media.example/image.png"), MediaType: "image/png"},
		{URL: a2aStringPointer("https://user@media.example/image.png"), MediaType: "image/png"},
		{URL: a2aStringPointer("https://media.example/image.svg"), MediaType: "image/svg+xml"},
		{URL: a2aStringPointer("https://media.example/image.png"), MediaType: "image/png", Filename: "../image.png"},
	}
	for _, part := range invalid {
		if _, err := countA2ARemoteParts([]a2aPart{part}); err == nil {
			t.Errorf("accepted invalid remote part: %+v", part)
		}
	}
	parts := make([]a2aPart, openai.MaxImageAttachments+1)
	for index := range parts {
		parts[index] = a2aPart{URL: a2aStringPointer("https://media.example/image.png"), MediaType: "image/png"}
	}
	if _, err := countA2ARemoteParts(parts); err == nil {
		t.Fatal("accepted too many remote images")
	}
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/jpeg"}}, Body: io.NopCloser(strings.NewReader("\xff\xd8\xff"))}
	if _, _, err := readA2ARemoteImage(response, "image/png", openai.MaxTotalImageBytes); err == nil {
		t.Fatal("accepted mismatched response content type")
	}
}

func TestA2ASendMessageAcceptsStructuredDataThroughPolicyPath(t *testing.T) {
	router, llm, billing := a2aTestHandler(t)
	body := `{"jsonrpc":"2.0","id":"rpc-data","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-data","role":"ROLE_USER","parts":[{"data":{"city":"Paris","days":2},"mediaType":"application/json"}]}}}`
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || billing.calls != 1 || llm.request.ResponseRequest == nil {
		t.Fatalf("status=%d billing=%d request=%+v body=%s", response.Code, billing.calls, llm.request.ResponseRequest, response.Body.String())
	}
	input := llm.request.ResponseRequest.Input.([]any)
	part := input[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != `{"city":"Paris","days":2}` {
		t.Fatalf("canonical data part=%+v", part)
	}
}

func TestA2ASendStreamingMessagePersistsOrderedTaskLifecycle(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, llm, billing := a2aTestHandlerWithTasks(t, store)
	llm.stream = true
	card := httptest.NewRecorder()
	router.ServeHTTP(card, httptest.NewRequest(http.MethodGet, "/a2a/research/.well-known/agent-card.json", nil))
	if card.Code != http.StatusOK || !strings.Contains(card.Body.String(), `"streaming":true`) {
		t.Fatalf("card status=%d body=%s", card.Code, card.Body.String())
	}
	body := `{"jsonrpc":"2.0","id":"rpc-stream","method":"SendStreamingMessage","params":{"tenant":"research","message":{"messageId":"client-stream","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" || billing.calls != 1 {
		t.Fatalf("status=%d headers=%v billing=%d body=%s", response.Code, response.Header(), billing.calls, response.Body.String())
	}
	wire := response.Body.String()
	if strings.Contains(wire, "event:") || strings.Contains(wire, "[DONE]") {
		t.Fatalf("non-conformant SSE framing: %s", wire)
	}
	firstTask := strings.Index(wire, `"task":{"id":`)
	firstArtifact := strings.Index(wire, `"artifactUpdate":`)
	terminalStatus := strings.LastIndex(wire, `"statusUpdate":`)
	if firstTask < 0 || firstArtifact <= firstTask || terminalStatus <= firstArtifact || !strings.Contains(wire, `"state":"TASK_STATE_COMPLETED"`) {
		t.Fatalf("unordered stream: %s", wire)
	}
	if strings.Contains(wire, `"index":`) || strings.Contains(wire, `"taskArtifactUpdate":`) || strings.Contains(wire, `"taskStatusUpdate":`) {
		t.Fatalf("stream uses non-v1 fields: %s", wire)
	}
	if llm.request.ResponseRequest == nil || !llm.request.ResponseRequest.Stream || llm.request.Metadata["gateway.api_type"] != "a2a" {
		t.Fatalf("stream request=%+v", llm.request)
	}
	if len(store.tasks) != 1 {
		t.Fatalf("stored tasks=%d", len(store.tasks))
	}
	for _, stored := range store.tasks {
		decoded, err := decodeA2ATask(stored.Payload)
		if err != nil || decoded.Status.State != "TASK_STATE_COMPLETED" || len(decoded.History) != 2 || len(decoded.Artifacts) != 1 || decoded.Artifacts[0].ArtifactID == "" {
			t.Fatalf("stored=%+v err=%v", decoded, err)
		}
	}
}

func TestA2AStreamingFailurePersistsFailedTask(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, llm, billing := a2aTestHandlerWithTasks(t, store)
	llm.stream, llm.streamFailure = true, true
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"rpc-stream","method":"SendStreamingMessage","params":{"tenant":"research","message":{"messageId":"client-stream","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"TASK_STATE_FAILED"`) || billing.calls != 1 {
		t.Fatalf("status=%d billing=%d body=%s", response.Code, billing.calls, response.Body.String())
	}
	for _, stored := range store.tasks {
		decoded, err := decodeA2ATask(stored.Payload)
		if err != nil || decoded.Status.State != "TASK_STATE_FAILED" {
			t.Fatalf("stored=%+v err=%v", decoded, err)
		}
	}
}

func TestA2AStreamingWrapsFailuresBeforeFirstEvent(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, llm, billing := a2aTestHandlerWithTasks(t, store)
	llm.stream, llm.preStreamFailure = true, true
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"rpc-stream","method":"SendStreamingMessage","params":{"tenant":"research","message":{"messageId":"client-stream","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code < 500 || response.Header().Get("Content-Type") != "application/json" || !strings.Contains(response.Body.String(), `"jsonrpc":"2.0"`) || !strings.Contains(response.Body.String(), `"id":"rpc-stream"`) || !strings.Contains(response.Body.String(), `"error":`) || billing.calls != 1 || len(store.tasks) != 0 {
		t.Fatalf("status=%d headers=%v billing=%d tasks=%d body=%s", response.Code, response.Header(), billing.calls, len(store.tasks), response.Body.String())
	}
}

func TestA2AStreamingDoesNotPublishSuccessBeforeSettlement(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, llm, _ := a2aTestHandlerWithTasks(t, store)
	llm.stream, llm.terminalFailure = true, true
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"rpc-stream","method":"SendStreamingMessage","params":{"tenant":"research","message":{"messageId":"client-stream","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if strings.Contains(response.Body.String(), `"state":"TASK_STATE_COMPLETED"`) || !strings.Contains(response.Body.String(), `"state":"TASK_STATE_FAILED"`) {
		t.Fatalf("premature terminal success: %s", response.Body.String())
	}
	for _, stored := range store.tasks {
		decoded, err := decodeA2ATask(stored.Payload)
		if err != nil || decoded.Status.State != "TASK_STATE_FAILED" {
			t.Fatalf("stored=%+v err=%v", decoded, err)
		}
	}
}

func TestA2ASubscribeToTaskStreamsPostSettlementLifecycle(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, llm, billing := a2aTestHandlerWithTasks(t, store)
	llm.response = openai.ResponseResponse{ID: "resp_subscribe", Model: "test-model", Status: "queued"}
	call := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
		request.Header.Set("A2A-Version", "1.0")
		request.Header.Set("Authorization", "Bearer key")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	created := call(`{"jsonrpc":"2.0","id":"send","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hello"}]},"configuration":{"returnImmediately":true}}}`)
	var sent struct {
		Result struct {
			Task a2aTask `json:"task"`
		} `json:"result"`
	}
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &sent) != nil {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	llm.retrieved = openai.ResponseResponse{ID: "resp_subscribe", Model: "test-model", Status: "completed", OutputText: "done"}
	llm.backgroundSettled = true
	stream := call(`{"jsonrpc":"2.0","id":"subscribe","method":"SubscribeToTask","params":{"tenant":"research","id":"` + sent.Result.Task.ID + `"}}`)
	wire := stream.Body.String()
	if stream.Code != http.StatusOK || stream.Header().Get("Content-Type") != "text/event-stream" || strings.Contains(wire, "[DONE]") || strings.Contains(wire, "event:") {
		t.Fatalf("status=%d headers=%v body=%s", stream.Code, stream.Header(), wire)
	}
	firstTask, artifact, status := strings.Index(wire, `"task":`), strings.Index(wire, `"artifactUpdate":`), strings.Index(wire, `"statusUpdate":`)
	if firstTask < 0 || artifact <= firstTask || status <= artifact || !strings.Contains(wire, `"state":"TASK_STATE_COMPLETED"`) || billing.calls != 1 {
		t.Fatalf("unordered subscription billing=%d body=%s", billing.calls, wire)
	}
}

func TestA2ARejectsMalformedInlineImageBeforeBilling(t *testing.T) {
	router, _, billing := a2aTestHandler(t)
	body := `{"jsonrpc":"2.0","id":"rpc-image","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-image","role":"ROLE_USER","parts":[{"raw":"bm90LWEtcG5n","mediaType":"image/png"}]}}}`
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":-32005`) || billing.calls != 0 {
		t.Fatalf("status=%d billing=%d body=%s", response.Code, billing.calls, response.Body.String())
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
		{"streaming", "1.0", `{"jsonrpc":"2.0","id":1,"method":"SendStreamingMessage","params":{"tenant":"research","message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`, `"code":-32004`},
		{"task", "1.0", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"tenant":"research","message":{"messageId":"m","taskId":"t","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`, `"code":-32004`},
		{"binary", "1.0", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"tenant":"research","message":{"messageId":"m","role":"ROLE_USER","parts":[{"raw":"AA==","mediaType":"application/octet-stream"}]}}}`, `"code":-32005`},
		{"push", "1.0", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"tenant":"research","message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hello"}]},"configuration":{"pushNotificationConfig":{}}}}`, `"code":-32003`},
		{"unknown", "1.0", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"tenant":"research","message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hello","unknown":true}]}}}`, `"code":-32700`},
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

func TestA2AStreamingRejectsReturnImmediatelyBeforeBilling(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, _, billing := a2aTestHandlerWithTasks(t, store)
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"stream","method":"SendStreamingMessage","params":{"tenant":"research","message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hello"}]},"configuration":{"returnImmediately":true}}}`))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":-32602`) || billing.calls != 0 || len(store.tasks) != 0 {
		t.Fatalf("status=%d billing=%d tasks=%d body=%s", response.Code, billing.calls, len(store.tasks), response.Body.String())
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

func TestA2AReturnImmediatelyPersistsAndMaterializesBackgroundTask(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, llm, billing := a2aTestHandlerWithTasks(t, store)
	llm.response = openai.ResponseResponse{ID: "resp_background", Model: "test-model", Status: "queued"}

	send := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"send","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-message","role":"ROLE_USER","parts":[{"text":"hello"}]},"configuration":{"returnImmediately":true}}}`))
	send.Header.Set("A2A-Version", "1.0")
	send.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, send)
	var sent struct {
		Result struct {
			Task a2aTask `json:"task"`
		} `json:"result"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &sent) != nil {
		t.Fatalf("send status=%d body=%s", response.Code, response.Body.String())
	}
	if sent.Result.Task.Status.State != "TASK_STATE_SUBMITTED" || len(sent.Result.Task.History) != 1 || len(sent.Result.Task.Artifacts) != 0 {
		t.Fatalf("unexpected submitted task: %+v", sent.Result.Task)
	}
	if strings.Contains(response.Body.String(), "backgroundResponseId") || strings.Contains(response.Body.String(), "resp_background") {
		t.Fatalf("internal response binding leaked: %s", response.Body.String())
	}
	if llm.request.ResponseRequest == nil || !llm.request.ResponseRequest.Background || llm.request.ResponseRequest.Store == nil || !*llm.request.ResponseRequest.Store || billing.calls != 1 {
		t.Fatalf("request=%+v billing=%d", llm.request.ResponseRequest, billing.calls)
	}
	stored := store.tasks[sent.Result.Task.ID]
	_, backgroundID, err := decodeA2AStoredTask(stored.Payload)
	if err != nil || backgroundID != "resp_background" {
		t.Fatalf("background binding=%q err=%v payload=%s", backgroundID, err, stored.Payload)
	}

	llm.retrieved = openai.ResponseResponse{ID: "resp_background", Model: "test-model", Status: "completed", OutputText: "done"}
	get := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"get-pending","method":"GetTask","params":{"tenant":"research","id":"`+sent.Result.Task.ID+`"}}`))
	get.Header.Set("A2A-Version", "1.0")
	get.Header.Set("Authorization", "Bearer key")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, get)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"TASK_STATE_SUBMITTED"`) || strings.Contains(response.Body.String(), `"state":"TASK_STATE_COMPLETED"`) {
		t.Fatalf("unsettled status=%d body=%s", response.Code, response.Body.String())
	}
	llm.backgroundSettled = true
	get = httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"get","method":"GetTask","params":{"tenant":"research","id":"`+sent.Result.Task.ID+`"}}`))
	get.Header.Set("A2A-Version", "1.0")
	get.Header.Set("Authorization", "Bearer key")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, get)
	var got struct {
		Result a2aTask `json:"result"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil {
		t.Fatalf("get status=%d body=%s", response.Code, response.Body.String())
	}
	if got.Result.Status.State != "TASK_STATE_COMPLETED" || len(got.Result.History) != 2 || len(got.Result.Artifacts) != 1 || *got.Result.Artifacts[0].Parts[0].Text != "done" {
		t.Fatalf("unexpected completed task: %+v", got.Result)
	}
	if llm.retrieveCalls != 2 || billing.calls != 1 {
		t.Fatalf("retrieve=%d billing=%d", llm.retrieveCalls, billing.calls)
	}
	_, backgroundID, err = decodeA2AStoredTask(store.tasks[sent.Result.Task.ID].Payload)
	if err != nil || backgroundID != "" {
		t.Fatalf("terminal binding=%q err=%v", backgroundID, err)
	}
}

func TestA2ABackgroundTaskStorageFailureCancelsExecution(t *testing.T) {
	tasks := make(map[string]a2astate.Task, 10)
	for index := 0; index < 10; index++ {
		tasks[strconv.Itoa(index)] = a2astate.Task{}
	}
	store := &a2aMemoryTaskStore{tasks: tasks}
	router, llm, billing := a2aTestHandlerWithTasks(t, store)
	llm.response = openai.ResponseResponse{ID: "resp_orphan", Model: "test-model", Status: "queued"}
	llm.canceled = openai.ResponseResponse{ID: "resp_orphan", Model: "test-model", Status: "cancelled"}

	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"send","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-message","role":"ROLE_USER","parts":[{"text":"hello"}]},"configuration":{"returnImmediately":true}}}`))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "Task quota exceeded") || llm.cancellationCalls != 1 || billing.calls != 1 {
		t.Fatalf("status=%d cancellations=%d billing=%d body=%s", response.Code, llm.cancellationCalls, billing.calls, response.Body.String())
	}
}

func TestMaterializeA2ABackgroundTaskMapsTerminalFailure(t *testing.T) {
	text := "hello"
	task := a2aTask{
		ID: "task", ContextID: "context", Status: a2aTaskStatus{State: "TASK_STATE_SUBMITTED"},
		History: []a2aMessage{{MessageID: "message", ContextID: "context", TaskID: "task", Role: "ROLE_USER", Parts: []a2aPart{{Text: &text}}}},
	}
	failed := materializeA2ABackgroundTask(task, openai.ResponseResponse{ID: "resp", Status: "failed"})
	if failed.Status.State != "TASK_STATE_FAILED" || len(failed.History) != 1 || len(failed.Artifacts) != 0 {
		t.Fatalf("failed task: %+v", failed)
	}
}

func TestA2ACancelBackgroundTaskWaitsForSettlementBeforeTerminalState(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, llm, billing := a2aTestHandlerWithTasks(t, store)
	llm.response = openai.ResponseResponse{ID: "resp_cancel", Model: "test-model", Status: "in_progress"}
	llm.canceled = openai.ResponseResponse{ID: "resp_cancel", Model: "test-model", Status: "cancelled"}

	call := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
		request.Header.Set("A2A-Version", "1.0")
		request.Header.Set("Authorization", "Bearer key")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	created := call(`{"jsonrpc":"2.0","id":"send","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-message","role":"ROLE_USER","parts":[{"text":"hello"}]},"configuration":{"returnImmediately":true}}}`)
	var sent struct {
		Result struct {
			Task a2aTask `json:"task"`
		} `json:"result"`
	}
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &sent) != nil || sent.Result.Task.Status.State != "TASK_STATE_WORKING" {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	canceled := call(`{"jsonrpc":"2.0","id":"cancel","method":"CancelTask","params":{"tenant":"research","id":"` + sent.Result.Task.ID + `"}}`)
	if canceled.Code != http.StatusOK || !strings.Contains(canceled.Body.String(), `"state":"TASK_STATE_WORKING"`) || strings.Contains(canceled.Body.String(), `"state":"TASK_STATE_CANCELED"`) || llm.cancellationCalls != 1 || billing.calls != 1 {
		t.Fatalf("cancel status=%d calls=%d billing=%d body=%s", canceled.Code, llm.cancellationCalls, billing.calls, canceled.Body.String())
	}
	stored := store.tasks[sent.Result.Task.ID]
	decoded, backgroundID, err := decodeA2AStoredTask(stored.Payload)
	if err != nil || decoded.Status.State != "TASK_STATE_WORKING" || backgroundID != "resp_cancel" {
		t.Fatalf("stored=%+v binding=%q err=%v", decoded, backgroundID, err)
	}
	llm.backgroundSettled = true
	llm.retrieved = llm.canceled
	settled := call(`{"jsonrpc":"2.0","id":"get-settled","method":"GetTask","params":{"tenant":"research","id":"` + sent.Result.Task.ID + `"}}`)
	if settled.Code != http.StatusOK || !strings.Contains(settled.Body.String(), `"state":"TASK_STATE_CANCELED"`) || llm.cancellationCalls != 1 {
		t.Fatalf("settled status=%d cancel calls=%d body=%s", settled.Code, llm.cancellationCalls, settled.Body.String())
	}
}

func TestA2ATaskContinuationPreservesHistoryAndBilling(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, llm, billing := a2aTestHandlerWithTasks(t, store)
	call := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
		request.Header.Set("A2A-Version", "1.0")
		request.Header.Set("Authorization", "Bearer key")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	created := call(`{"jsonrpc":"2.0","id":"create","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"first","role":"ROLE_USER","parts":[{"text":"one"}]}}}`)
	var envelope struct {
		Result struct {
			Task a2aTask `json:"task"`
		} `json:"result"`
	}
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &envelope) != nil {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	task := envelope.Result.Task
	continued := call(`{"jsonrpc":"2.0","id":"continue","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"second","taskId":"` + task.ID + `","contextId":"` + task.ContextID + `","role":"ROLE_USER","parts":[{"text":"two"}]}}}`)
	if continued.Code != http.StatusOK || json.Unmarshal(continued.Body.Bytes(), &envelope) != nil {
		t.Fatalf("continue status=%d body=%s", continued.Code, continued.Body.String())
	}
	continuedTask := envelope.Result.Task
	if continuedTask.ID != task.ID || continuedTask.ContextID != task.ContextID || len(continuedTask.History) != 4 || len(continuedTask.Artifacts) != 2 || billing.calls != 2 {
		t.Fatalf("task=%+v billing=%d", continuedTask, billing.calls)
	}
	input, ok := llm.request.ResponseRequest.Input.([]any)
	if !ok || len(input) != 3 || input[0].(map[string]any)["role"] != "user" || input[1].(map[string]any)["role"] != "assistant" || input[2].(map[string]any)["role"] != "user" {
		t.Fatalf("continuation input=%#v", llm.request.ResponseRequest.Input)
	}
	if input[0].(map[string]any)["content"].([]any)[0].(map[string]any)["type"] != "input_text" || input[1].(map[string]any)["content"].([]any)[0].(map[string]any)["type"] != "output_text" {
		t.Fatalf("continuation content=%#v", input)
	}
	duplicate := call(`{"jsonrpc":"2.0","id":"duplicate","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"second","taskId":"` + task.ID + `","role":"ROLE_USER","parts":[{"text":"again"}]}}}`)
	if duplicate.Code != http.StatusConflict || billing.calls != 2 {
		t.Fatalf("duplicate status=%d billing=%d body=%s", duplicate.Code, billing.calls, duplicate.Body.String())
	}
	mismatch := call(`{"jsonrpc":"2.0","id":"mismatch","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"third","taskId":"` + task.ID + `","contextId":"other","role":"ROLE_USER","parts":[{"text":"again"}]}}}`)
	if mismatch.Code != http.StatusConflict || billing.calls != 2 {
		t.Fatalf("mismatch status=%d billing=%d body=%s", mismatch.Code, billing.calls, mismatch.Body.String())
	}
}

func TestDecodeA2ATaskRejectsUnsafeStoredHistory(t *testing.T) {
	valid := `{"id":"task","contextId":"context","status":{"state":"TASK_STATE_COMPLETED","timestamp":"2026-09-11T00:00:00Z"},"artifacts":[{"artifactId":"artifact","parts":[{"text":"ok"}]}],"history":[{"messageId":"user","contextId":"context","taskId":"task","role":"ROLE_USER","parts":[{"text":"hi"}]},{"messageId":"agent","contextId":"context","taskId":"task","role":"ROLE_AGENT","parts":[{"text":"ok"}]}]}`
	if _, err := decodeA2ATask([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(valid, `"role":"ROLE_USER"`, `"role":"ROLE_SYSTEM"`, 1),
		strings.Replace(valid, `"taskId":"task"`, `"taskId":"other"`, 1),
		strings.Replace(valid, `{"text":"hi"}`, `{"url":"https://example.test"}`, 1),
		strings.Replace(valid, `"timestamp":"2026-09-11T00:00:00Z"`, `"timestamp":"invalid"`, 1),
	} {
		if _, err := decodeA2ATask([]byte(invalid)); err == nil {
			t.Fatalf("accepted invalid task: %s", invalid)
		}
	}
}

func TestA2ATaskContinuationReportsOptimisticConflict(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, _, billing := a2aTestHandlerWithTasks(t, store)
	call := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
		request.Header.Set("A2A-Version", "1.0")
		request.Header.Set("Authorization", "Bearer key")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	created := call(`{"jsonrpc":"2.0","id":"create","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"first","role":"ROLE_USER","parts":[{"text":"one"}]}}}`)
	var envelope struct {
		Result struct {
			Task a2aTask `json:"task"`
		} `json:"result"`
	}
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &envelope) != nil {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	store.mu.Lock()
	store.updateErr = a2astate.ErrConflict
	store.mu.Unlock()
	response := call(`{"jsonrpc":"2.0","id":"continue","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"second","taskId":"` + envelope.Result.Task.ID + `","role":"ROLE_USER","parts":[{"text":"two"}]}}}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "Task changed concurrently") || billing.calls != 2 {
		t.Fatalf("status=%d billing=%d body=%s", response.Code, billing.calls, response.Body.String())
	}
}

func TestA2ATaskContinuationRejectsNonCompletedTaskBeforeBilling(t *testing.T) {
	store := &a2aMemoryTaskStore{tasks: map[string]a2astate.Task{}}
	router, _, billing := a2aTestHandlerWithTasks(t, store)
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"create","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"first","role":"ROLE_USER","parts":[{"text":"one"}]}}}`))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var envelope struct {
		Result struct {
			Task a2aTask `json:"task"`
		} `json:"result"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &envelope) != nil {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	store.mu.Lock()
	stored := store.tasks[envelope.Result.Task.ID]
	stored.State = "TASK_STATE_FAILED"
	var task a2aTask
	if json.Unmarshal(stored.Payload, &task) != nil {
		t.Fatal("decode stored task")
	}
	task.Status.State = stored.State
	stored.Payload, _ = json.Marshal(task)
	store.tasks[stored.ID] = stored
	store.mu.Unlock()
	request = httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(`{"jsonrpc":"2.0","id":"continue","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"second","taskId":"`+stored.ID+`","role":"ROLE_USER","parts":[{"text":"two"}]}}}`))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || billing.calls != 1 {
		t.Fatalf("status=%d billing=%d body=%s", response.Code, billing.calls, response.Body.String())
	}
}
