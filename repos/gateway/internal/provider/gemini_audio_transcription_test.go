package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestGeminiAudioTranscriptionContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiRequest
		if r.URL.Path != "/v1beta/models/gemini-transcribe:generateContent" || r.Header.Get("x-goog-api-key") != "secret" || json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatalf("request=%s headers=%v body=%+v", r.URL.String(), r.Header, body)
		}
		parts, cfg := body.Contents[0].Parts, body.Generation.AudioTranscription
		if len(parts) != 2 || parts[1].InlineData == nil || parts[1].InlineData.MIMEType != "audio/wav" || cfg == nil || len(cfg.LanguageCodes) != 2 || cfg.LanguageCodes[0] != "en-US" || len(cfg.CustomVocabulary) != 2 || cfg.Mode != "SMART" || body.Generation.Temperature == nil || *body.Generation.Temperature != 0.2 {
			t.Fatalf("body=%+v", body)
		}
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"thought":true,"text":"internal"},{"text":"hello "},{"text":"world"}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3,"thoughtsTokenCount":2,"totalTokenCount":17}}`)
	}))
	defer server.Close()
	temperature := 0.2
	response, err := NewGemini(server.URL, "secret", false).TranscribeAudio(t.Context(), openai.AudioTranscriptionRequest{Model: "models/gemini-transcribe", File: transcriptionAttachment(), Prompt: "Acme product names", ResponseFormat: "json", Temperature: &temperature, Languages: []string{"en-US", "fr"}, Keywords: []string{"Acme", "Codex"}, Mode: "SMART"})
	if err != nil || response.Text != "hello world" || response.Usage == nil || response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 5 || response.Usage.TotalTokens != 17 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGeminiAudioTranscriptionRejectsUnsupportedParametersBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	tests := []struct {
		name, param string
		mutate      func(*openai.AudioTranscriptionRequest)
	}{
		{"format", "response_format", func(r *openai.AudioTranscriptionRequest) { r.ResponseFormat = "verbose_json" }},
		{"timestamps", "response_format", func(r *openai.AudioTranscriptionRequest) {
			r.ResponseFormat = "verbose_json"
			r.TimestampGranularities = []string{"word"}
		}},
		{"include", "include", func(r *openai.AudioTranscriptionRequest) { r.Include = []string{"logprobs"} }},
		{"chunking", "chunking_strategy", func(r *openai.AudioTranscriptionRequest) {
			r.ChunkingStrategy = &openai.AudioChunkingStrategy{Type: "auto"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := openai.AudioTranscriptionRequest{Model: "model", File: transcriptionAttachment()}
			test.mutate(&request)
			_, err := NewGemini(server.URL, "secret", false).TranscribeAudio(t.Context(), request)
			var providerErr *Error
			if !errors.As(err, &providerErr) || providerErr.UpstreamCode != "unsupported_parameter" || providerErr.Param != test.param {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestRouterRoutesNativeGeminiAudioTranscription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/upstream:generateContent" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "gemini-audio", Type: "gemini", BaseURL: server.URL, Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"audio_transcription"}}}}).(*Router)
	request := openai.AudioTranscriptionRequest{Model: "public", File: transcriptionAttachment()}
	response, err := router.TranscribeAudio(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public"}, AudioTranscriptionRequest: &request})
	if err != nil || response.Usage == nil || response.Usage.TotalTokens != 3 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGeminiAudioTranslationContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiRequest
		if r.URL.Path != "/v1beta/models/gemini-translate:generateContent" || json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatalf("request=%s body=%+v", r.URL.String(), body)
		}
		if len(body.Contents) != 1 || len(body.Contents[0].Parts) != 2 || !strings.Contains(body.Contents[0].Parts[0].Text, "into English") || body.Generation.AudioTranscription != nil || body.Contents[0].Parts[1].InlineData == nil {
			t.Fatalf("body=%+v", body)
		}
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"Good morning"}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2,"totalTokenCount":10}}`)
	}))
	defer server.Close()
	response, err := NewGemini(server.URL, "secret", false).TranslateAudio(t.Context(), openai.AudioTranscriptionRequest{Model: "gemini-translate", File: transcriptionAttachment(), Language: "en", Prompt: "formal names"})
	if err != nil || response.Text != "Good morning" || response.Usage == nil || response.Usage.TotalTokens != 10 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGeminiAudioTranslationRejectsTranscriptionOnlyParametersBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	tests := []struct {
		name, param string
		mutate      func(*openai.AudioTranscriptionRequest)
	}{
		{"language", "language", func(r *openai.AudioTranscriptionRequest) { r.Language = "fr" }},
		{"languages", "languages", func(r *openai.AudioTranscriptionRequest) { r.Languages = []string{"fr"} }},
		{"keywords", "keywords", func(r *openai.AudioTranscriptionRequest) { r.Keywords = []string{"Acme"} }},
		{"mode", "mode", func(r *openai.AudioTranscriptionRequest) { r.Mode = "SMART" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := openai.AudioTranscriptionRequest{Model: "model", File: transcriptionAttachment()}
			test.mutate(&request)
			_, err := NewGemini(server.URL, "secret", false).TranslateAudio(t.Context(), request)
			var providerErr *Error
			if !errors.As(err, &providerErr) || providerErr.UpstreamCode != "unsupported_parameter" || providerErr.Param != test.param {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestRouterRoutesNativeGeminiAudioTranslation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/upstream:generateContent" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "gemini-translation", Type: "gemini", BaseURL: server.URL, Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"audio_translation"}}}}).(*Router)
	request := openai.AudioTranscriptionRequest{Model: "public", File: transcriptionAttachment()}
	response, err := router.TranslateAudio(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public"}, AudioTranscriptionRequest: &request})
	if err != nil || response.Text != "hello" || response.Usage == nil || response.Usage.TotalTokens != 3 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
