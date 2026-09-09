package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/openai"
)

func TestModelDeploymentUpdateChangesRuntimeCandidates(t *testing.T) {
	enabled := true
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "primary", Type: "demo", Models: []string{"old"}, Enabled: &enabled, Priority: 10, Weight: 1}}}).(*Router)
	list := router.ListModelDeployments(context.Background())
	if len(list) != 1 || !list[0].Enabled || list[0].ProviderType != "demo" {
		t.Fatalf("unsafe or incomplete deployments: %+v", list)
	}
	saved, err := router.UpdateModelDeployment("primary", ModelDeployment{Models: []string{"new"}, Capabilities: []string{"chat"}, Priority: 1, Weight: 5, Enabled: true})
	if err != nil || saved.ProviderType != "demo" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	endpoints := router.runtimeEndpoints()
	if len(endpoints) != 1 || endpoints[0].Models[0] != "new" || endpoints[0].Weight != 5 {
		t.Fatalf("runtime override not applied: %+v", endpoints)
	}
	_, err = router.UpdateModelDeployment("primary", ModelDeployment{Models: []string{"new"}, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if endpoints := router.runtimeEndpoints(); len(endpoints) != 0 {
		t.Fatalf("disabled deployment remained routable: %+v", endpoints)
	}
	if diagnostics := router.Diagnostics(context.Background()); len(diagnostics.Endpoints) != 0 {
		t.Fatalf("disabled deployment leaked into active diagnostics: %+v", diagnostics)
	}
}

func TestDeploymentCapabilitiesAcceptImageOperations(t *testing.T) {
	if !validDeploymentCapabilities([]string{"image_generation", "image_edit", "image_variation"}) {
		t.Fatal("image capabilities were rejected")
	}
}

func TestManagedDeploymentEnablesNativeStreaming(t *testing.T) {
	var streamRequested atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		streamRequested.Store(body.Stream)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"native\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "native", Type: "openai-compatible", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		capabilities := []string{"chat"}
		if stream {
			capabilities = append(capabilities, "stream")
		}
		endpoint, err := router.endpointForDeployment(ModelDeployment{ProviderID: "native", Models: []string{"test"}, Capabilities: capabilities})
		if err != nil {
			t.Fatal(err)
		}
		client := endpoint.Provider.(StreamingClient)
		_, err = client.StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "test"}, func(string) error { return nil })
		if stream {
			if err != nil || !streamRequested.Load() {
				t.Fatalf("native stream not enabled: %v", err)
			}
		} else if !errors.Is(err, ErrStreamingUnsupported) {
			t.Fatalf("stream unexpectedly enabled: %v", err)
		}
	}
}
