package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"golang.org/x/net/websocket"
)

type azureRoundTripFunc func(*http.Request) (*http.Response, error)

func (f azureRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestAzureOpenAIRealtimeUsesNativeURLAndAuthentication(t *testing.T) {
	for _, test := range []struct {
		name, basePath, credential, apiVersion, authType string
		wantPath, wantQuery, wantAPIKey, wantBearer      string
	}{
		{name: "GA API key", basePath: "/openai/deployments/legacy", credential: "resource-key", authType: "api_key", wantPath: "/openai/v1/realtime", wantQuery: "model=deployment-a", wantAPIKey: "resource-key"},
		{name: "preview Entra", basePath: "/openai/deployments/legacy", credential: "entra-token", apiVersion: "2025-04-01-preview", authType: "entra", wantPath: "/openai/realtime", wantQuery: "api-version=2025-04-01-preview&deployment=deployment-a", wantBearer: "Bearer entra-token"},
		{name: "prefixed GA API key", basePath: "/tenant-a/openai/v1", credential: "resource-key", authType: "api_key", wantPath: "/tenant-a/openai/v1/realtime", wantQuery: "model=deployment-a", wantAPIKey: "resource-key"},
		{name: "prefixed preview Entra", basePath: "/tenant-a/openai/deployments/legacy", credential: "entra-token", apiVersion: "2025-04-01-preview", authType: "entra", wantPath: "/tenant-a/openai/realtime", wantQuery: "api-version=2025-04-01-preview&deployment=deployment-a", wantBearer: "Bearer entra-token"},
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

func TestManagedAzureRealtimeRoutesWithAuthenticationAndQuota(t *testing.T) {
	for _, test := range []struct {
		name, authType, apiVersion, wantPath, wantQuery string
	}{
		{name: "GA API key", authType: "api_key", wantPath: "/openai/v1/realtime", wantQuery: "model=upstream-model"},
		{name: "preview Entra", authType: "entra", apiVersion: "2025-04-01-preview", wantPath: "/openai/realtime", wantQuery: "api-version=2025-04-01-preview&deployment=upstream-model"},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstreamCheck := make(chan error, 1)
			server := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
				request := connection.Request()
				if request.URL.Path != test.wantPath || request.URL.RawQuery != test.wantQuery {
					upstreamCheck <- fmt.Errorf("unexpected realtime URL: %s", request.URL.String())
					return
				}
				if test.authType == "entra" && (request.Header.Get("Authorization") != "Bearer managed-secret" || request.Header.Get("api-key") != "") ||
					test.authType == "api_key" && (request.Header.Get("api-key") != "managed-secret" || request.Header.Get("Authorization") != "") {
					upstreamCheck <- fmt.Errorf("unexpected realtime authentication headers: %v", request.Header)
					return
				}
				upstreamCheck <- nil
			}))
			t.Cleanup(server.Close)

			router := New(Config{CredentialEncryptionKey: []byte("azure-realtime-test-key"), DeploymentQuotaStore: NewMemoryDeploymentQuotaStore()}).(*Router)
			if _, err := router.CreateProvider(ManagedProvider{ID: "azure", Type: "azure-openai", BaseURL: server.URL, APIVersion: test.apiVersion, AuthType: test.authType, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := router.CreateCredential(CredentialInput{ID: "azure-key", ProviderID: "azure", Secret: "managed-secret"}); err != nil {
				t.Fatal(err)
			}
			if _, err := router.CreateModelDeployment(ModelDeployment{ID: "azure-realtime", ProviderID: "azure", CredentialID: "azure-key", Models: []string{"public-model"}, UpstreamModel: "upstream-model", Capabilities: []string{"realtime"}, RateLimitTPM: 4, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			connection, attempt, err := router.OpenRealtime(t.Context(), modules.RequestContext{RequestID: "execution"}, "public-model")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connection.Close() })
			if err := <-upstreamCheck; err != nil {
				t.Fatal(err)
			}
			if attempt.Request.Model != "upstream-model" || attempt.Metadata["provider.endpoint.name"] != "azure-realtime" {
				t.Fatalf("unexpected routed attempt: %+v", attempt)
			}
			reserver, ok := connection.(RealtimeTokenReserver)
			if !ok {
				t.Fatal("managed Azure realtime connection has no token reservation")
			}
			if err := reserver.ReserveRealtimeTokens(t.Context(), 4); err != nil {
				t.Fatal(err)
			}
			var quotaErr *DeploymentQuotaError
			if err := reserver.ReserveRealtimeTokens(t.Context(), 1); !errors.As(err, &quotaErr) {
				t.Fatalf("deployment TPM was not enforced: %v", err)
			}
		})
	}
}

func TestAzureFoundryProjectDoesNotAdvertiseResourceRealtime(t *testing.T) {
	router := New(Config{CredentialEncryptionKey: []byte("foundry-realtime-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "foundry", Type: "azure-openai", BaseURL: "https://resource.services.ai.azure.com/api/projects/project-a", AuthType: "entra", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, err := router.CreateModelDeployment(ModelDeployment{ID: "foundry-realtime", ProviderID: "foundry", Models: []string{"model"}, Capabilities: []string{"realtime"}, Enabled: true})
	if !errors.Is(err, ErrUnsupportedProviderCapability) {
		t.Fatalf("project Realtime capability accepted: %v", err)
	}
	client := NewAzureOpenAI("https://resource.services.ai.azure.com/api/projects/project-a", "token", false, "", "entra")
	if _, err := client.OpenRealtime(t.Context(), "model"); err == nil {
		t.Fatal("project URL was silently rewritten to resource Realtime URL")
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

func TestAzureOpenAIInferenceUsesClientSecretIdentity(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/tenant-id/oauth2/v2.0/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("client_secret") != "private-value" || r.Form.Get("scope") != azureOpenAIScope {
			t.Errorf("invalid service principal request: scope=%q", r.Form.Get("scope"))
		}
		_, _ = fmt.Fprint(w, `{"access_token":"client-secret-token","expires_in":3600,"token_type":"Bearer"}`)
	})
	mux.HandleFunc("/openai/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer client-secret-token" || r.Header.Get("api-key") != "" {
			t.Errorf("invalid Azure inference authentication")
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "chat-azure", Model: "deployment"})
	})
	t.Setenv("AZURE_TENANT_ID", "tenant-id")
	t.Setenv("AZURE_CLIENT_ID", "client-id")
	t.Setenv("AZURE_CLIENT_SECRET", "private-value")
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", "")
	client := NewAzureOpenAI(server.URL, "", false, "", "entra")
	transport := client.client.Transport.(azureOpenAITransport)
	transport.tokenSource.authorityBaseURL = server.URL
	if _, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "deployment", Messages: []openai.Message{{Role: "user", Content: "hello"}}}); err != nil {
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

func TestAzureFoundryProjectBehindPathPrefix(t *testing.T) {
	for _, test := range []struct {
		path string
		want string
		ok   bool
	}{
		{"/tenant/api/projects/project-a", "/tenant/api/projects/project-a", true},
		{"/tenant/api/projects/project-a/openai/v1", "/tenant/api/projects/project-a", true},
		{"/tenant/api/projects/project-a/other", "", false},
		{"/tenant/api/projects/", "", false},
	} {
		got, ok := azureFoundryProjectPath(test.path)
		if got != test.want || ok != test.ok {
			t.Errorf("project path %q = %q, %t; want %q, %t", test.path, got, ok, test.want, test.ok)
		}
	}
	if _, err := normalizeManagedProvider(ManagedProvider{ID: "foundry", Type: "azure-openai", BaseURL: "https://proxy.example.test/tenant/api/projects/project-a", APIVersion: "2025-04-01-preview", AuthType: "entra"}); err == nil {
		t.Fatal("versioned Foundry project URL behind path prefix was accepted")
	}
	if azureRealtimeSupportedBaseURL("https://proxy.example.test/tenant/api/projects/project-a/openai/v1") {
		t.Fatal("Foundry project behind path prefix advertised resource Realtime")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tenant/api/projects/project-a/openai/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer project-token" || r.Header.Get("api-key") != "" {
			t.Errorf("unexpected Foundry request: %s headers=%v", r.URL, r.Header)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "chat-foundry", Model: "deployment"})
	}))
	t.Cleanup(server.Close)
	client := NewAzureOpenAI(server.URL+"/tenant/api/projects/project-a", "project-token", false, "", "entra")
	if _, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "deployment", Messages: []openai.Message{{Role: "user", Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestAzureFoundryProjectManagedIdentityEndToEnd(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var tokenCalls atomic.Int32
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		if r.Header.Get("Metadata") != "true" || r.URL.Query().Get("resource") != azureFoundryResource {
			t.Errorf("managed identity request: headers=%v query=%v", r.Header, r.URL.Query())
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"project-identity-token","expires_on":%d,"token_type":"Bearer"}`, now.Add(time.Hour).Unix())
	}))
	t.Cleanup(identity.Close)
	var inferenceCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inferenceCalls.Add(1)
		if r.URL.Path != "/api/projects/project-a/openai/v1/chat/completions" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer project-identity-token" || r.Header.Get("api-key") != "" {
			t.Errorf("Foundry inference request: %s headers=%v", r.URL.String(), r.Header)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "chat-foundry", Model: "deployment"})
	}))
	t.Cleanup(upstream.Close)
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewAzureOpenAI("https://resource.services.ai.azure.com/api/projects/project-a", "", false, "", "entra")
	transport := client.client.Transport.(azureOpenAITransport)
	transport.tokenSource.now = func() time.Time { return now }
	transport.tokenSource.getenv = awsTestEnvironment(nil)
	transport.tokenSource.imdsURL = identity.URL
	transport.base = azureRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "resource.services.ai.azure.com" || request.URL.Scheme != "https" {
			t.Errorf("unexpected provider origin: %s", request.URL.String())
		}
		cloned := request.Clone(request.Context())
		cloned.URL.Scheme, cloned.URL.Host, cloned.Host = upstreamURL.Scheme, upstreamURL.Host, upstreamURL.Host
		return http.DefaultTransport.RoundTrip(cloned)
	})
	client.client.Transport = transport
	request := openai.ChatCompletionRequest{Model: "deployment", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	for range 2 {
		if _, err := client.ChatCompletions(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	if tokenCalls.Load() != 1 || inferenceCalls.Load() != 2 {
		t.Fatalf("token requests=%d inference requests=%d", tokenCalls.Load(), inferenceCalls.Load())
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

func TestManagedAzureVersionedRootUsesDeploymentRoute(t *testing.T) {
	for _, test := range []struct {
		name, authType, basePath, wantPath string
	}{
		{name: "API key", authType: "api_key", wantPath: "/openai/deployments/upstream-deployment/chat/completions"},
		{name: "Entra", authType: "entra", wantPath: "/openai/deployments/upstream-deployment/chat/completions"},
		{name: "reverse proxy prefix", authType: "entra", basePath: "/tenant-a/openai/v1", wantPath: "/tenant-a/openai/deployments/upstream-deployment/chat/completions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.wantPath || r.URL.Query().Get("api-version") != "2024-10-21" {
					t.Errorf("unexpected Azure URL: %s", r.URL.String())
					http.NotFound(w, r)
					return
				}
				if test.authType == "entra" && (r.Header.Get("Authorization") != "Bearer managed-secret" || r.Header.Get("api-key") != "") ||
					test.authType == "api_key" && (r.Header.Get("api-key") != "managed-secret" || r.Header.Get("Authorization") != "") {
					t.Errorf("unexpected Azure authentication headers: %v", r.Header)
				}
				var request openai.ChatCompletionRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "upstream-deployment" {
					t.Errorf("unexpected upstream model: %q err=%v", request.Model, err)
				}
				_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
					ID: "chat-azure", Model: "upstream-deployment", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "hello"}, FinishReason: "stop"}},
					Usage: openai.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3},
				})
			}))
			t.Cleanup(server.Close)
			router := New(Config{CredentialEncryptionKey: []byte("azure-versioned-root-test-key")}).(*Router)
			if _, err := router.CreateProvider(ManagedProvider{ID: "azure", Type: "azure-openai", BaseURL: server.URL + test.basePath, APIVersion: "2024-10-21", AuthType: test.authType, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := router.CreateCredential(CredentialInput{ID: "azure-credential", ProviderID: "azure", Secret: "managed-secret"}); err != nil {
				t.Fatal(err)
			}
			if _, err := router.CreateModelDeployment(ModelDeployment{ID: "azure-deployment", ProviderID: "azure", CredentialID: "azure-credential", Models: []string{"public-model"}, UpstreamModel: "upstream-deployment", Capabilities: []string{"chat"}, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			response, err := router.ChatCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Provider: "azure-deployment", Model: "public-model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}})
			if err != nil || response.Usage.TotalTokens != 3 {
				t.Fatalf("Azure routed response=%+v err=%v", response, err)
			}
		})
	}
}

func TestConfiguredAzureVersionedRootUsesDeploymentRoute(t *testing.T) {
	for _, test := range []struct {
		name, authType, basePath, wantPath, model string
		models                                    []string
		aliases                                   map[string]string
	}{
		{name: "API key", authType: "api_key", wantPath: "/openai/deployments/upstream-deployment/chat/completions", model: "public-model", models: []string{"public-model"}, aliases: map[string]string{"public-model": "upstream-deployment"}},
		{name: "Entra with reverse proxy", authType: "entra", basePath: "/tenant-a/openai/v1", wantPath: "/tenant-a/openai/deployments/upstream-deployment/chat/completions", model: "public-model", models: []string{"public-model"}, aliases: map[string]string{"public-model": "upstream-deployment"}},
		{name: "shared deployment aliases", authType: "api_key", wantPath: "/openai/deployments/upstream-deployment/chat/completions", model: "public-two", models: []string{"public-one", "public-two"}, aliases: map[string]string{"public-one": "upstream-deployment", "public-two": "upstream-deployment"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.wantPath || r.URL.Query().Get("api-version") != "2024-10-21" {
					t.Errorf("unexpected Azure URL: %s", r.URL.String())
					http.NotFound(w, r)
					return
				}
				if test.authType == "entra" && (r.Header.Get("Authorization") != "Bearer configured-secret" || r.Header.Get("api-key") != "") ||
					test.authType == "api_key" && (r.Header.Get("api-key") != "configured-secret" || r.Header.Get("Authorization") != "") {
					t.Errorf("unexpected Azure authentication headers: %v", r.Header)
				}
				var request openai.ChatCompletionRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "upstream-deployment" {
					t.Errorf("unexpected upstream model: %q err=%v", request.Model, err)
				}
				_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
					ID: "chat-azure", Model: "upstream-deployment", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "hello"}, FinishReason: "stop"}},
					Usage: openai.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3},
				})
			}))
			t.Cleanup(server.Close)
			router, err := NewWithError(Config{Endpoints: []config.ProviderEndpointConfig{{
				Name: "azure-static", Type: "azure-openai", BaseURL: server.URL + test.basePath,
				APIKey: "configured-secret", APIVersion: "2024-10-21", AuthType: test.authType,
				Models: test.models, ModelAliases: test.aliases, Capabilities: []string{"chat"},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := router.ChatCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Provider: "azure-static", Model: test.model, Messages: []openai.Message{{Role: "user", Content: "hello"}}}})
			if err != nil || response.Usage.TotalTokens != 3 {
				t.Fatalf("configured Azure response=%+v err=%v", response, err)
			}
		})
	}
}

func TestConfiguredAzureVersionedRootRejectsAmbiguousModels(t *testing.T) {
	_, err := NewWithError(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "azure-static", Type: "azure-openai", BaseURL: "https://resource.openai.azure.com", APIVersion: "2024-10-21",
		Models: []string{"public-one", "public-two"}, Capabilities: []string{"chat"},
	}}})
	if !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("ambiguous versioned Azure route was accepted: %v", err)
	}
}

func TestAzureManagedDeploymentBaseURL(t *testing.T) {
	for _, test := range []struct {
		name, baseURL, apiVersion, upstream, want string
		models                                    []string
		invalid                                   bool
	}{
		{name: "resource root", baseURL: "https://resource.openai.azure.com", apiVersion: "2024-10-21", models: []string{"deployment-a"}, want: "https://resource.openai.azure.com/openai/deployments/deployment-a"},
		{name: "reverse proxy prefix", baseURL: "https://proxy.example.test/tenant-a/openai/v1", apiVersion: "2024-10-21", upstream: "deployment-a", models: []string{"public"}, want: "https://proxy.example.test/tenant-a/openai/deployments/deployment-a"},
		{name: "explicit deployment", baseURL: "https://resource.openai.azure.com/openai/deployments/deployment-a", apiVersion: "2024-10-21", models: []string{"public", "other"}, want: "https://resource.openai.azure.com/openai/deployments/deployment-a"},
		{name: "GA v1", baseURL: "https://resource.openai.azure.com", models: []string{"public", "other"}, want: "https://resource.openai.azure.com"},
		{name: "v1 preview", baseURL: "https://resource.openai.azure.com", apiVersion: "preview", models: []string{"public", "other"}, want: "https://resource.openai.azure.com"},
		{name: "ambiguous model", baseURL: "https://resource.openai.azure.com", apiVersion: "2024-10-21", models: []string{"one", "two"}, invalid: true},
		{name: "unsafe deployment segment", baseURL: "https://resource.openai.azure.com", apiVersion: "2024-10-21", upstream: "name/other", models: []string{"public"}, invalid: true},
		{name: "dot deployment segment", baseURL: "https://resource.openai.azure.com", apiVersion: "2024-10-21", upstream: "..", models: []string{"public"}, invalid: true},
		{name: "unsafe explicit deployment", baseURL: "https://resource.openai.azure.com/openai/deployments/name/other", apiVersion: "2024-10-21", models: []string{"public"}, invalid: true},
		{name: "encoded explicit deployment", baseURL: "https://resource.openai.azure.com/openai/deployments/name%2fother", apiVersion: "2024-10-21", models: []string{"public"}, invalid: true},
		{name: "incomplete explicit path", baseURL: "https://resource.openai.azure.com/openai/deployments", apiVersion: "2024-10-21", models: []string{"public"}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := azureManagedDeploymentBaseURL(test.baseURL, test.apiVersion, ModelDeployment{Models: test.models, UpstreamModel: test.upstream})
			if test.invalid {
				if !errors.Is(err, ErrInvalidDeployment) {
					t.Fatalf("expected invalid deployment, got URL=%q err=%v", got, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("URL=%q, want %q, err=%v", got, test.want, err)
			}
		})
	}
}

func TestAzureVersionedResponseRoutesUseResourcePath(t *testing.T) {
	for _, test := range []struct {
		name, basePath, method, suffix, wantPath string
	}{
		{name: "create", basePath: "/openai/deployments/deployment-a", method: http.MethodPost, suffix: "responses", wantPath: "/openai/responses"},
		{name: "retrieve", basePath: "/openai/deployments/deployment-a", method: http.MethodGet, suffix: "responses/resp_123", wantPath: "/openai/responses/resp_123"},
		{name: "cancel", basePath: "/openai/deployments/deployment-a", method: http.MethodPost, suffix: "responses/resp_123/cancel", wantPath: "/openai/responses/resp_123/cancel"},
		{name: "input items", basePath: "/tenant-a/openai/deployments/deployment-a", method: http.MethodGet, suffix: "responses/resp_123/input_items", wantPath: "/tenant-a/openai/responses/resp_123/input_items"},
		{name: "count tokens", basePath: "/openai/deployments/deployment-a", method: http.MethodPost, suffix: "responses/input_tokens", wantPath: "/openai/responses/input_tokens"},
		{name: "chat stays deployment scoped", basePath: "/openai/deployments/deployment-a", method: http.MethodPost, suffix: "chat/completions", wantPath: "/openai/deployments/deployment-a/chat/completions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != test.method || r.URL.Path != test.wantPath || r.URL.Query().Get("api-version") != "2025-04-01-preview" {
					t.Errorf("unexpected Azure request: %s %s", r.Method, r.URL.String())
					http.NotFound(w, r)
					return
				}
				if r.Header.Get("api-key") != "resource-key" || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected Azure authentication headers: %v", r.Header)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)
			client := NewAzureOpenAI(server.URL+test.basePath, "resource-key", false, "2025-04-01-preview", "api_key")
			request, err := http.NewRequestWithContext(t.Context(), test.method, providerURL(client.baseURL, test.suffix), http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = response.Body.Close() })
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("unexpected Azure status: %d", response.StatusCode)
			}
		})
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

func TestAzureOpenAIBasePathSharesGAModelAndInferenceRoutes(t *testing.T) {
	var modelCalls, chatCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "resource-key" || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
			t.Errorf("unexpected Azure headers or query: %s headers=%v", r.URL, r.Header)
		}
		switch r.URL.Path {
		case "/openai/v1/models":
			modelCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":[{"id":"deployment-a"}]}`))
		case "/openai/v1/chat/completions":
			chatCalls.Add(1)
			_, _ = w.Write([]byte(`{"id":"chat-azure","object":"chat.completion","model":"deployment-a","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	router := New(Config{CredentialEncryptionKey: []byte("azure-ga-discovery-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "azure", Type: "azure-openai", BaseURL: server.URL + "/openai", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "azure-key", ProviderID: "azure", Secret: "resource-key"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "azure", "azure-key")
	if err != nil || len(models) != 1 || models[0].ID != "deployment-a" {
		t.Fatalf("models=%v err=%v", models, err)
	}
	client := NewAzureOpenAI(server.URL+"/openai", "resource-key", false, "", "api_key")
	if _, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "deployment-a", Messages: []openai.Message{{Role: "user", Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if modelCalls.Load() != 1 || chatCalls.Load() != 1 {
		t.Fatalf("model calls=%d chat calls=%d", modelCalls.Load(), chatCalls.Load())
	}
}

func TestAzureOpenAIBasePathDiscoveryKeepsVersionedRoute(t *testing.T) {
	for _, test := range []struct {
		apiVersion string
		wantPath   string
	}{
		{apiVersion: "", wantPath: "/tenant/openai/v1/models"},
		{apiVersion: "preview", wantPath: "/tenant/openai/v1/models"},
		{apiVersion: "2024-10-21", wantPath: "/tenant/openai/models"},
	} {
		t.Run(test.apiVersion, func(t *testing.T) {
			endpoint, err := azureOpenAIDiscoveryURL(ManagedProvider{BaseURL: "https://proxy.example.test/tenant/openai", APIVersion: test.apiVersion})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(endpoint)
			if err != nil || parsed.Path != test.wantPath {
				t.Fatalf("discovery endpoint=%q err=%v", endpoint, err)
			}
		})
	}
}

func TestAzureOpenAIBasePathPreviewKeepsV1InferenceRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tenant/openai/v1/chat/completions" || r.URL.RawQuery != "api-version=preview" || r.Header.Get("api-key") != "resource-key" {
			t.Errorf("unexpected preview request: %s headers=%v", r.URL, r.Header)
			http.Error(w, "invalid route", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"id":"chat-azure","object":"chat.completion","model":"deployment-a","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(server.Close)
	client := NewAzureOpenAI(server.URL+"/tenant/openai", "resource-key", false, "preview", "api_key")
	if _, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "deployment-a", Messages: []openai.Message{{Role: "user", Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedAzureOpenAIRejectsInvalidNativeSettings(t *testing.T) {
	for _, input := range []ManagedProvider{
		{ID: "azure", Type: "azure-openai", BaseURL: "https://example.test", APIVersion: "2025-13-01"},
		{ID: "azure", Type: "azure-openai", BaseURL: "https://example.test", AuthType: "basic"},
		{ID: "azure", Type: "azure-openai", BaseURL: "https://example.test?secret=value"},
		{ID: "azure", Type: "azure-openai", BaseURL: "https://proxy.example.test/tenant/../openai/v1"},
		{ID: "azure", Type: "azure-openai", BaseURL: "https://proxy.example.test/tenant%2fother/openai/v1"},
		{ID: "azure", Type: "azure-openai", BaseURL: "https://example.test#fragment"},
		{ID: "foundry", Type: "azure-openai", BaseURL: "https://resource.services.ai.azure.com/api/projects/project-a", APIVersion: "2025-04-01-preview", AuthType: "entra"},
		{ID: "foundry", Type: "azure-openai", BaseURL: "https://resource.services.ai.azure.com/api/projects/project-a/openai/v1", APIVersion: "2025-04-01-preview", AuthType: "entra"},
	} {
		if _, err := normalizeManagedProvider(input); err == nil {
			t.Fatalf("invalid managed provider accepted: %+v", input)
		}
	}
}
