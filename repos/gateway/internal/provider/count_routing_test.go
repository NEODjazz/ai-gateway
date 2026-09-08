package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestTokenCountUsesDeploymentAdmission(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"input_tokens":2}`))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "anthropic", BaseURL: server.URL, Models: []string{"model"}, MaxParallelRequests: 1}}}).(*Router)
	release, err := router.endpoints[0].Admission.acquire(context.Background(), "native")
	if err != nil {
		t.Fatal(err)
	}
	request := modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hi"}}}}
	_, err = router.CountTokens(context.Background(), request)
	var admission *AdmissionError
	if !errors.As(err, &admission) || calls.Load() != 0 {
		release()
		t.Fatalf("admission bypassed: %v calls=%d", err, calls.Load())
	}
	release()
	result, err := router.CountTokens(context.Background(), request)
	if err != nil || result.InputTokens != 2 || calls.Load() != 1 {
		t.Fatalf("count after release: %+v %v", result, err)
	}
}
func TestTokenCountRejectsUnsupportedAdapterBeforePolicy(t *testing.T) {
	spy := &countPrePolicy{}
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "unsupported", Type: "openai-compatible", BaseURL: "http://unused.invalid", Models: []string{"model"}}}, Modules: modules.NewPipeline([]modules.Module{spy})}).(*Router)
	_, err := router.CountTokens(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hi"}}}})
	assertUnsupportedParameter(t, err, "count_tokens")
	if spy.calls != 0 {
		t.Fatal("unsupported adapter ran policy modules")
	}
}

type countPrePolicy struct{ stopAdmissionModule }

func (*countPrePolicy) Name() string { return "dlp" }
