package provider

import (
	"context"
	"errors"
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

func TestCohereDiscoveryPaginatesAndDeduplicates(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/models" || r.URL.Query().Get("page_size") != "1000" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("unexpected discovery request: %s headers=%v", r.URL, r.Header)
		}
		switch r.URL.Query().Get("page_token") {
		case "":
			_, _ = fmt.Fprint(w, `{"models":[{"name":"z","endpoints":["chat"]},{"name":"old","is_deprecated":true,"endpoints":["chat"]}],"next_page_token":"page +/="}`)
		case "page +/=":
			_, _ = fmt.Fprint(w, `{"models":[{"name":"a","endpoints":["embed"]},{"name":"z","endpoints":["chat"]}]}`)
		default:
			http.Error(w, "unexpected page", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	router := New(Config{CredentialEncryptionKey: []byte("cohere-pagination-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "cohere", Type: "cohere", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "cohere-key", ProviderID: "cohere", Secret: "test-secret"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "cohere", "cohere-key")
	if err != nil || len(models) != 2 || models[0].ID != "a" || models[1].ID != "z" || calls.Load() != 2 {
		t.Fatalf("models=%v calls=%d err=%v", models, calls.Load(), err)
	}
}

func TestCohereDiscoveryFailsWhenLaterPageFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page_token") == "" {
			_, _ = fmt.Fprint(w, `{"models":[{"name":"a","endpoints":["chat"]}],"next_page_token":"next"}`)
			return
		}
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "cohere", Type: "cohere", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "cohere", "")
	if !errors.Is(err, ErrProviderProbeFailed) || len(models) != 0 {
		t.Fatalf("partial catalog returned: models=%v err=%v", models, err)
	}
}

func TestCohereDiscoveryRejectsRepeatedPageToken(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"models":[],"next_page_token":"again"}`)
	}))
	t.Cleanup(server.Close)
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "cohere", Type: "cohere", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.DiscoverProviderModels(t.Context(), "cohere", ""); !errors.Is(err, ErrProviderProbeFailed) || calls.Load() != 2 {
		t.Fatalf("repeated pagination token accepted: calls=%d err=%v", calls.Load(), err)
	}
}

func TestCohereDiscoveryDoesNotFollowCredentialRedirect(t *testing.T) {
	var forwarded atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Store(true) }))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)
	router := New(Config{CredentialEncryptionKey: []byte("cohere-redirect-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "cohere", Type: "cohere", BaseURL: source.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "cohere-key", ProviderID: "cohere", Secret: "test-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.DiscoverProviderModels(t.Context(), "cohere", "cohere-key"); !errors.Is(err, ErrProviderProbeFailed) || forwarded.Load() {
		t.Fatalf("credential redirect accepted: forwarded=%v err=%v", forwarded.Load(), err)
	}
}
