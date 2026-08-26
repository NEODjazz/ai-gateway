package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/config"
)

func TestDeploymentHealthCheckUsesConfiguredCredentialAndUpstreamModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer probe-secret" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"probe","object":"chat.completion","model":"upstream-model","choices":[]}`))
	}))
	defer upstream.Close()

	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "remote", Type: "openai-compatible", BaseURL: upstream.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "remote-key", ProviderID: "remote", Secret: "probe-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateModelDeployment(ModelDeployment{ID: "deployment", ProviderID: "remote", CredentialID: "remote-key", UpstreamModel: "upstream-model", Models: []string{"public-model"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	check, err := router.TestModelDeployment(context.Background(), "deployment")
	if err != nil {
		t.Fatal(err)
	}
	if check.Status != "available" || check.Model != "upstream-model" || check.ProviderID != "remote" {
		t.Fatalf("unexpected check: %+v", check)
	}
	history, err := router.ListModelDeploymentHealth(context.Background(), "deployment", 10)
	if err != nil || len(history) != 1 || history[0].Status != "available" {
		t.Fatalf("unexpected history: %+v err=%v", history, err)
	}
}

func TestDeploymentHealthCheckKeepsOnlySafeFailureMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"secret request body user@example.com","code":"unsupported_parameter","param":"temperature"}}`))
	}))
	defer upstream.Close()

	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "remote", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"model"}}}}).(*Router)
	check, err := router.TestModelDeployment(context.Background(), "remote")
	if err != nil {
		t.Fatal(err)
	}
	if check.Status != "unavailable" || check.FailureClass != "client_request" || check.HTTPStatus != http.StatusBadRequest || check.UpstreamCode != "unsupported_parameter" || check.Param != "temperature" {
		t.Fatalf("unexpected check: %+v", check)
	}
	if _, err := router.ListModelDeploymentHealth(context.Background(), "missing", 10); !errors.Is(err, ErrDeploymentNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}
