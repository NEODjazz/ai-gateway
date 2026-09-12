package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/provider"
)

func TestAdminProviderLifecycle(t *testing.T) {
	runtime := provider.New(provider.Config{})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))

	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/admin/v1/providers", strings.NewReader(`{"id":"local","type":"ollama","base_url":"http://127.0.0.1:11434","rate_limit_rpm":100,"rate_limit_tpm":50000,"enabled":true}`)))
	if create.Code != http.StatusCreated || !strings.Contains(create.Body.String(), `"id":"local"`) || !strings.Contains(create.Body.String(), `"rate_limit_rpm":100`) || !strings.Contains(create.Body.String(), `"rate_limit_tpm":50000`) {
		t.Fatalf("create failed: status=%d body=%s", create.Code, create.Body.String())
	}

	update := httptest.NewRecorder()
	handler.ServeHTTP(update, httptest.NewRequest(http.MethodPut, "/admin/v1/providers/local", strings.NewReader(`{"type":"ollama","base_url":"http://ollama:11434","rate_limit_rpm":200,"rate_limit_tpm":75000,"enabled":false}`)))
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"enabled":false`) || !strings.Contains(update.Body.String(), `"rate_limit_rpm":200`) {
		t.Fatalf("update failed: status=%d body=%s", update.Code, update.Body.String())
	}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/providers", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `http://ollama:11434`) {
		t.Fatalf("list failed: status=%d body=%s", list.Code, list.Body.String())
	}

	remove := httptest.NewRecorder()
	handler.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/admin/v1/providers/local", nil))
	if remove.Code != http.StatusNoContent {
		t.Fatalf("delete failed: status=%d body=%s", remove.Code, remove.Body.String())
	}
}

func TestAdminProviderRejectsCredentialInPayload(t *testing.T) {
	runtime := provider.New(provider.Config{})
	response := httptest.NewRecorder()
	Routes(NewHandler(modulesPipeline("admin"), runtime)).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/admin/v1/providers", strings.NewReader(`{"id":"unsafe","type":"openai","base_url":"https://api.openai.com","api_key":"secret"}`)))
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("unsafe payload accepted or reflected: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAdminListsProviderCapabilityProfiles(t *testing.T) {
	runtime := provider.New(provider.Config{})
	response := httptest.NewRecorder()
	Routes(NewHandler(modulesPipeline("admin"), runtime)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/provider-capabilities", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"type":"voyage","operations":["embeddings","rerank"],"capabilities":["embeddings","rerank"],"auth_types":[],"chat_parameters":{"supported_options":[],"reasoning_effort":[],"logprobs":[],"service_tier":[]},"response_parameters":{"supported_options":[],"reasoning_effort":[],"service_tier":[]}`) || !strings.Contains(response.Body.String(), `"web_fetch"`) || !strings.Contains(response.Body.String(), `"auth_types":["api_key","gcp_adc"]`) || !strings.Contains(response.Body.String(), `"count_tokens"`) || !strings.Contains(response.Body.String(), `"service_tier":["auto","standard_only"]`) || !strings.Contains(response.Body.String(), `"supported_options":["metadata","modalities","audio"`) || !strings.Contains(response.Body.String(), `"response_parameters":{"supported_options":["metadata","top_logprobs","truncation"`) {
		t.Fatalf("unexpected capability profiles: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAdminProviderUpdateReportsIncompatibleDeployments(t *testing.T) {
	runtime := provider.New(provider.Config{})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/admin/v1/providers", strings.NewReader(`{"id":"managed","type":"openai-compatible","base_url":"https://provider.example","enabled":true}`)),
		httptest.NewRequest(http.MethodPost, "/admin/v1/model-deployments", strings.NewReader(`{"id":"images","provider_id":"managed","models":["image-model"],"capabilities":["image_generation"],"enabled":true}`)),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("setup failed: %d %s", response.Code, response.Body.String())
		}
	}
	update := httptest.NewRecorder()
	handler.ServeHTTP(update, httptest.NewRequest(http.MethodPut, "/admin/v1/providers/managed", strings.NewReader(`{"type":"voyage","base_url":"https://provider.example","enabled":true}`)))
	if update.Code != http.StatusBadRequest || !strings.Contains(update.Body.String(), `"code":"unsupported_provider_capability"`) || !strings.Contains(update.Body.String(), "voyage does not support image_generation") {
		t.Fatalf("incompatible update was not explained: %d %s", update.Code, update.Body.String())
	}
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/providers", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"type":"openai-compatible"`) || strings.Contains(list.Body.String(), `"type":"voyage"`) {
		t.Fatalf("failed update was not rolled back: %d %s", list.Code, list.Body.String())
	}
}
