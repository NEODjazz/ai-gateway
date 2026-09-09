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
