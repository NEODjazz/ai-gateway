package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/provider"
)

func TestAdminCredentialLifecycleNeverReturnsSecret(t *testing.T) {
	runtime := provider.New(provider.Config{CredentialEncryptionKey: []byte("test-master-key")})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))
	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/admin/v1/credentials", strings.NewReader(`{"id":"openai-prod","description":"production key","secret":"top-secret"}`)))
	if create.Code != http.StatusCreated || strings.Contains(create.Body.String(), "top-secret") {
		t.Fatalf("create exposed secret: status=%d body=%s", create.Code, create.Body.String())
	}
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/credentials", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "openai-prod") || strings.Contains(list.Body.String(), "top-secret") || strings.Contains(list.Body.String(), "ciphertext") {
		t.Fatalf("list exposed secret material: status=%d body=%s", list.Code, list.Body.String())
	}
	update := httptest.NewRecorder()
	handler.ServeHTTP(update, httptest.NewRequest(http.MethodPut, "/admin/v1/credentials/openai-prod", strings.NewReader(`{"description":"metadata only"}`)))
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), "metadata only") {
		t.Fatalf("update exposed secret: status=%d body=%s", update.Code, update.Body.String())
	}
	rotate := httptest.NewRecorder()
	handler.ServeHTTP(rotate, httptest.NewRequest(http.MethodPost, "/admin/v1/credentials/openai-prod/rotate", strings.NewReader(`{"secret":"new-secret"}`)))
	if rotate.Code != http.StatusOK || strings.Contains(rotate.Body.String(), "new-secret") {
		t.Fatalf("rotation exposed secret: status=%d body=%s", rotate.Code, rotate.Body.String())
	}
	remove := httptest.NewRecorder()
	handler.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/admin/v1/credentials/openai-prod", nil))
	if remove.Code != http.StatusNoContent {
		t.Fatalf("delete failed: status=%d body=%s", remove.Code, remove.Body.String())
	}
}
