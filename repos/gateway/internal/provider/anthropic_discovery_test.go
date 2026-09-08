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

func TestAnthropicDiscoveryPaginates(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/proxy/v1/models" || r.URL.Query().Get("limit") != "1000" || r.Header.Get("x-api-key") != "test-secret" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
			t.Error("invalid native discovery request")
		}
		switch r.URL.Query().Get("after_id") {
		case "":
			_, _ = fmt.Fprint(w, `{"data":[{"id":"z"}],"has_more":true,"last_id":"z&cursor"}`)
		case "z&cursor":
			_, _ = fmt.Fprint(w, `{"data":[{"id":"a"},{"id":"z"}],"has_more":false,"last_id":"z"}`)
		default:
			t.Error("cursor was not encoded correctly")
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	for _, suffix := range []string{"/proxy", "/proxy/v1/"} {
		models, err := discoverAnthropicModels(context.Background(), server.URL+suffix, "test-secret")
		if err != nil || len(models) != 2 || models[0].ID != "a" || models[1].ID != "z" {
			t.Fatalf("models=%v err=%v", models, err)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestAnthropicDiscoveryRejectsIncompleteCatalog(t *testing.T) {
	for name, payload := range map[string]string{
		"missing cursor": `{"data":[{"id":"a"}],"has_more":true}`,
		"empty page":     `{"data":[],"has_more":true,"last_id":"a"}`,
		"cursor cycle":   `{"data":[{"id":"a"}],"has_more":true,"last_id":"a"}`,
		"invalid JSON":   `{`,
		"oversized page": strings.Repeat(" ", (2<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, payload) }))
			defer server.Close()
			models, err := discoverAnthropicModels(context.Background(), server.URL, "test-secret")
			if !errors.Is(err, ErrProviderProbeFailed) || models != nil {
				t.Fatalf("incomplete catalog returned: %v, %v", models, err)
			}
		})
	}
}

func TestAnthropicDiscoveryFailsWhenLaterPageFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after_id") != "" {
			w.WriteHeader(429)
			_, _ = fmt.Fprint(w, `{"error":{"message":"sensitive upstream details"}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"a"}],"has_more":true,"last_id":"a"}`)
	}))
	defer server.Close()
	models, err := discoverAnthropicModels(context.Background(), server.URL, "test-secret")
	if err != ErrProviderProbeFailed || models != nil {
		t.Fatalf("partial success or unredacted error: %v %v", models, err)
	}
}

func TestAnthropicDiscoveryBoundsPagesAndModels(t *testing.T) {
	for _, count := range []int{1, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page := calls.Add(1)
				data := make([]map[string]string, count)
				for i := range data {
					data[i] = map[string]string{"id": "duplicate"}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "has_more": true, "last_id": fmt.Sprint(page)})
			}))
			defer server.Close()
			models, err := discoverAnthropicModels(context.Background(), server.URL, "test-secret")
			wantCalls := int64(100)
			if count == 1000 {
				wantCalls = 11
			}
			if err != ErrProviderProbeFailed || models != nil || calls.Load() != wantCalls {
				t.Fatalf("limits: calls=%d models=%v err=%v", calls.Load(), models, err)
			}
		})
	}
}

func TestAnthropicDiscoveryDoesNotFollowRedirects(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	_, err := discoverAnthropicModels(context.Background(), server.URL, "test-secret")
	if err != ErrProviderProbeFailed || reached.Load() {
		t.Fatal("redirect followed or accepted")
	}
}

func TestAnthropicDiscoveryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	if _, err := discoverAnthropicModels(ctx, server.URL, "test-secret"); err != ErrProviderProbeFailed {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestManagedAnthropicDiscoveryUsesScopedCredential(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("x-api-key") != "test-secret" {
			t.Error("wrong provider credential")
		}
		if r.URL.Query().Get("after_id") == "" {
			_, _ = fmt.Fprint(w, `{"data":[{"id":"a"}],"has_more":true,"last_id":"a"}`)
		} else {
			_, _ = fmt.Fprint(w, `{"data":[{"id":"b"}],"has_more":false}`)
		}
	}))
	defer server.Close()
	router := New(Config{CredentialEncryptionKey: []byte("discovery-test-key")}).(*Router)
	for _, id := range []string{"native", "other"} {
		if _, err := router.CreateProvider(ManagedProvider{ID: id, Type: "anthropic", BaseURL: server.URL, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := router.CreateCredential(CredentialInput{ID: id + "-key", ProviderID: id, Secret: "test-secret"}); err != nil {
			t.Fatal(err)
		}
	}
	models, err := router.DiscoverProviderModels(context.Background(), "native", "native-key")
	if err != nil || len(models) != 2 || calls.Load() != 2 {
		t.Fatalf("managed discovery: models=%v err=%v calls=%d", models, err, calls.Load())
	}
	if _, err := router.DiscoverProviderModels(context.Background(), "native", "other-key"); err == nil {
		t.Fatal("foreign credential accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("foreign credential reached upstream")
	}
}
