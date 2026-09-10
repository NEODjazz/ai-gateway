package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

func TestManagedBedrockDiscoveryUsesNativeContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/proxy/foundation-models" || r.Header.Get("Authorization") != "Bearer bedrock-key" {
			t.Errorf("unexpected discovery request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprint(w, `{"modelSummaries":[{"modelId":"amazon.nova-lite-v1:0"},{"modelId":"anthropic.claude-sonnet-4-20250514-v1:0"}]}`)
	}))
	defer server.Close()

	router := New(Config{CredentialEncryptionKey: []byte("bedrock-discovery-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "bedrock", Type: "bedrock", BaseURL: server.URL + "/proxy", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "bedrock-key", ProviderID: "bedrock", Secret: "bedrock-key"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "bedrock", "bedrock-key")
	if err != nil || len(models) != 2 || models[0].ID != "amazon.nova-lite-v1:0" || models[1].ID != "anthropic.claude-sonnet-4-20250514-v1:0" {
		t.Fatalf("models=%v err=%v", models, err)
	}
}

func TestManagedBedrockSharesAmbientCredentialsAcrossDiscoveryAndInference(t *testing.T) {
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		t.Setenv(name, "")
	}
	var credentialCalls atomic.Int32
	credentialServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		credentialCalls.Add(1)
		_, _ = fmt.Fprintf(w, `{"AccessKeyId":"ROLEKEY","SecretAccessKey":"secret","Token":"role-session","Expiration":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer credentialServer.Close()
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", credentialServer.URL)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=ROLEKEY/") || r.Header.Get("X-Amz-Security-Token") != "role-session" {
			t.Errorf("unsigned Bedrock request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/foundation-models":
			if strings.Contains(r.Header.Get("Authorization"), "content-type") {
				t.Errorf("discovery signed an absent content-type: %q", r.Header.Get("Authorization"))
			}
			_, _ = fmt.Fprint(w, `{"modelSummaries":[{"modelId":"model"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/model/model/converse":
			_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	router := New(Config{CredentialEncryptionKey: []byte("bedrock-ambient-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "aws", Type: "bedrock", BaseURL: upstream.URL, AuthType: "aws_sigv4", Region: "us-east-1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "aws", "")
	if err != nil || len(models) != 1 || models[0].ID != "model" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	if _, err := router.CreateModelDeployment(ModelDeployment{ID: "aws-model", ProviderID: "aws", UpstreamModel: "model", Models: []string{"public-model"}, Capabilities: []string{"chat"}, Weight: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var client Client
	for _, endpoint := range router.runtimeEndpoints() {
		if endpoint.Name == "aws-model" {
			client = endpoint.Provider
		}
	}
	if client == nil {
		t.Fatal("managed Bedrock deployment was not built")
	}
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || response.Usage.TotalTokens != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if credentialCalls.Load() != 1 {
		t.Fatalf("credential refresh calls=%d", credentialCalls.Load())
	}
}

func TestBedrockDiscoveryURLUsesControlPlaneHost(t *testing.T) {
	got, err := discoveryURL(ManagedProvider{Type: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://bedrock.us-east-1.amazonaws.com/foundation-models" {
		t.Fatalf("discovery URL=%q", got)
	}
}

func TestParseBedrockDiscoveryBoundsAndSortsModels(t *testing.T) {
	models, err := parseDiscoveredModels("bedrock", []byte(`{"modelSummaries":[{"modelId":"model-b"},{"modelId":""},{"modelId":"model-a"},{"modelId":"model-b"}]}`))
	if err != nil || len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-b" {
		t.Fatalf("models=%v err=%v", models, err)
	}
}

func TestParseBedrockDiscoveryRejectsMissingCatalog(t *testing.T) {
	if _, err := parseDiscoveredModels("bedrock", []byte(`{}`)); err == nil {
		t.Fatal("missing modelSummaries was accepted")
	}
}
