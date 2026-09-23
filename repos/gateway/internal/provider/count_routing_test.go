package provider

import (
	"context"
	"encoding/json"
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

func TestTokenCountPreservesGeminiMediaResolution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/model:countTokens" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var body struct {
			Request struct {
				Generation geminiGeneration `json:"generationConfig"`
			} `json:"generateContentRequest"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Request.Generation.MediaResolution != "MEDIA_RESOLUTION_HIGH" {
			t.Errorf("media resolution=%q", body.Request.Generation.MediaResolution)
		}
		_, _ = w.Write([]byte(`{"totalTokens":1120}`))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "gemini", Type: "gemini", BaseURL: server.URL, Models: []string{"model"},
		Capabilities: []string{"chat", "vision", "gemini_media_resolution"}, AVEnabled: true,
	}}}).(*Router)
	result, err := router.CountTokens(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{
		Model: "model",
		Messages: []openai.Message{{Role: "user", Content: []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
		}}},
		GeminiMediaResolution: "MEDIA_RESOLUTION_HIGH",
	}})
	if err != nil || result.InputTokens != 1120 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestTokenCountPreservesGeminiMediaProcessing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Request struct {
				Contents []geminiContent `json:"contents"`
			} `json:"generateContentRequest"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if got := body.Request.Contents[0].Parts[0].MediaProcessing; got != "AGENTIC" {
			t.Errorf("media processing=%q", got)
		}
		_, _ = w.Write([]byte(`{"totalTokens":240}`))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "gemini", Type: "gemini", BaseURL: server.URL, Models: []string{"model"},
		Capabilities: []string{"chat", "video_input", "gemini_media_processing"}, AVEnabled: true,
	}}}).(*Router)
	result, err := router.CountTokens(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: []any{
			map[string]any{"type": "input_video", "input_video": map[string]any{"data": "AAAADGZ0eXBtcDQy", "format": "mp4"}, "gemini_media_processing": "AGENTIC"},
		}}},
	}})
	if err != nil || result.InputTokens != 240 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestTokenCountPreservesGeminiFileSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Request struct {
				Tools []geminiTool `json:"tools"`
			} `json:"generateContentRequest"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Request.Tools) != 1 || body.Request.Tools[0].FileSearch == nil || body.Request.Tools[0].FileSearch.StoreNames[0] != "fileSearchStores/policies" {
			t.Errorf("file search tool lost: %+v", body.Request.Tools)
		}
		_, _ = w.Write([]byte(`{"totalTokens":48}`))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "gemini", Type: "gemini", BaseURL: server.URL, Models: []string{"model"}, Capabilities: []string{"chat", "gemini_file_search"},
	}}}).(*Router)
	result, err := router.CountTokens(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "find policy"}}, GeminiFileSearch: &openai.GeminiFileSearchConfig{StoreNames: []string{"fileSearchStores/policies"}},
	}})
	if err != nil || result.InputTokens != 48 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type countPrePolicy struct{ stopAdmissionModule }

func (*countPrePolicy) Name() string { return "dlp" }
