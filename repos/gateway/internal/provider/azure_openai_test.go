package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAzureOpenAIResourceRootUsesV1AndAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/chat/completions" || r.URL.RawQuery != "" {
			t.Fatalf("unexpected URL: %s", r.URL.String())
		}
		if r.Header.Get("api-key") != "resource-key" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected auth headers: %v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "chat-azure", Model: "deployment"})
	}))
	defer server.Close()
	client := NewAzureOpenAI(server.URL, "resource-key", false, "", "api_key")
	if _, err := client.ChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "deployment", Messages: []openai.Message{{Role: "user", Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestAzureOpenAIVersionedDeploymentUsesEntraBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/deployments/deployment-a/embeddings" || r.URL.Query().Get("api-version") != "2025-04-01-preview" {
			t.Fatalf("unexpected URL: %s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer entra-token" || r.Header.Get("api-key") != "" {
			t.Fatalf("unexpected auth headers: %v", r.Header)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"deployment-a","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer server.Close()
	client := NewAzureOpenAI(server.URL+"/openai/deployments/deployment-a", "entra-token", false, "2025-04-01-preview", "entra")
	if _, err := client.Embeddings(context.Background(), openai.EmbeddingRequest{Model: "deployment-a", Input: "hello"}); err != nil {
		t.Fatal(err)
	}
}

func TestAzureOpenAIDoesNotForwardCredentialAcrossRedirect(t *testing.T) {
	received := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := NewAzureOpenAI(source.URL, "resource-key", false, "", "api_key")
	if _, err := client.ChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "deployment", Messages: []openai.Message{{Role: "user", Content: "hello"}}}); err == nil {
		t.Fatal("expected redirect rejection")
	}
	if received {
		t.Fatal("credential-bearing redirect reached another host")
	}
}

func TestManagedAzureOpenAIDiscoveryDoesNotForwardCredentialAcrossRedirect(t *testing.T) {
	received := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	router := New(Config{CredentialEncryptionKey: []byte("azure-redirect-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "azure", Type: "azure-openai", BaseURL: source.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "azure-key", ProviderID: "azure", Secret: "resource-key"}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.DiscoverProviderModels(context.Background(), "azure", "azure-key"); err == nil {
		t.Fatal("expected redirect rejection")
	}
	if received {
		t.Fatal("credential-bearing discovery redirect reached another host")
	}
}

func TestManagedAzureOpenAIDiscoveryUsesNativeVersionAndAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/models" || r.URL.Query().Get("api-version") != "2024-10-21" || r.Header.Get("api-key") != "resource-key" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected discovery request: %s headers=%v", r.URL.String(), r.Header)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-b"},{"id":"model-a"}]}`))
	}))
	defer server.Close()
	router := New(Config{CredentialEncryptionKey: []byte("azure-discovery-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "azure", Type: "azure-openai", BaseURL: server.URL + "/openai/deployments/deployment-a", APIVersion: "2024-10-21", AuthType: "api_key", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "azure-key", ProviderID: "azure", Secret: "resource-key"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(context.Background(), "azure", "azure-key")
	if err != nil || len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-b" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
}

func TestManagedAzureOpenAIRejectsInvalidNativeSettings(t *testing.T) {
	for _, input := range []ManagedProvider{
		{ID: "azure", Type: "azure-openai", BaseURL: "https://example.test", APIVersion: "2025-13-01"},
		{ID: "azure", Type: "azure-openai", BaseURL: "https://example.test", AuthType: "basic"},
		{ID: "azure", Type: "azure-openai", BaseURL: "https://example.test?secret=value"},
		{ID: "azure", Type: "azure-openai", BaseURL: "https://example.test#fragment"},
	} {
		if _, err := normalizeManagedProvider(input); err == nil {
			t.Fatalf("invalid managed provider accepted: %+v", input)
		}
	}
}
