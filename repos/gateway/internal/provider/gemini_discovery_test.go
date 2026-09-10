package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeminiDiscoveryPaginatesAndFiltersCapabilities(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1beta/models" || r.Header.Get("x-goog-api-key") != "fake-key" || r.URL.Query().Has("key") {
			t.Error("invalid discovery auth/path")
		}
		switch r.URL.Query().Get("pageToken") {
		case "":
			_, _ = fmt.Fprint(w, `{"models":[{"name":"models/z","supportedGenerationMethods":["generateContent"]},{"name":"models/embed","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"next&token"}`)
		case "next&token":
			_, _ = fmt.Fprint(w, `{"models":[{"name":"models/a","supportedGenerationMethods":["generateContent"]},{"name":"models/z","supportedGenerationMethods":["generateContent"]}]}`)
		default:
			t.Error("wrong continuation token")
		}
	}))
	defer server.Close()
	models, err := discoverGeminiModels(context.Background(), server.URL, "fake-key")
	if err != nil || calls != 2 || len(models) != 3 || models[0].ID != "a" || models[1].ID != "embed" || models[2].ID != "z" {
		t.Fatalf("discovery: models=%v calls=%d err=%v", models, calls, err)
	}
}

func TestGeminiDiscoveryUsesGCPWorkloadToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metadata/token":
			w.Header().Set("Metadata-Flavor", "Google")
			_, _ = fmt.Fprint(w, `{"access_token":"workload-token","expires_in":3600,"token_type":"Bearer"}`)
		case "/v1beta/models":
			if r.Header.Get("Authorization") != "Bearer workload-token" || r.Header.Get("x-goog-api-key") != "" {
				t.Errorf("invalid workload discovery auth: %v", r.Header)
			}
			_, _ = fmt.Fprint(w, `{"models":[{"name":"models/model-a","supportedGenerationMethods":["generateContent"]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	gemini := NewGeminiWithAuth(server.URL, "", false, "gcp_adc")
	gemini.tokenSource.metadataURL = server.URL + "/metadata/token"
	models, err := discoverGeminiModelsWithClient(t.Context(), gemini)
	if err != nil || len(models) != 1 || models[0].ID != "model-a" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
}

func TestGeminiDiscoveryRejectsPaginationCycles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, `{"nextPageToken":"repeat"}`) }))
	defer server.Close()
	if _, err := discoverGeminiModels(context.Background(), server.URL, ""); err == nil {
		t.Fatal("pagination cycle accepted")
	}
}
