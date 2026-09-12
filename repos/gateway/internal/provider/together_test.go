package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestTogetherInferenceContracts(t *testing.T) {
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path]++
		if r.Header.Get("Authorization") != "Bearer together-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		if r.Method == http.MethodPost {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "model" {
				t.Fatalf("path=%q body=%#v", r.URL.Path, body)
			}
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		case "/v1/completions":
			_, _ = fmt.Fprint(w, `{"id":"completion","object":"text_completion","model":"model","choices":[{"index":0,"text":"ok","finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		case "/v1/embeddings":
			_, _ = fmt.Fprint(w, `{"object":"list","model":"model","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
		case "/v1/rerank":
			_, _ = fmt.Fprint(w, `{"id":"rerank","results":[{"index":0,"relevance_score":0.9}],"usage":{"prompt_tokens":7,"completion_tokens":0,"total_tokens":7}}`)
		case "/v1/models":
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"model-b"},{"id":"model-a"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewTogether(server.URL, "together-key", true)
	chat, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || chat.Usage.TotalTokens != 3 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	completion, err := client.Completions(t.Context(), openai.CompletionRequest{Model: "model", Prompt: "hello"})
	if err != nil || completion.Usage.TotalTokens != 3 {
		t.Fatalf("completion=%+v err=%v", completion, err)
	}
	embedding, err := client.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "model", Input: "hello"})
	if err != nil || embedding.Usage.TotalTokens != 2 || len(embedding.Data) != 1 {
		t.Fatalf("embedding=%+v err=%v", embedding, err)
	}
	topN := 1
	reranked, err := client.Rerank(t.Context(), openai.RerankRequest{Model: "model", Query: "query", Documents: []any{"document"}, TopN: &topN})
	if err != nil || len(reranked.Results) != 1 || reranked.Meta == nil || reranked.Meta.Tokens == nil || reranked.Meta.Tokens.InputTokens != 7 {
		t.Fatalf("rerank=%+v err=%v", reranked, err)
	}
	router := New(Config{CredentialEncryptionKey: []byte("together-provider-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "together", Type: "together", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "together-key", ProviderID: "together", Secret: "together-key"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "together", "together-key")
	if err != nil || len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-b" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/rerank", "/v1/models"} {
		if seen[path] != 1 {
			t.Fatalf("path %s called %d times", path, seen[path])
		}
	}
}

func TestTogetherCapabilityProfileIsBounded(t *testing.T) {
	for _, profile := range ManagedProviderCapabilityProfiles() {
		if profile.Type != "together" {
			continue
		}
		if !slices.Equal(profile.Operations, []string{"chat", "completions", "embeddings", "rerank", "audio_speech", "stream"}) || !slices.Equal(profile.Capabilities, []string{"chat", "completions", "embeddings", "rerank", "audio_speech", "stream", "tools", "structured_output", "vision"}) || len(profile.AuthTypes) != 0 {
			t.Fatalf("profile=%+v", profile)
		}
		if slices.Contains(profile.ChatParameters.SupportedOptions, "store") || slices.Contains(profile.ChatParameters.SupportedOptions, "metadata") || slices.Contains(profile.ChatParameters.SupportedOptions, "service_tier") || slices.Contains(profile.ChatParameters.SupportedOptions, "prediction") || slices.Contains(profile.ChatParameters.SupportedOptions, "logprobs") || slices.Contains(profile.ChatParameters.SupportedOptions, "logit_bias") || len(profile.ChatParameters.Logprobs) != 0 {
			t.Fatalf("ignored options advertised: %+v", profile.ChatParameters)
		}
		return
	}
	t.Fatal("Together capability profile is missing")
}

func TestTogetherAudioSpeechContract(t *testing.T) {
	tests := []struct {
		name          string
		request       openai.AudioSpeechRequest
		wantFormat    string
		wantMediaType string
	}{
		{name: "default mp3", request: openai.AudioSpeechRequest{Model: "speech", Input: "hello", Voice: "voice"}, wantFormat: "mp3", wantMediaType: "audio/mpeg"},
		{name: "raw pcm with language", request: openai.AudioSpeechRequest{Model: "speech", Input: "hello", Voice: "voice", Language: "en-us", ResponseFormat: "pcm", StreamFormat: "audio"}, wantFormat: "raw", wantMediaType: "application/octet-stream"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/speech" || r.Header.Get("Authorization") != "Bearer together-key" {
					t.Fatalf("method=%s path=%s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
				}
				var body struct {
					Model          string `json:"model"`
					Input          string `json:"input"`
					Voice          string `json:"voice"`
					Language       string `json:"language"`
					ResponseFormat string `json:"response_format"`
					Stream         bool   `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Model != test.request.Model || body.Input != test.request.Input || body.Voice != test.request.Voice || body.Language != test.request.Language || body.ResponseFormat != test.wantFormat || body.Stream {
					t.Fatalf("body=%+v", body)
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write([]byte("audio"))
			}))
			defer server.Close()

			response, err := NewTogether(server.URL, "together-key", false).GenerateSpeech(t.Context(), test.request)
			if err != nil || string(response.Data) != "audio" || response.Model != test.request.Model || response.ContentType != test.wantMediaType {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
}

func TestTogetherAudioSpeechRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	speed := 1.0
	tests := []struct {
		param string
		apply func(*openai.AudioSpeechRequest)
	}{
		{param: "language", apply: func(r *openai.AudioSpeechRequest) { r.Language = "auto" }},
		{param: "language", apply: func(r *openai.AudioSpeechRequest) { r.Language = "en-US" }},
		{param: "instructions", apply: func(r *openai.AudioSpeechRequest) { r.Instructions = "warmly" }},
		{param: "response_format", apply: func(r *openai.AudioSpeechRequest) { r.ResponseFormat = "opus" }},
		{param: "speed", apply: func(r *openai.AudioSpeechRequest) { r.Speed = &speed }},
		{param: "stream_format", apply: func(r *openai.AudioSpeechRequest) { r.StreamFormat = "sse" }},
	}
	for _, test := range tests {
		t.Run(test.param, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer server.Close()
			request := openai.AudioSpeechRequest{Model: "speech", Input: "hello", Voice: "voice"}
			test.apply(&request)

			_, err := NewTogether(server.URL, "key", false).GenerateSpeech(t.Context(), request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Param != test.param || failure.UpstreamCode != "unsupported_parameter" || called {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}

func TestTogetherRejectsIgnoredParameterBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	store := true
	_, err := NewTogether(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Store: &store}})
	if err == nil || !strings.Contains(err.Error(), "store") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestTogetherRejectsUnsupportedRerankParameterBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()

	_, err := NewTogether(server.URL, "key", false).Rerank(t.Context(), openai.RerankRequest{
		Model:      "model",
		Query:      "query",
		Documents:  []any{"document"},
		RankFields: []string{"title"},
	})
	if err == nil || !strings.Contains(err.Error(), "rank_fields") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestTogetherRerankRequiresExactUsage(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing", body: `{"id":"rerank","results":[]}`},
		{name: "negative", body: `{"id":"rerank","results":[],"usage":{"prompt_tokens":-1,"completion_tokens":0,"total_tokens":-1}}`},
		{name: "inconsistent total", body: `{"id":"rerank","results":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":2}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()

			_, err := NewTogether(server.URL, "key", false).Rerank(t.Context(), openai.RerankRequest{Model: "model", Query: "query", Documents: []any{"document"}})
			if err == nil || !strings.Contains(err.Error(), "usage") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestTogetherRerankAcceptsObjectDocuments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Documents []map[string]any `json:"documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Documents) != 1 || body.Documents[0]["title"] != "document" {
			t.Fatalf("documents=%#v", body.Documents)
		}
		_, _ = fmt.Fprint(w, `{"id":"rerank","results":[{"index":0,"relevance_score":0.9,"document":{"title":"document"}}],"usage":{"prompt_tokens":4,"completion_tokens":0,"total_tokens":4}}`)
	}))
	defer server.Close()

	response, err := NewTogether(server.URL, "key", false).Rerank(t.Context(), openai.RerankRequest{
		Model:     "model",
		Query:     "query",
		Documents: []any{map[string]any{"title": "document"}},
	})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document == nil {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestTogetherRerankRejectsTrailingAndOversizedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "trailing", body: `{"id":"rerank","results":[],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}} {}`},
		{name: "oversized", body: strings.Repeat(" ", (8<<20)+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()

			_, err := NewTogether(server.URL, "key", false).Rerank(t.Context(), openai.RerankRequest{Model: "model", Query: "query", Documents: []any{"document"}})
			if err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}
