package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
	"golang.org/x/net/websocket"
)

func TestAzureOpenAIRealtimeUsesNativeURLAndAuthentication(t *testing.T) {
	for _, test := range []struct {
		name, basePath, credential, apiVersion, authType string
		wantPath, wantQuery, wantAPIKey, wantBearer      string
	}{
		{name: "GA API key", basePath: "/openai/deployments/legacy", credential: "resource-key", authType: "api_key", wantPath: "/openai/v1/realtime", wantQuery: "model=deployment-a", wantAPIKey: "resource-key"},
		{name: "preview Entra", basePath: "/openai/deployments/legacy", credential: "entra-token", apiVersion: "2025-04-01-preview", authType: "entra", wantPath: "/openai/realtime", wantQuery: "api-version=2025-04-01-preview&deployment=deployment-a", wantBearer: "Bearer entra-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			serverErr := make(chan error, 1)
			server := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
				request := connection.Request()
				if request.URL.Path != test.wantPath || request.URL.RawQuery != test.wantQuery {
					serverErr <- fmt.Errorf("unexpected realtime URL: %s", request.URL.String())
					return
				}
				if request.Header.Get("api-key") != test.wantAPIKey || request.Header.Get("Authorization") != test.wantBearer {
					serverErr <- fmt.Errorf("unexpected realtime auth headers: %v", request.Header)
					return
				}
				var event string
				if err := websocket.Message.Receive(connection, &event); err != nil {
					serverErr <- err
					return
				}
				if event != `{"type":"session.update"}` {
					serverErr <- fmt.Errorf("unexpected realtime event: %s", event)
					return
				}
				serverErr <- nil
			}))
			t.Cleanup(server.Close)
			client := NewAzureOpenAI(server.URL+test.basePath, test.credential, false, test.apiVersion, test.authType)
			connection, err := client.OpenRealtime(t.Context(), "deployment-a")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connection.Close() })
			if err := connection.Send([]byte(`{"type":"session.update"}`)); err != nil {
				t.Fatal(err)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAzureOpenAIRealtimeFailsClosedWithoutAPIKey(t *testing.T) {
	client := NewAzureOpenAI("https://resource.openai.azure.com", "", false, "", "api_key")
	if _, err := client.OpenRealtime(t.Context(), "deployment-a"); err == nil {
		t.Fatal("missing Azure Realtime API key was accepted")
	}
}

func TestAzureOpenAIRealtimeUsesAmbientManagedIdentity(t *testing.T) {
	serverErr := make(chan error, 1)
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/identity/token", func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-IDENTITY-HEADER") != "identity-header" || request.URL.Query().Get("resource") != azureOpenAIResource {
			http.Error(w, "invalid identity request", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"ambient-token","expires_on":%d,"token_type":"Bearer"}`, time.Now().Add(time.Hour).Unix())
	})
	mux.Handle("/openai/v1/realtime", websocket.Handler(func(connection *websocket.Conn) {
		request := connection.Request()
		if request.URL.Query().Get("model") != "deployment-a" || request.Header.Get("Authorization") != "Bearer ambient-token" || request.Header.Get("api-key") != "" {
			serverErr <- fmt.Errorf("unexpected ambient realtime request: %s headers=%v", request.URL.String(), request.Header)
			return
		}
		serverErr <- nil
	}))
	t.Setenv("IDENTITY_ENDPOINT", server.URL+"/identity/token")
	t.Setenv("IDENTITY_HEADER", "identity-header")
	client := NewAzureOpenAI(server.URL, "", false, "", "entra")
	connection, err := client.OpenRealtime(t.Context(), "deployment-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

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

func TestAzureFoundryProjectUsesV1AndEntraBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/project-a/openai/v1/chat/completions" || r.URL.RawQuery != "" {
			t.Fatalf("unexpected URL: %s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer project-token" || r.Header.Get("api-key") != "" {
			t.Fatalf("unexpected auth headers: %v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "chat-foundry", Model: "deployment"})
	}))
	t.Cleanup(server.Close)
	client := NewAzureOpenAI(server.URL+"/api/projects/project-a", "project-token", false, "", "entra")
	if _, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "deployment", Messages: []openai.Message{{Role: "user", Content: "hello"}}}); err != nil {
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

func TestAzureOpenAIUsesAmbientManagedIdentity(t *testing.T) {
	now := time.Now().Add(time.Hour).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity/token":
			if r.Header.Get("X-IDENTITY-HEADER") != "identity-header" || r.URL.Query().Get("resource") != azureOpenAIResource {
				t.Fatalf("unexpected identity request: %s headers=%v", r.URL.String(), r.Header)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"ambient-token","expires_on":%d,"token_type":"Bearer"}`, now)))
		case "/openai/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer ambient-token" || r.Header.Get("api-key") != "" {
				t.Fatalf("unexpected auth headers: %v", r.Header)
			}
			_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "chat-azure", Model: "deployment"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("IDENTITY_ENDPOINT", server.URL+"/identity/token")
	t.Setenv("IDENTITY_HEADER", "identity-header")
	client := NewAzureOpenAI(server.URL, "", false, "", "entra")
	if _, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "deployment", Messages: []openai.Message{{Role: "user", Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedAzureDiscoveryUsesAmbientManagedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity/token":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"ambient-token","expires_on":%d,"token_type":"Bearer"}`, time.Now().Add(time.Hour).Unix())))
		case "/openai/v1/models":
			if r.Header.Get("Authorization") != "Bearer ambient-token" || r.Header.Get("api-key") != "" {
				t.Fatalf("unexpected discovery auth: %v", r.Header)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("IDENTITY_ENDPOINT", server.URL+"/identity/token")
	t.Setenv("IDENTITY_HEADER", "identity-header")
	router := New(Config{CredentialEncryptionKey: []byte("azure-ambient-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "azure", Type: "azure-openai", BaseURL: server.URL, AuthType: "entra", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "azure", "")
	if err != nil || len(models) != 1 || models[0].ID != "model-a" {
		t.Fatalf("models=%+v err=%v", models, err)
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
