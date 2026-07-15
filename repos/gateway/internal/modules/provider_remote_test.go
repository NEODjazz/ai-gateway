package modules

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestProviderRemoteModuleSkipsDisabledProvider(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	module := NewProviderRemoteModule("dlp", true, server.URL+"/scan")
	err := module.Handle(context.Background(), &RequestContext{
		Metadata: map[string]string{"provider.modules.dlp.enabled": "false"},
		Request: openai.ChatCompletionRequest{
			Messages: []openai.Message{{Role: "user", Content: "secret"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("remote module should not be called for disabled provider")
	}
}

func TestProviderRemoteModuleRequiresURLWhenEnabled(t *testing.T) {
	module := NewProviderRemoteModule("av", true, "")
	err := module.Handle(context.Background(), &RequestContext{
		Metadata: map[string]string{"provider.modules.av.enabled": "true"},
		Request: openai.ChatCompletionRequest{
			Messages: []openai.Message{{Role: "user", Content: "secret"}},
		},
	})
	if err == nil {
		t.Fatal("expected missing url error")
	}
}

func TestRemoteModuleMapsContentRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnavailableForLegalReasons)
	}))
	defer server.Close()

	module := NewRemoteModule("dlp", false, server.URL)
	err := module.Handle(context.Background(), &RequestContext{})
	if !errors.Is(err, ErrContentRejected) {
		t.Fatalf("expected content rejected error, got %v", err)
	}
}

func TestPipelineStopsOnContentRejectedEvenWhenOptional(t *testing.T) {
	pipeline := NewPipeline([]Module{
		rejectingModule{},
	})

	err := pipeline.Run(context.Background(), &RequestContext{})
	if !errors.Is(err, ErrContentRejected) {
		t.Fatalf("expected content rejected error, got %v", err)
	}
}

type rejectingModule struct{}

func (rejectingModule) Name() string {
	return "optional-rejecting"
}

func (rejectingModule) Required() bool {
	return false
}

func (rejectingModule) Handle(context.Context, *RequestContext) error {
	return ErrContentRejected
}
