package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestManagedCohereDiscoveryUsesScopedCredentialAndFiltersModels(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/proxy/v1/models" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("unexpected discovery request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"models":[{"name":"rerank-z","endpoints":["rerank"]},{"name":"embed-a","endpoints":["embed"]},{"name":"chat","endpoints":["chat"]},{"name":"rerank-old","is_deprecated":true,"endpoints":["rerank"]},{"name":"rerank-a","endpoints":["rerank"]}]}`)
	}))
	defer server.Close()
	router := New(Config{CredentialEncryptionKey: []byte("cohere-discovery-key")}).(*Router)
	for _, id := range []string{"native", "other"} {
		if _, err := router.CreateProvider(ManagedProvider{ID: id, Type: "cohere", BaseURL: server.URL + "/proxy", Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := router.CreateCredential(CredentialInput{ID: id + "-key", ProviderID: id, Secret: "test-secret"}); err != nil {
			t.Fatal(err)
		}
	}
	models, err := router.DiscoverProviderModels(context.Background(), "native", "native-key")
	if err != nil || len(models) != 4 || models[0].ID != "chat" || models[1].ID != "embed-a" || models[2].ID != "rerank-a" || models[3].ID != "rerank-z" || calls.Load() != 1 {
		t.Fatalf("models=%v calls=%d err=%v", models, calls.Load(), err)
	}
	if _, err := router.DiscoverProviderModels(context.Background(), "native", "other-key"); err == nil {
		t.Fatal("foreign credential accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("foreign credential reached upstream")
	}
}
