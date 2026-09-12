package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestTogetherInferenceContracts(t *testing.T) {
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path]++
		if r.Header.Get("Authorization") != "Bearer together-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		if r.Method == http.MethodPost {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "model" {
				t.Fatalf("path=%q body=%#v", r.URL.Path, body)
			}
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		case "/v1/completions":
			_, _ = fmt.Fprint(w, `{"id":"completion","object":"text_completion","model":"model","choices":[{"index":0,"text":"ok","finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		case "/v1/embeddings":
			_, _ = fmt.Fprint(w, `{"object":"list","model":"model","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
		case "/v1/models":
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"model-b"},{"id":"model-a"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewTogether(server.URL, "together-key", true)
	chat, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || chat.Usage.TotalTokens != 3 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	completion, err := client.Completions(t.Context(), openai.CompletionRequest{Model: "model", Prompt: "hello"})
	if err != nil || completion.Usage.TotalTokens != 3 {
		t.Fatalf("completion=%+v err=%v", completion, err)
	}
	embedding, err := client.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "model", Input: "hello"})
	if err != nil || embedding.Usage.TotalTokens != 2 || len(embedding.Data) != 1 {
		t.Fatalf("embedding=%+v err=%v", embedding, err)
	}
	router := New(Config{CredentialEncryptionKey: []byte("together-provider-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "together", Type: "together", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "together-key", ProviderID: "together", Secret: "together-key"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "together", "together-key")
	if err != nil || len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-b" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/models"} {
		if seen[path] != 1 {
			t.Fatalf("path %s called %d times", path, seen[path])
		}
	}
}

func TestTogetherCapabilityProfileIsBounded(t *testing.T) {
	for _, profile := range ManagedProviderCapabilityProfiles() {
		if profile.Type != "together" {
			continue
		}
		if !slices.Equal(profile.Operations, []string{"chat", "completions", "embeddings", "stream"}) || !slices.Equal(profile.Capabilities, []string{"chat", "completions", "embeddings", "stream", "tools", "structured_output", "vision"}) || len(profile.AuthTypes) != 0 {
			t.Fatalf("profile=%+v", profile)
		}
		if slices.Contains(profile.ChatParameters.SupportedOptions, "store") || slices.Contains(profile.ChatParameters.SupportedOptions, "metadata") || slices.Contains(profile.ChatParameters.SupportedOptions, "service_tier") || slices.Contains(profile.ChatParameters.SupportedOptions, "prediction") {
			t.Fatalf("ignored options advertised: %+v", profile.ChatParameters)
		}
		return
	}
	t.Fatal("Together capability profile is missing")
}

func TestTogetherRejectsIgnoredParameterBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	store := true
	_, err := NewTogether(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Store: &store}})
	if err == nil || !strings.Contains(err.Error(), "store") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}
