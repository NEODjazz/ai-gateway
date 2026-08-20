package modules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestProviderRemoteModuleSkipsDisabledProvider(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(ScanResponse{Allowed: true})
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

func TestScanPayloadIncludesToolArgumentsAndResponseFunctionOutput(t *testing.T) {
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{
		Role: "assistant", ToolCalls: []openai.ToolCall{{Function: openai.FunctionCall{Arguments: `{"email":"user@example.com"}`}}},
	}}}, ResponseRequest: &openai.ResponseRequest{Input: []any{map[string]any{"type": "function_call_output", "output": "secret-result"}}}}
	payload := scanPayload(&req)
	if !strings.Contains(payload, "user@example.com") || !strings.Contains(payload, "secret-result") {
		t.Fatalf("tool content missing from scan projection: %s", payload)
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

	_, err := callRemote[ScanRequest, ScanResponse](context.Background(), newRemoteHTTPClient(), server.URL, ScanRequest{})
	if !errors.Is(err, ErrContentRejected) {
		t.Fatalf("expected content rejected error, got %v", err)
	}
}

func TestProviderRemoteModuleDoesNotSendBearerToken(t *testing.T) {
	const bearer = "super-secret-bearer-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, found := body["api_key"]; found {
			t.Fatal("provider module request must not contain api_key")
		}
		if _, found := body["user_id"]; found {
			t.Fatal("scan module request must not contain identity")
		}
		_ = json.NewEncoder(w).Encode(ScanResponse{Allowed: true})
	}))
	defer server.Close()

	module := NewProviderRemoteModule("dlp", true, server.URL)
	err := module.Handle(context.Background(), &RequestContext{
		APIKey: bearer,
		UserID: "user-1",
		Metadata: map[string]string{
			"provider.modules.dlp.enabled": "true",
		},
		Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: "hello"}}},
	})
	if err != nil {
		t.Fatal(err)
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
