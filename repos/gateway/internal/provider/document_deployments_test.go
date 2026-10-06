package provider

import (
	"errors"
	"testing"

	"ai-gateway-gateway/internal/config"
)

func TestDeploymentDocumentProcessingPolicy(t *testing.T) {
	r := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "local", Type: "ollama", Models: []string{"qwen"}, Capabilities: []string{"chat", "responses"}}}}).(*Router)
	for _, mode := range []string{"", "native", "docling"} {
		saved, err := r.UpdateModelDeployment("local", ModelDeployment{Models: []string{"qwen"}, Capabilities: []string{"chat", "responses"}, DocumentProcessing: mode, Enabled: true})
		if err != nil || saved.DocumentProcessing != mode {
			t.Fatalf("mode=%q saved=%+v err=%v", mode, saved, err)
		}
		if got := r.runtimeEndpoints()[0].DocumentProcessing; got != mode {
			t.Fatalf("runtime mode=%q want=%q", got, mode)
		}
	}
	// Old management clients must not reset a configured processing policy.
	saved, err := r.UpdateModelDeployment("local", ModelDeployment{Models: []string{"qwen"}, Capabilities: []string{"chat", "responses"}, Enabled: true})
	if err != nil || saved.DocumentProcessing != "docling" {
		t.Fatalf("omitted policy lost: saved=%+v err=%v", saved, err)
	}
	for _, mode := range []string{"Docling", "unknown"} {
		_, err := r.UpdateModelDeployment("local", ModelDeployment{Models: []string{"qwen"}, Capabilities: []string{"chat"}, DocumentProcessing: mode})
		if !errors.Is(err, ErrInvalidDeployment) {
			t.Fatalf("invalid mode %q: %v", mode, err)
		}
	}
	_, err = r.UpdateModelDeployment("local", ModelDeployment{Models: []string{"qwen"}, Capabilities: []string{"chat", "file_input"}, DocumentProcessing: "docling"})
	if !errors.Is(err, ErrUnsupportedProviderCapability) {
		t.Fatalf("gateway conversion must not enable native file_input: %v", err)
	}
	if sameDeploymentRuntime(ModelDeployment{DocumentProcessing: "native"}, ModelDeployment{DocumentProcessing: "docling"}) {
		t.Fatal("processing changes reused old runtime")
	}
}

func TestDocumentProcessingPolicyPersists(t *testing.T) {
	store := &memoryControlPlaneStore{}
	r, err := NewWithError(Config{ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	first := r.(*Router)
	if _, err := first.CreateProvider(ManagedProvider{ID: "local", Type: "ollama", BaseURL: "http://localhost:11434", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateModelDeployment(ModelDeployment{ID: "qwen", ProviderID: "local", Models: []string{"qwen"}, Capabilities: []string{"chat"}, DocumentProcessing: "docling", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	restored, err := NewWithError(Config{ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.(*Router).ListModelDeployments(t.Context()); len(got) != 1 || got[0].DocumentProcessing != "docling" {
		t.Fatalf("restored=%+v", got)
	}
	store.snapshot.Deployments[0].DocumentProcessing = "invalid"
	if _, err := NewWithError(Config{ControlPlaneStore: store}); err == nil {
		t.Fatal("invalid persisted policy was accepted")
	}
}
