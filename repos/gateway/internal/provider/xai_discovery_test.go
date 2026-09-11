package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManagedXAIDiscoveryMergesTextAndEmbeddingCatalogs(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("Accept") != "application/json" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		switch r.URL.Path {
		case "/proxy/v1/models":
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"grok-4"},{"id":"shared"}]}`)
		case "/proxy/v1/embedding-models":
			_, _ = fmt.Fprint(w, `{"models":[{"id":"v1"},{"id":"shared"},{"id":" "}]}`)
		default:
			t.Fatalf("path=%q", r.URL.Path)
		}
	}))
	defer server.Close()
	router := New(Config{CredentialEncryptionKey: []byte("xai-discovery-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "native", Type: "xai", BaseURL: server.URL + "/proxy", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "native-key", ProviderID: "native", Secret: "test-secret"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(context.Background(), "native", "native-key")
	if err != nil || calls.Load() != 2 || len(models) != 3 || models[0].ID != "grok-4" || models[1].ID != "shared" || models[2].ID != "v1" {
		t.Fatalf("models=%v calls=%d err=%v", models, calls.Load(), err)
	}
}

func TestXAIDiscoveryFailsClosedOnIncompleteCatalog(t *testing.T) {
	for name, embeddingResult := range map[string]struct {
		status int
		body   string
	}{
		"upstream failure": {status: http.StatusTooManyRequests, body: `{"error":{"message":"private"}}`},
		"missing models":   {status: http.StatusOK, body: `{}`},
		"invalid JSON":     {status: http.StatusOK, body: `{`},
		"oversized":        {status: http.StatusOK, body: strings.Repeat(" ", (2<<20)+1)},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/embedding-models") {
					w.WriteHeader(embeddingResult.status)
					_, _ = fmt.Fprint(w, embeddingResult.body)
					return
				}
				_, _ = fmt.Fprint(w, `{"data":[{"id":"grok-4"}]}`)
			}))
			defer server.Close()
			models, err := discoverXAIModels(t.Context(), server.URL, "secret")
			if models != nil || !errors.Is(err, ErrProviderProbeFailed) {
				t.Fatalf("partial catalog accepted: models=%v err=%v", models, err)
			}
		})
	}
}

func TestXAIDiscoveryRejectsRedirectAndUnsafeBaseURL(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	if _, err := discoverXAIModels(t.Context(), source.URL, "secret"); !errors.Is(err, ErrProviderProbeFailed) || targetCalls.Load() != 0 {
		t.Fatalf("redirect followed: calls=%d err=%v", targetCalls.Load(), err)
	}
	for _, baseURL := range []string{"ftp://example.com", "https://user@example.com", "https://example.com?secret=x", "https://example.com#fragment"} {
		if _, err := discoverXAIModels(t.Context(), baseURL, "secret"); !errors.Is(err, ErrProviderProbeFailed) {
			t.Fatalf("unsafe base URL accepted: %q err=%v", baseURL, err)
		}
	}
}

func TestXAIDiscoveryBoundsCombinedCatalogSize(t *testing.T) {
	models := make([]map[string]string, 6000)
	for index := range models {
		models[index] = map[string]string{"id": "duplicate"}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embedding-models") {
			_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": models})
	}))
	defer server.Close()
	if result, err := discoverXAIModels(t.Context(), server.URL, "secret"); result != nil || !errors.Is(err, ErrProviderProbeFailed) {
		t.Fatalf("oversized combined catalog accepted: models=%v err=%v", result, err)
	}
}
