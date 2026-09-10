package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestOpenRouterExposesOnlyImplementedOperations(t *testing.T) {
	client := any(NewOpenRouter("https://openrouter.example/v1", "", true, ""))
	for name, supported := range map[string]bool{
		"completion":          implements[CompletionClient](client),
		"embedding":           implements[EmbeddingClient](client),
		"rerank":              implements[RerankClient](client),
		"image_generation":    implements[ImageGenerationClient](client),
		"audio_transcription": implements[AudioTranscriptionClient](client),
		"audio_speech":        implements[AudioSpeechClient](client),
	} {
		if !supported {
			t.Errorf("expected %s support", name)
		}
	}
	for name, supported := range map[string]bool{
		"moderation":      implements[ModerationClient](client),
		"image_edit":      implements[ImageEditClient](client),
		"image_variation": implements[ImageVariationClient](client),
		"search":          implements[SearchClient](client),
		"mcp":             implements[MCPClient](client),
	} {
		if supported {
			t.Errorf("unexpected %s support", name)
		}
	}
}

func implements[T any](value any) bool {
	_, ok := value.(T)
	return ok
}

func TestOpenRouterImageGenerationUsesDedicatedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/images" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["provider"] != nil || request["prompt"] != "draw" || request["user"] != "tenant-user" || request["n"] != float64(2) {
			t.Fatalf("request=%#v err=%v", request, err)
		}
		_, _ = fmt.Fprint(w, `{"created":7,"data":[{"b64_json":"aW1hZ2U=","media_type":"image/png"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8,"cost":0.04}}`)
	}))
	defer server.Close()
	n := 2
	response, err := NewOpenRouter(server.URL+"/api/v1", "secret", false, "").GenerateImage(context.Background(), openai.ImageGenerationRequest{Model: "image", Prompt: "draw", N: &n, User: "tenant-user"})
	if err != nil || response.Created != 7 || len(response.Data) != 1 || response.Usage == nil || *response.Usage != (openai.ImageUsage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8}) {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestOpenRouterPreservesServiceTier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["service_tier"] != "priority" {
			t.Fatalf("request=%#v err=%v", request, err)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	response, err := NewOpenRouter(server.URL+"/v1", "", false, "").ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hi"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "priority"},
	})
	if err != nil || response.ID != "chat" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestOpenRouterRejectsUnsupportedOperationParametersBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	client := NewOpenRouter(server.URL, "secret", false, "")
	requests := []func() error{
		func() error {
			_, err := client.GenerateImage(t.Context(), openai.ImageGenerationRequest{Model: "image", Prompt: "draw", Style: "vivid"})
			return err
		},
		func() error {
			_, err := client.Rerank(t.Context(), openai.RerankRequest{Model: "rerank", Query: "q", Documents: []any{"d"}, RankFields: []string{"text"}})
			return err
		},
		func() error {
			_, err := client.GenerateSpeech(t.Context(), openai.AudioSpeechRequest{Model: "speech", Input: "hi", Voice: "alloy", Instructions: "whisper"})
			return err
		},
	}
	for _, call := range requests {
		var providerErr *Error
		if err := call(); !errors.As(err, &providerErr) || providerErr.Provider != "openrouter" || providerErr.UpstreamCode != "unsupported_parameter" {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("unsupported requests reached network: %d", calls)
	}
}
