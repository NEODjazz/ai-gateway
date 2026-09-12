package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/skillstate"
)

type countPolicy struct {
	deny  bool
	calls int
}

func (*countPolicy) Name() string   { return "dlp" }
func (*countPolicy) Required() bool { return false }
func (p *countPolicy) Handle(_ context.Context, req *modules.RequestContext) error {
	p.calls++
	if p.deny {
		return modules.ErrContentRejected
	}
	req.Request.Messages[0].Content = "sanitized"
	return nil
}

func countEndpointCall(handler http.Handler, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(body))
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("x-api-key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func TestCountEndpointAppliesPolicyWithoutBilling(t *testing.T) {
	for _, denied := range []bool{false, true} {
		var upstreamCalls, billingCalls atomic.Int64
		billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { billingCalls.Add(1); w.WriteHeader(500) }))
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstreamCalls.Add(1)
			var body struct {
				Model    string `json:"model"`
				Messages []struct {
					Content any `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			if body.Model != "upstream-model" || len(body.Messages) != 1 || body.Messages[0].Content != "sanitized" || r.Header.Get("x-api-key") != "provider-secret" {
				t.Errorf("policy or alias lost: %+v", body)
			}
			if r.URL.Path != "/v1/messages/count_tokens" {
				t.Error("generation invoked")
			}
			_, _ = w.Write([]byte(`{"input_tokens":23}`))
		}))
		policy := &countPolicy{deny: denied}
		billingModule := modules.NewRemoteBillingModule(true, billing.URL)
		router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "anthropic", BaseURL: upstream.URL, APIKey: "provider-secret", Models: []string{"model"}, ModelAliases: map[string]string{"model": "upstream-model"}}}, Modules: modules.NewPipeline([]modules.Module{billingModule, policy})})
		handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"model"}}}, billingModule}), router))
		response := countEndpointCall(handler, `{"model":"model","messages":[{"role":"user","content":"private"}]}`, "gateway-test-key")
		upstream.Close()
		billing.Close()
		wantCode, wantCalls := 200, int64(1)
		if denied {
			wantCode, wantCalls = 451, 0
		}
		if response.Code != wantCode || upstreamCalls.Load() != wantCalls || billingCalls.Load() != 0 || policy.calls != 1 {
			t.Fatalf("count flow: code=%d upstream=%d billing=%d policy=%d body=%s", response.Code, upstreamCalls.Load(), billingCalls.Load(), policy.calls, response.Body.String())
		}
		if !denied && !strings.Contains(response.Body.String(), `"input_tokens":23`) {
			t.Fatal(response.Body.String())
		}
	}
}
func TestCountEndpointChecksAuthModelAndTPM(t *testing.T) {
	for _, tc := range []struct {
		key    string
		policy accessPolicyModule
		code   int
	}{
		{policy: accessPolicyModule{models: []string{"*"}}, code: 401},
		{key: "gateway-test-key", policy: accessPolicyModule{models: []string{"other"}}, code: 403},
		{key: "gateway-test-key", policy: accessPolicyModule{models: []string{"*"}, tpm: 1}, code: 429},
	} {
		handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{tc.policy}}), &chatProvider{}))
		response := countEndpointCall(handler, `{"model":"model","messages":[{"role":"user","content":"enough input to exceed a single token"}]}`, tc.key)
		if response.Code != tc.code || !strings.Contains(response.Body.String(), `"type":"error"`) {
			t.Fatalf("access: %d %s", response.Code, response.Body.String())
		}
	}
}
func TestCountEndpointRejectsGenerationParameters(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), &chatProvider{}))
	for _, field := range []string{`"max_tokens":10`, `"stream":true`, `"temperature":0.1`, `"stop_sequences":["END"]`} {
		response := countEndpointCall(handler, `{"model":"model","messages":[{"role":"user","content":"hi"}],`+field+`}`, "")
		if response.Code != 400 {
			t.Fatalf("generation parameter accepted: %d", response.Code)
		}
	}
}

type countProviderSpy struct {
	chatProvider
	calls   int
	request modules.RequestContext
	result  provider.TokenCountResult
}

func (p *countProviderSpy) CountTokens(_ context.Context, request modules.RequestContext) (provider.TokenCountResult, error) {
	p.calls++
	p.request = request
	if p.result.InputTokens == 0 {
		return provider.TokenCountResult{InputTokens: 10}, nil
	}
	return p.result, nil
}

func TestCountEndpointPreservesContextManagement(t *testing.T) {
	original := 70
	counter := &countProviderSpy{result: provider.TokenCountResult{InputTokens: 25, OriginalInputTokens: &original}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}}}}), counter))
	response := countEndpointCall(handler, `{"model":"m","context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]},"messages":[{"role":"user","content":"hi"}]}`, "gateway-test-key")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"original_input_tokens":70`) || !strings.Contains(string(counter.request.Request.AnthropicContextManagement), "clear_tool_uses_20250919") {
		t.Fatalf("status=%d body=%s context=%s", response.Code, response.Body.String(), counter.request.Request.AnthropicContextManagement)
	}
}

func TestCountEndpointPreservesPDFDocument(t *testing.T) {
	counter := &countProviderSpy{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}}}}), counter))
	response := countEndpointCall(handler, `{"model":"m","messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjcKY29udGVudA=="},"title":"Report","context":"Audited","citations":{"enabled":true}}]}]}`, "gateway-test-key")
	attachments, err := openai.ChatFileAttachments(counter.request.Request.Messages)
	citations := counter.request.Request.Messages[0].AnthropicDocumentCitations
	metadata := counter.request.Request.Messages[0].AnthropicDocumentMetadata
	if response.Code != http.StatusOK || counter.calls != 1 || err != nil || len(attachments) != 1 || attachments[0].MediaType != "application/pdf" || len(citations) != 1 || !citations[0] || len(metadata) != 1 || metadata[0].Title != "Report" || metadata[0].Context != "Audited" {
		t.Fatalf("status=%d body=%s calls=%d attachments=%+v err=%v", response.Code, response.Body.String(), counter.calls, attachments, err)
	}
}

func TestCountEndpointPreservesPlainTextDocument(t *testing.T) {
	counter := &countProviderSpy{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}}}}), counter))
	response := countEndpointCall(handler, `{"model":"m","messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","media_type":"text/plain","data":"private@example.com"},"title":"Report","citations":{"enabled":true}}]}]}`, "gateway-test-key")
	message := counter.request.Request.Messages[0]
	if response.Code != http.StatusOK || counter.calls != 1 || !openai.HasChatTextDocuments(counter.request.Request) || openai.ContentText(message.Content) != "private@example.com" || len(message.AnthropicDocumentMetadata) != 1 || message.AnthropicDocumentMetadata[0].Title != "Report" || len(message.AnthropicDocumentCitations) != 1 || !message.AnthropicDocumentCitations[0] {
		t.Fatalf("status=%d body=%s calls=%d request=%+v", response.Code, response.Body.String(), counter.calls, counter.request.Request)
	}
}

func TestCountEndpointResolvesOwnedFileDocumentWithoutBilling(t *testing.T) {
	counter := &countProviderSpy{}
	billing := &lifecycleBillingModule{}
	identity := modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"}
	content := []byte("count this document")
	files := &memoryFileStore{files: map[string]filestate.File{"file_owned": {
		ID: "file_owned", OwnerKey: fileOwnerKey(identity), Filename: "notes.txt", Purpose: "user_data", ContentType: "text/plain", Bytes: int64(len(content)), Content: content,
	}}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: identity.CredentialID, user: identity.UserID}, billing, accessPolicyModule{models: []string{"*"}}}), counter).WithFileStore(files, FileRuntimeConfig{MaxBytes: 32 << 20, OwnerQuotaBytes: 64 << 20}))
	response := countEndpointCall(handler, `{"model":"m","messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file_owned"}}]}]}`, "gateway-test-key")
	if response.Code != http.StatusOK || counter.calls != 1 || billing.calls != 0 || !openai.HasChatTextDocuments(counter.request.Request) || openai.ContentText(counter.request.Request.Messages[0].Content) != string(content) {
		t.Fatalf("status=%d body=%s calls=%d request=%+v", response.Code, response.Body.String(), counter.calls, counter.request.Request)
	}
}

func TestCountEndpointFetchesURLPDFWithoutBilling(t *testing.T) {
	counter := &countProviderSpy{result: provider.TokenCountResult{InputTokens: 12}}
	billing := &lifecycleBillingModule{}
	h := NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}, billing, accessPolicyModule{models: []string{"*"}}}), counter)
	fetches := 0
	h.a2aHTTPClient = a2aHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		fetches++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/pdf"}}, Body: io.NopCloser(strings.NewReader("%PDF-1.7\ncontent"))}, nil
	})
	handler := Routes(h)
	response := countEndpointCall(handler, `{"model":"m","messages":[{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://documents.example/report.pdf"}}]}]}`, "gateway-test-key")
	attachments, err := openai.ChatFileAttachments(counter.request.Request.Messages)
	if response.Code != http.StatusOK || fetches != 1 || counter.calls != 1 || billing.calls != 0 || err != nil || len(attachments) != 1 || !strings.Contains(response.Body.String(), `"input_tokens":12`) {
		t.Fatalf("status=%d fetches=%d calls=%d billing=%d attachments=%+v err=%v body=%s", response.Code, fetches, counter.calls, billing.calls, attachments, err, response.Body.String())
	}
}

func TestCountEndpointFetchesURLImageWithoutBilling(t *testing.T) {
	counter := &countProviderSpy{result: provider.TokenCountResult{InputTokens: 9}}
	billing := &lifecycleBillingModule{}
	h := NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}, billing, accessPolicyModule{models: []string{"*"}}}), counter)
	h.a2aHTTPClient = a2aHTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\nimage"))}, nil
	})
	response := countEndpointCall(Routes(h), `{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://images.example/chart.png"}}]}]}`, "gateway-test-key")
	images, err := openai.ChatImageAttachments(counter.request.Request.Messages)
	if response.Code != http.StatusOK || counter.calls != 1 || billing.calls != 0 || err != nil || len(images) != 1 || !strings.Contains(response.Body.String(), `"input_tokens":9`) {
		t.Fatalf("status=%d calls=%d billing=%d images=%+v err=%v body=%s", response.Code, counter.calls, billing.calls, images, err, response.Body.String())
	}
}

func TestCountEndpointResolvesOwnedFileImageWithoutBilling(t *testing.T) {
	counter := &countProviderSpy{result: provider.TokenCountResult{InputTokens: 7}}
	billing := &lifecycleBillingModule{}
	identity := modules.RequestContext{CredentialID: "credential", UserID: "user"}
	image := []byte("\x89PNG\r\n\x1a\nimage")
	files := &memoryFileStore{files: map[string]filestate.File{"file_image": {
		ID: "file_image", OwnerKey: fileOwnerKey(identity), Filename: "chart.png", Purpose: "user_data", ContentType: "image/png", Bytes: int64(len(image)), Content: image,
	}}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: identity.CredentialID, user: identity.UserID}, billing, accessPolicyModule{models: []string{"*"}}}), counter).WithFileStore(files, FileRuntimeConfig{MaxBytes: 32 << 20, OwnerQuotaBytes: 64 << 20}))
	response := countEndpointCall(handler, `{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"file_image"}}]}]}`, "gateway-test-key")
	images, err := openai.ChatImageAttachments(counter.request.Request.Messages)
	if response.Code != http.StatusOK || counter.calls != 1 || billing.calls != 0 || err != nil || len(images) != 1 || !strings.Contains(response.Body.String(), `"input_tokens":7`) {
		t.Fatalf("status=%d calls=%d billing=%d images=%+v err=%v body=%s", response.Code, counter.calls, billing.calls, images, err, response.Body.String())
	}
}
func TestCountEndpointEnforcesToolACLAndSharedRPM(t *testing.T) {
	counter := &countProviderSpy{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}, tools: []string{"safe"}, rpm: 1}}}), counter))
	denied := countEndpointCall(handler, `{"model":"m","tools":[{"name":"denied","input_schema":{}}],"messages":[{"role":"user","content":"hi"}]}`, "gateway-test-key")
	if denied.Code != 403 || counter.calls != 0 {
		t.Fatal("tool ACL bypassed")
	}
	for _, want := range []int{200, 429} {
		response := countEndpointCall(handler, `{"model":"m","messages":[{"role":"assistant","content":"prefix"}]}`, "gateway-test-key")
		if response.Code != want {
			t.Fatalf("RPM/prefill: %d %s", response.Code, response.Body.String())
		}
	}
	if counter.calls != 1 {
		t.Fatal("limited count reached provider")
	}
}

func TestCountEndpointPreservesThinkingConfiguration(t *testing.T) {
	counter := &countProviderSpy{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}}}}), counter))
	response := countEndpointCall(handler, `{"model":"m","thinking":{"type":"enabled","budget_tokens":2048,"display":"summarized"},"messages":[{"role":"user","content":"hi"}]}`, "gateway-test-key")
	thinking := counter.request.Request.AnthropicThinking
	if response.Code != http.StatusOK || counter.calls != 1 || thinking == nil || thinking.Type != "enabled" || thinking.BudgetTokens == nil || *thinking.BudgetTokens != 2048 || thinking.Display != "summarized" {
		t.Fatalf("status=%d body=%s calls=%d thinking=%+v", response.Code, response.Body.String(), counter.calls, thinking)
	}
}

func TestCountEndpointPreservesInferenceGeo(t *testing.T) {
	counter := &countProviderSpy{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}}}}), counter))
	response := countEndpointCall(handler, `{"model":"m","inference_geo":"us","cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[{"role":"user","content":"hi"}]}`, "gateway-test-key")
	control := counter.request.Request.AnthropicCacheControl
	if response.Code != http.StatusOK || counter.calls != 1 || counter.request.Request.AnthropicInferenceGeo != "us" || control == nil || control.TTL != "1h" {
		t.Fatalf("status=%d body=%s calls=%d geo=%q cache=%+v", response.Code, response.Body.String(), counter.calls, counter.request.Request.AnthropicInferenceGeo, control)
	}
}

func TestCountEndpointIncludesOwnedSkillExecutionContext(t *testing.T) {
	owner := skillOwnerKey(modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"})
	store := &memorySkillStore{
		items: map[string]skillstate.Ownership{
			"skill_owned": {SkillID: "skill_owned", OwnerKey: owner, EndpointID: "skills-endpoint"},
		},
		executions: map[string]skillstate.Execution{
			"container_1": {ContainerID: "container_1", OwnerKey: owner, EndpointID: "skills-endpoint", ExpiresAt: time.Now().Add(time.Hour)},
		},
	}
	counter := &countProviderSpy{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}, tools: []string{"skill:skill_owned", "code_execution"}}}}), counter).WithSkillStore(store))
	response := countEndpointCall(handler, `{"model":"m","container":{"id":"container_1","skills":[{"type":"custom","skill_id":"skill_owned","version":"v1"}]},"tools":[{"type":"code_execution_20250825","name":"code_execution"}],"messages":[{"role":"user","content":"count this"}]}`, "gateway-test-key")
	request := counter.request.Request
	if response.Code != http.StatusOK || counter.calls != 1 || request.Provider != "skills-endpoint" || request.AnthropicContainerID != "container_1" || len(request.AnthropicSkills) != 1 || !request.AnthropicCodeExecution || request.NativeInputTokens == 0 {
		t.Fatalf("status=%d body=%s calls=%d request=%+v", response.Code, response.Body.String(), counter.calls, request)
	}
}

func TestCountEndpointUsesNativeGeminiCounter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-upstream:countTokens" || r.Header.Get("x-goog-api-key") != "upstream-key" || r.Header.Get("Authorization") != "" {
			t.Error("Gemini counter routing/auth lost")
		}
		_, _ = w.Write([]byte(`{"totalTokens":37}`))
	}))
	defer server.Close()
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "gemini", Type: "gemini", BaseURL: server.URL, APIKey: "upstream-key", Models: []string{"public-model"}, ModelAliases: map[string]string{"public-model": "gemini-upstream"}}}})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public-model"}}}}), router))
	response := countEndpointCall(handler, `{"model":"public-model","messages":[{"role":"user","content":"hi"}]}`, "gateway-test-key")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"input_tokens":37`) {
		t.Fatalf("Gemini public count: %d %s", response.Code, response.Body.String())
	}
}

func TestCountEndpointUsesNativeBedrockCounter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/bedrock-upstream/count-tokens" || r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Errorf("Bedrock counter routing/auth lost: path=%q headers=%v", r.URL.Path, r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["input"] == nil {
			t.Errorf("invalid Bedrock count body: %#v err=%v", body, err)
		}
		_, _ = w.Write([]byte(`{"inputTokens":41}`))
	}))
	defer server.Close()
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "bedrock", Type: "bedrock", BaseURL: server.URL, APIKey: "upstream-key", Models: []string{"public-model"}, ModelAliases: map[string]string{"public-model": "bedrock-upstream"}}}})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public-model"}}}}), router))
	response := countEndpointCall(handler, `{"model":"public-model","messages":[{"role":"user","content":"hi"}]}`, "gateway-test-key")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"input_tokens":41`) {
		t.Fatalf("Bedrock public count: %d %s", response.Code, response.Body.String())
	}
}
