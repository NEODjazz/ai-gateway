package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func TestGenerateCountTokensNativeContext(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1beta/models/gemini-test:countTokens" || r.Header.Get("x-goog-api-key") != "provider-key" {
			t.Error("incorrect native routing")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		nested, ok := body["generateContentRequest"].(map[string]any)
		if !ok || nested["model"] != "models/gemini-test" || nested["systemInstruction"] == nil || nested["tools"] == nil {
			t.Errorf("context lost: %#v", body)
		}
		_, _ = w.Write([]byte(`{"totalTokens":42}`))
	}))
	defer upstream.Close()
	billing := &messagesUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "gemini", BaseURL: upstream.URL, APIKey: "provider-key", Models: []string{"m"}, ModelAliases: map[string]string{"m": "gemini-test"}}}, Modules: modules.NewPipeline([]modules.Module{billing})})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}, tools: []string{"weather"}}}, billing}), router))
	response := generateCall(handler, "/v1beta/models/m:countTokens", `{"generateContentRequest":{"model":"models/m","contents":[{"parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"system context"}]},"tools":[{"functionDeclarations":[{"name":"weather","parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"}}}}]}]}}`, "gateway-test-key")
	if response.Code != 200 || strings.TrimSpace(response.Body.String()) != `{"totalTokens":42}` || calls != 1 || billing.calls != 0 || response.Header().Get("X-Execution-ID") == "" {
		t.Fatalf("count response: %d %s upstream=%d billing=%d", response.Code, response.Body.String(), calls, billing.calls)
	}
}

func TestGenerateCountTokensResolvesOwnedFileData(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	content := []byte("notes")
	files := &memoryFileStore{files: map[string]filestate.File{"file_text": {ID: "file_text", OwnerKey: owner, Filename: "notes.txt", Purpose: "user_data", ContentType: "text/plain", Bytes: int64(len(content)), Content: content}}}
	counter := &countProviderSpy{result: provider.TokenCountResult{InputTokens: 7}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), counter).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 1 << 20, OwnerQuotaBytes: 4 << 20}))
	response := generateCall(handler, "/v1beta/models/m:countTokens", `{"contents":[{"parts":[{"fileData":{"mimeType":"text/plain","fileUri":"file_text"}}]}]}`, "gateway-test-key")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"totalTokens":7}` || counter.calls != 1 || !openai.HasChatTextDocuments(counter.request.Request) || openai.HasChatResolvableReferences(counter.request.Request) {
		t.Fatalf("status=%d calls=%d request=%+v body=%s", response.Code, counter.calls, counter.request.Request, response.Body.String())
	}
}
func TestGenerateCountTokensValidation(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"generateContentRequest":{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{}}}`,
		`{"generateContentRequest":{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":null}}`,
		`{"contents":[],"generateContentRequest":{"contents":[{"parts":[{"text":"hi"}]}]}}`,
		`{"generateContentRequest":{"model":"models/other","contents":[{"parts":[{"text":"hi"}]}]}}`,
		`{"generateContentRequest":{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":1}}}`,
		`{"generateContentRequest":{"contents":[{"parts":[{"text":"hi"}]}],"cachedContent":"private"}}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"tools":[]}`,
	} {
		spy := &countProviderSpy{}
		response := generateCall(Routes(NewHandler(modules.NewPipeline(nil), spy)), "/v1beta/models/m:countTokens", body, "")
		if response.Code != 400 || spy.calls != 0 || !strings.Contains(response.Body.String(), `"status":`) {
			t.Fatalf("accepted %s: %d %s", body, response.Code, response.Body.String())
		}
	}
}
func TestGenerateCountTokensAccessAndSharedQuota(t *testing.T) {
	spy := &countProviderSpy{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}, tools: []string{"safe"}, rpm: 1}}}), spy))
	body := `{"contents":[{"parts":[{"text":"hi"}]}]}`
	for _, tc := range []struct {
		path, body, key string
		code            int
	}{
		{"/v1beta/models/m:countTokens", body, "", 401},
		{"/v1beta/models/other:countTokens", body, "gateway-test-key", 403},
		{"/v1beta/models/m:countTokens", `{"generateContentRequest":{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"denied"}]}]}}`, "gateway-test-key", 403},
		{"/v1beta/models/m:countTokens", body, "gateway-test-key", 200},
		{"/v1beta/models/m:countTokens", body, "gateway-test-key", 429},
	} {
		response := generateCall(handler, tc.path, tc.body, tc.key)
		if response.Code != tc.code {
			t.Fatalf("access: want %d got %d %s", tc.code, response.Code, response.Body.String())
		}
	}
	if spy.calls != 1 {
		t.Fatalf("counter calls=%d", spy.calls)
	}
	// Both native protocols share the same identity window.
	response := countEndpointCall(handler, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, "gateway-test-key")
	if response.Code != 429 {
		t.Fatalf("cross-protocol quota bypass: %d", response.Code)
	}
}
