package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

const containerFixture = `{"id":"cntr_123","object":"container","created_at":1,"status":"running","expires_after":{"anchor":"last_active_at","minutes":20},"last_active_at":2,"memory_limit":"4g","name":"analysis"}`

func TestOpenAICompatibleContainerLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/containers":
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if !strings.Contains(text, `"name":"analysis"`) || strings.Contains(text, `"model"`) || strings.Contains(text, `"provider"`) {
				t.Fatalf("body=%s", body)
			}
			_, _ = io.WriteString(w, containerFixture)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/containers/cntr_123":
			_, _ = io.WriteString(w, containerFixture)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/containers/cntr_123":
			_, _ = io.WriteString(w, `{"id":"cntr_123","object":"container.deleted","deleted":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewOpenAICompatible(server.URL+"/v1", "secret", false)
	client.client = server.Client()
	input := openai.ContainerProviderCreateRequest{Name: "analysis", MemoryLimit: "4g", ExpiresAfter: &openai.ContainerExpiresAfter{Anchor: "last_active_at", Minutes: 20}}
	created, err := client.CreateContainer(t.Context(), input)
	if err != nil || created.ID != "cntr_123" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if retrieved, err := client.RetrieveContainer(t.Context(), created.ID); err != nil || retrieved.ID != created.ID {
		t.Fatalf("retrieved=%+v err=%v", retrieved, err)
	}
	if deleted, err := client.DeleteContainer(t.Context(), created.ID); err != nil || !deleted.Deleted {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
}

func TestContainerRouterRequiresExplicitCapabilityAndPinsDeployment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, containerFixture) }))
	defer server.Close()
	newRuntime := func(capabilities []string) ContainerProvider {
		return New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "sandbox", Type: "openai", BaseURL: server.URL + "/v1", Models: []string{"public-model"}, Capabilities: capabilities}}}).(ContainerProvider)
	}
	if _, _, err := newRuntime(nil).CreateContainer(t.Context(), modules.RequestContext{}, openai.ContainerCreateRequest{Model: "public-model", Name: "analysis"}, nil); err == nil {
		t.Fatal("container route accepted without explicit capability")
	}
	runtime := newRuntime([]string{"container"})
	admitted := false
	created, binding, err := runtime.CreateContainer(t.Context(), modules.RequestContext{}, openai.ContainerCreateRequest{Model: "public-model", Name: "analysis"}, func(_ context.Context, request *modules.RequestContext) error {
		admitted = request.Request.Model == "public-model" && request.Metadata["gateway.api_type"] == "container"
		return nil
	})
	if err != nil || !admitted || created.ID != "cntr_123" || binding.Endpoint != "sandbox" || len(binding.Deployment) != 64 {
		t.Fatalf("created=%+v binding=%+v admitted=%t err=%v", created, binding, admitted, err)
	}
	binding.Deployment = strings.Repeat("0", 64)
	if _, err = runtime.RetrieveContainer(t.Context(), binding, created.ID); !errors.Is(err, ErrContainerDeploymentChanged) {
		t.Fatalf("changed deployment error=%v", err)
	}
}

func TestContainerAdapterRejectsInvalidInputAndResponse(t *testing.T) {
	client := NewOpenAICompatible("https://example.invalid/v1", "", false)
	if _, err := client.CreateContainer(context.Background(), openai.ContainerProviderCreateRequest{Name: "x", MemoryLimit: "2g"}); err == nil {
		t.Fatal("unsupported memory accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"bad/id","object":"container"}`)
	}))
	defer server.Close()
	client = NewOpenAICompatible(server.URL+"/v1", "", false)
	client.client = server.Client()
	if _, err := client.RetrieveContainer(t.Context(), "cntr_1"); err == nil {
		t.Fatal("invalid response accepted")
	}
}

func TestContainerAdapterBoundsMetadataAndDisablesRedirects(t *testing.T) {
	t.Run("oversized response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", maxContainerMetadataBytes+1))
		}))
		defer server.Close()
		client := NewOpenAICompatible(server.URL+"/v1", "", false)
		client.client = server.Client()
		if _, err := client.RetrieveContainer(t.Context(), "cntr_1"); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		redirected := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/target" {
				redirected = true
				_, _ = io.WriteString(w, containerFixture)
				return
			}
			http.Redirect(w, r, "/target", http.StatusFound)
		}))
		defer server.Close()
		client := NewOpenAICompatible(server.URL+"/v1", "", false)
		client.client = server.Client()
		if _, err := client.RetrieveContainer(t.Context(), "cntr_1"); err == nil || redirected {
			t.Fatalf("error=%v redirected=%t", err, redirected)
		}
	})
}
