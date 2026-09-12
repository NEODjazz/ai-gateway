package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/skillstate"
)

type memorySkillStore struct {
	mu         sync.Mutex
	items      map[string]skillstate.Ownership
	executions map[string]skillstate.Execution
}

func (s *memorySkillStore) SaveSkillExecution(_ context.Context, execution skillstate.Execution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.executions == nil {
		s.executions = map[string]skillstate.Execution{}
	}
	if current, found := s.executions[execution.ContainerID]; found && (current.OwnerKey != execution.OwnerKey || current.EndpointID != execution.EndpointID) {
		return skillstate.ErrConflict
	}
	s.executions[execution.ContainerID] = execution
	return nil
}

func (s *memorySkillStore) ResolveSkillExecution(_ context.Context, owner, id string) (skillstate.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	execution, found := s.executions[id]
	if !found || execution.OwnerKey != owner || !execution.ExpiresAt.After(time.Now().UTC()) {
		return skillstate.Execution{}, skillstate.ErrNotFound
	}
	return execution, nil
}

func (s *memorySkillStore) ClaimSkill(_ context.Context, item skillstate.Ownership) (skillstate.Ownership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.items[item.SkillID]; found {
		return skillstate.Ownership{}, skillstate.ErrConflict
	}
	item.CreatedAt = time.Unix(1, 0)
	s.items[item.SkillID] = item
	return item, nil
}
func (s *memorySkillStore) ResolveSkill(_ context.Context, owner, id string) (skillstate.Ownership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, found := s.items[id]
	if !found || item.OwnerKey != owner {
		return skillstate.Ownership{}, skillstate.ErrNotFound
	}
	return item, nil
}
func (s *memorySkillStore) OwnedSkills(_ context.Context, owner, endpoint string, ids []string) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[string]bool{}
	for _, id := range ids {
		item, found := s.items[id]
		if found && item.OwnerKey == owner && item.EndpointID == endpoint {
			result[id] = true
		}
	}
	return result, nil
}
func (s *memorySkillStore) DeleteSkill(_ context.Context, owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, found := s.items[id]
	if !found || item.OwnerKey != owner {
		return skillstate.ErrNotFound
	}
	delete(s.items, id)
	return nil
}

func TestMessagesExecutesOwnedCustomSkillOnBoundDeployment(t *testing.T) {
	owner := skillOwnerKey(modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"})
	store := &memorySkillStore{items: map[string]skillstate.Ownership{
		"skill_owned": {SkillID: "skill_owned", OwnerKey: owner, EndpointID: "skills-endpoint"},
	}}
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{
		ID: "msg-skill", Model: "model", NativeContainer: json.RawMessage(`{"id":"container_1","expires_at":"2099-09-12T14:00:00Z","skills":[{"type":"custom","skill_id":"skill_owned","version":"v1"}]}`),
		Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}},
		Usage:   openai.Usage{PromptTokens: 9, CompletionTokens: 2, TotalTokens: 11},
	}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}, tools: []string{"skill:skill_owned", "code_execution"}}}}), upstream).WithSkillStore(store))

	response := nativeMessageCall(handler, `{"model":"model","max_tokens":20,"container":{"skills":[{"type":"custom","skill_id":"skill_owned","version":"v1"}]},"tools":[{"type":"code_execution_20250825","name":"code_execution"}],"messages":[{"role":"user","content":"run it"}]}`, "gateway-test-key")
	if response.Code != http.StatusOK || upstream.calls != 1 {
		t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), upstream.calls)
	}
	request := upstream.request.Request
	if request.Provider != "skills-endpoint" || len(request.AnthropicSkills) != 1 || request.AnthropicSkills[0].Version != "v1" || request.NativeInputTokens == 0 {
		t.Fatalf("skill request was not bound and accounted: %+v", request)
	}
	if !strings.Contains(response.Body.String(), `"container":{"expires_at":"2099-09-12T14:00:00Z","id":"container_1"`) || !strings.Contains(response.Body.String(), `"skill_id":"skill_owned"`) {
		t.Fatalf("native container was not preserved: %s", response.Body.String())
	}
	continued := nativeMessageCall(handler, `{"model":"model","max_tokens":20,"container":{"id":"container_1","skills":[{"type":"custom","skill_id":"skill_owned","version":"v1"}]},"tools":[{"type":"code_execution_20250825","name":"code_execution"}],"messages":[{"role":"user","content":"continue"}]}`, "gateway-test-key")
	if continued.Code != http.StatusOK || upstream.calls != 2 || upstream.request.Request.AnthropicContainerID != "container_1" || upstream.request.Request.Provider != "skills-endpoint" {
		t.Fatalf("continuation status=%d body=%s calls=%d request=%+v", continued.Code, continued.Body.String(), upstream.calls, upstream.request.Request)
	}
	if _, err := store.ResolveSkillExecution(t.Context(), "other-owner", "container_1"); !errors.Is(err, skillstate.ErrNotFound) {
		t.Fatalf("cross-owner continuation resolution=%v", err)
	}
}

func TestMessagesStreamsOwnedCustomSkillAfterDurableContainerBinding(t *testing.T) {
	owner := skillOwnerKey(modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"})
	store := &memorySkillStore{items: map[string]skillstate.Ownership{
		"skill_owned": {SkillID: "skill_owned", OwnerKey: owner, EndpointID: "skills-endpoint"},
	}}
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{
		ID: "msg-skill-stream", Model: "model", NativeContainer: json.RawMessage(`{"id":"container_stream","expires_at":"2099-09-12T14:00:00Z","skills":[{"type":"custom","skill_id":"skill_owned","version":"v1"}]}`),
		Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}},
		Usage:   openai.Usage{PromptTokens: 9, CompletionTokens: 2, TotalTokens: 11},
	}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}, tools: []string{"skill:skill_owned", "code_execution"}}}}), upstream).WithSkillStore(store))

	response := nativeMessageCall(handler, `{"model":"model","max_tokens":20,"stream":true,"container":{"skills":[{"type":"custom","skill_id":"skill_owned","version":"v1"}]},"tools":[{"type":"code_execution_20250825","name":"code_execution"}],"messages":[{"role":"user","content":"run it"}]}`, "gateway-test-key")
	if response.Code != http.StatusOK || upstream.calls != 1 || !strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d body=%s calls=%d content-type=%s", response.Code, response.Body.String(), upstream.calls, response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	if !strings.Contains(body, `"type":"message_start"`) || !strings.Contains(body, `"container":{"expires_at":"2099-09-12T14:00:00Z","id":"container_stream"`) || !strings.Contains(body, `"type":"message_stop"`) {
		t.Fatalf("streamed container lifecycle missing: %s", body)
	}
	if execution, err := store.ResolveSkillExecution(t.Context(), owner, "container_stream"); err != nil || execution.EndpointID != "skills-endpoint" {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
	if upstream.request.Request.Stream {
		t.Fatal("skill stream reached provider before durable container validation")
	}
}

func TestMessagesSkillExecutionFailsClosed(t *testing.T) {
	owner := skillOwnerKey(modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"})
	store := &memorySkillStore{items: map[string]skillstate.Ownership{
		"skill_a": {SkillID: "skill_a", OwnerKey: owner, EndpointID: "endpoint-a"},
		"skill_b": {SkillID: "skill_b", OwnerKey: owner, EndpointID: "endpoint-b"},
	}}
	for _, test := range []struct {
		name, skills, grants string
		status               int
	}{
		{name: "foreign", skills: `[{"type":"custom","skill_id":"foreign"}]`, grants: "*", status: http.StatusNotFound},
		{name: "different deployments", skills: `[{"type":"custom","skill_id":"skill_a"},{"type":"custom","skill_id":"skill_b"}]`, grants: "*", status: http.StatusBadRequest},
		{name: "tool policy", skills: `[{"type":"custom","skill_id":"skill_a"}]`, grants: "skill:other", status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &fallbackChatProvider{}
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}, tools: []string{test.grants}}}}), upstream).WithSkillStore(store))
			response := nativeMessageCall(handler, `{"model":"model","max_tokens":20,"container":{"skills":`+test.skills+`},"tools":[{"type":"code_execution_20250825","name":"code_execution"}],"messages":[{"role":"user","content":"run"}]}`, "gateway-test-key")
			if response.Code != test.status || upstream.calls != 0 {
				t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), upstream.calls)
			}
		})
	}
}

func TestMessagesSkillExecutionRejectsUnknownContainerAndInvalidProviderContainer(t *testing.T) {
	owner := skillOwnerKey(modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"})
	store := &memorySkillStore{items: map[string]skillstate.Ownership{
		"skill_owned": {SkillID: "skill_owned", OwnerKey: owner, EndpointID: "endpoint-a"},
	}}
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{
		ID: "msg-skill", NativeContainer: json.RawMessage(`{"id":"container_new"}`),
		Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}},
	}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}, tools: []string{"*"}}}}), upstream).WithSkillStore(store))

	unknown := nativeMessageCall(handler, `{"model":"model","max_tokens":20,"container":{"id":"container_unknown","skills":[{"type":"custom","skill_id":"skill_owned"}]},"tools":[{"type":"code_execution_20250825","name":"code_execution"}],"messages":[{"role":"user","content":"continue"}]}`, "gateway-test-key")
	if unknown.Code != http.StatusNotFound || upstream.calls != 0 {
		t.Fatalf("unknown container status=%d body=%s calls=%d", unknown.Code, unknown.Body.String(), upstream.calls)
	}
	invalid := nativeMessageCall(handler, `{"model":"model","max_tokens":20,"container":{"skills":[{"type":"custom","skill_id":"skill_owned"}]},"tools":[{"type":"code_execution_20250825","name":"code_execution"}],"messages":[{"role":"user","content":"run"}]}`, "gateway-test-key")
	if invalid.Code != http.StatusBadGateway || !strings.Contains(invalid.Body.String(), `"type":"error"`) || upstream.calls != 1 {
		t.Fatalf("invalid provider container status=%d body=%s calls=%d", invalid.Code, invalid.Body.String(), upstream.calls)
	}
	invalidStream := nativeMessageCall(handler, `{"model":"model","max_tokens":20,"stream":true,"container":{"skills":[{"type":"custom","skill_id":"skill_owned"}]},"tools":[{"type":"code_execution_20250825","name":"code_execution"}],"messages":[{"role":"user","content":"run"}]}`, "gateway-test-key")
	if invalidStream.Code != http.StatusBadGateway || !strings.Contains(invalidStream.Body.String(), `"type":"error"`) || strings.Contains(invalidStream.Header().Get("Content-Type"), "text/event-stream") || upstream.calls != 2 {
		t.Fatalf("invalid streamed provider container status=%d body=%s calls=%d content-type=%s", invalidStream.Code, invalidStream.Body.String(), upstream.calls, invalidStream.Header().Get("Content-Type"))
	}
}

type skillGatewayProvider struct {
	*chatProvider
	calls []provider.SkillRequest
}

func (p *skillGatewayProvider) ExecuteSkillRequest(_ context.Context, _ modules.RequestContext, endpoint string, request provider.SkillRequest) (provider.SkillResponse, string, error) {
	p.calls = append(p.calls, request)
	if endpoint != "" && endpoint != "skills-endpoint" {
		return provider.SkillResponse{}, "", errors.New("wrong endpoint")
	}
	body := `{"id":"skill_owned","source":{"type":"custom"},"type":"skill"}`
	if request.Method == http.MethodGet && request.Path == "skills" {
		body = `{"data":[{"id":"skill_owned","source":{"type":"custom"}},{"id":"skill_foreign","source":{"type":"custom"}},{"id":"skill_plugin","source":{"type":"plugin"}},{"id":"skill_builtin","source":{"type":"anthropic"}}],"has_more":false,"first_id":"skill_owned","last_id":"skill_builtin"}`
	}
	if strings.Contains(request.Path, "skill_foreign") {
		body = `{"id":"skill_foreign","source":{"type":"custom"},"type":"skill"}`
	}
	if request.Method == http.MethodGet && strings.HasSuffix(request.Path, "/content") {
		return provider.SkillResponse{StatusCode: http.StatusOK, ContentType: "application/zip", Body: []byte("archive")}, "skills-endpoint", nil
	}
	if request.Method == http.MethodDelete {
		body = `{"id":"skill_owned","type":"skill_deleted"}`
	}
	return provider.SkillResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(body)}, "skills-endpoint", nil
}

func TestSkillUpdateAndContentUseOwnedEndpoint(t *testing.T) {
	owner := skillOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memorySkillStore{items: map[string]skillstate.Ownership{
		"skill_owned": {SkillID: "skill_owned", OwnerKey: owner, EndpointID: "skills-endpoint"},
	}}
	upstream := &skillGatewayProvider{chatProvider: &chatProvider{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), upstream).WithSkillStore(store))

	request := httptest.NewRequest(http.MethodPost, "/v1/skills/skill_owned", strings.NewReader(`{"default_version":"v2"}`))
	request.Header.Set("Authorization", "Bearer key")
	request.Header.Set("Content-Type", "application/json")
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, request)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	last := upstream.calls[len(upstream.calls)-1]
	if last.Method != http.MethodPost || last.Path != "skills/skill_owned" || last.ContentType != "application/json" || string(last.Body) != `{"default_version":"v2"}` {
		t.Fatalf("update transport=%+v", last)
	}

	content := authorizedSkillRequest(handler, http.MethodGet, "/v1/skills/skill_owned/content", "")
	if content.Code != http.StatusOK || content.Header().Get("Content-Type") != "application/zip" || content.Body.String() != "archive" {
		t.Fatalf("content status=%d headers=%v body=%q", content.Code, content.Header(), content.Body.String())
	}
	last = upstream.calls[len(upstream.calls)-1]
	if last.Method != http.MethodGet || last.Path != "skills/skill_owned/content" {
		t.Fatalf("content transport=%+v", last)
	}
}

func TestSkillUpdateRejectsForeignAndInvalidRequests(t *testing.T) {
	owner := skillOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memorySkillStore{items: map[string]skillstate.Ownership{
		"skill_owned": {SkillID: "skill_owned", OwnerKey: owner, EndpointID: "skills-endpoint"},
	}}
	upstream := &skillGatewayProvider{chatProvider: &chatProvider{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), upstream).WithSkillStore(store))

	for _, test := range []struct {
		path string
		body string
		want int
	}{
		{path: "/v1/skills/skill_foreign", body: `{"default_version":"v2"}`, want: http.StatusNotFound},
		{path: "/v1/skills/skill_owned", body: `{"default_version":"bad/version"}`, want: http.StatusBadRequest},
		{path: "/v1/skills/skill_owned", body: `{"default_version":"v2","unknown":true}`, want: http.StatusBadRequest},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer key")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("path=%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
	if len(upstream.calls) != 0 {
		t.Fatalf("provider called for rejected updates: %+v", upstream.calls)
	}
}

func TestSkillsCreateListIsolationAndDelete(t *testing.T) {
	store := &memorySkillStore{items: map[string]skillstate.Ownership{}}
	upstream := &skillGatewayProvider{chatProvider: &chatProvider{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), upstream).WithSkillStore(store))
	createRequest := httptest.NewRequest(http.MethodPost, "/v1/skills", strings.NewReader("--test--\r\n"))
	createRequest.Header.Set("Authorization", "Bearer key")
	createRequest.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, createRequest)
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	owner := skillOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	if item, err := store.ResolveSkill(t.Context(), owner, "skill_owned"); err != nil || item.EndpointID != "skills-endpoint" {
		t.Fatalf("ownership=%+v err=%v", item, err)
	}

	list := authorizedSkillRequest(handler, http.MethodGet, "/v1/skills?limit=20", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "skill_owned") || !strings.Contains(list.Body.String(), "skill_builtin") || strings.Contains(list.Body.String(), "skill_foreign") || strings.Contains(list.Body.String(), "skill_plugin") {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	foreign := authorizedSkillRequest(handler, http.MethodGet, "/v1/skills/skill_foreign", "")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	deleted := authorizedSkillRequest(handler, http.MethodDelete, "/v1/skills/skill_owned", "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, err := store.ResolveSkill(t.Context(), owner, "skill_owned"); !errors.Is(err, skillstate.ErrNotFound) {
		t.Fatalf("deleted ownership error=%v", err)
	}
}

func TestSkillsFailClosedWithoutOwnershipStoreAndRejectBadInputs(t *testing.T) {
	upstream := &skillGatewayProvider{chatProvider: &chatProvider{}}
	missing := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), upstream))
	if response := authorizedSkillRequest(missing, http.MethodGet, "/v1/skills", ""); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing store status=%d", response.Code)
	}
	configured := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), upstream).WithSkillStore(&memorySkillStore{items: map[string]skillstate.Ownership{}}))
	for _, path := range []string{"/v1/skills?limit=0", "/v1/skills?unknown=x", "/v1/skills/bad%2Fid"} {
		if response := authorizedSkillRequest(configured, http.MethodGet, path, ""); response.Code != http.StatusBadRequest {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/skills", strings.NewReader("payload"))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	configured.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("content type status=%d body=%s", response.Code, response.Body.String())
	}
}

func authorizedSkillRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
