package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagedMistralDiscoveryUsesScopedCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/proxy/v1/models" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("unexpected discovery request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"codestral-latest"},{"id":"mistral-small"}]}`)
	}))
	defer server.Close()
	router := New(Config{CredentialEncryptionKey: []byte("mistral-discovery-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "native", Type: "mistral", BaseURL: server.URL + "/proxy", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "native-key", ProviderID: "native", Secret: "test-secret"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(context.Background(), "native", "native-key")
	if err != nil || len(models) != 2 || models[0].ID != "codestral-latest" || models[1].ID != "mistral-small" {
		t.Fatalf("models=%v err=%v", models, err)
	}
}
