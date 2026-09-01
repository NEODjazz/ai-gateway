package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/provider"
)

func TestAdminDiscoversModelsAndTestsConnection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer discovery-secret" {
			t.Fatalf("unexpected discovery request: path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-b"},{"id":"model-a"}]}`))
	}))
	defer upstream.Close()
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "remote", Type: "openai-compatible", BaseURL: upstream.URL},
		{Name: "other", Type: "openai-compatible", BaseURL: upstream.URL},
	}})
	handler := Routes(NewHandler(modulesPipeline("admin"), runtime))
	credential := httptest.NewRecorder()
	handler.ServeHTTP(credential, httptest.NewRequest(http.MethodPost, "/admin/v1/credentials", strings.NewReader(`{"id":"remote-key","provider_id":"remote","secret":"discovery-secret"}`)))
	if credential.Code != http.StatusCreated {
		t.Fatalf("credential create failed: %d %s", credential.Code, credential.Body.String())
	}
	discover := httptest.NewRecorder()
	handler.ServeHTTP(discover, httptest.NewRequest(http.MethodPost, "/admin/v1/providers/remote/discover-models", strings.NewReader(`{"credential_id":"remote-key"}`)))
	if discover.Code != http.StatusOK || !strings.Contains(discover.Body.String(), `"id":"model-a"`) {
		t.Fatalf("discovery failed: %d %s", discover.Code, discover.Body.String())
	}
	probe := httptest.NewRecorder()
	handler.ServeHTTP(probe, httptest.NewRequest(http.MethodPost, "/admin/v1/providers/remote/test", strings.NewReader(`{"credential_id":"remote-key"}`)))
	if probe.Code != http.StatusOK || !strings.Contains(probe.Body.String(), `"status":"available"`) || !strings.Contains(probe.Body.String(), `"model_count":2`) {
		t.Fatalf("probe failed: %d %s", probe.Code, probe.Body.String())
	}
	mismatched := httptest.NewRecorder()
	handler.ServeHTTP(mismatched, httptest.NewRequest(http.MethodPost, "/admin/v1/providers/other/test", strings.NewReader(`{"credential_id":"remote-key"}`)))
	if mismatched.Code != http.StatusBadRequest || !strings.Contains(mismatched.Body.String(), `"code":"invalid_credential"`) {
		t.Fatalf("provider accepted another provider's credential: %d %s", mismatched.Code, mismatched.Body.String())
	}
	sharedCredential := httptest.NewRecorder()
	handler.ServeHTTP(sharedCredential, httptest.NewRequest(http.MethodPost, "/admin/v1/credentials", strings.NewReader(`{"id":"shared-key","secret":"discovery-secret"}`)))
	if sharedCredential.Code != http.StatusCreated {
		t.Fatalf("shared credential create failed: %d %s", sharedCredential.Code, sharedCredential.Body.String())
	}
	sharedProbe := httptest.NewRecorder()
	handler.ServeHTTP(sharedProbe, httptest.NewRequest(http.MethodPost, "/admin/v1/providers/other/test", strings.NewReader(`{"credential_id":"shared-key"}`)))
	if sharedProbe.Code != http.StatusOK || !strings.Contains(sharedProbe.Body.String(), `"status":"available"`) {
		t.Fatalf("unbound credential was not reusable: %d %s", sharedProbe.Code, sharedProbe.Body.String())
	}
}

func TestAdminProviderProbeDoesNotReflectUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sensitive upstream error", http.StatusUnauthorized)
	}))
	defer upstream.Close()
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "remote", Type: "openai-compatible", BaseURL: upstream.URL}}})
	response := httptest.NewRecorder()
	Routes(NewHandler(modulesPipeline("admin"), runtime)).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/admin/v1/providers/remote/test", strings.NewReader(`{}`)))
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "sensitive") {
		t.Fatalf("unsafe probe error: %d %s", response.Code, response.Body.String())
	}
}
