package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestOpenAICompatibleAudioSpeechContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/speech" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["provider"] != nil || payload["model"] != "tts-upstream" || payload["input"] != "hello" || payload["voice"] != "alloy" || payload["response_format"] != "mp3" || payload["stream_format"] != "audio" {
			t.Fatalf("payload=%v", payload)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3audio"))
	}))
	defer server.Close()
	request := openai.AudioSpeechRequest{Provider: "deployment", Model: "tts-upstream", Input: "hello", Voice: "alloy", ResponseFormat: "mp3", StreamFormat: "audio"}
	response, err := NewOpenAICompatible(server.URL+"/v1", "secret", false).GenerateSpeech(t.Context(), request)
	if err != nil || response.ContentType != "audio/mpeg" || !bytes.Equal(response.Data, []byte("ID3audio")) {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestAzureAudioSpeechContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/audio/speech" || r.URL.Query().Get("api-version") != "2025-04-01-preview" || r.Header.Get("api-key") != "secret" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected Azure request: %s headers=%v", r.URL.String(), r.Header)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3audio"))
	}))
	defer server.Close()
	response, err := NewAzureOpenAI(server.URL, "secret", false, "2025-04-01-preview", "api_key").GenerateSpeech(t.Context(), openai.AudioSpeechRequest{Model: "tts", Input: "hello", Voice: "alloy"})
	if err != nil || response.ContentType != "audio/mpeg" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestRouterAudioSpeechRequiresCapabilityAndAppliesAlias(t *testing.T) {
	var model string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		model = payload.Model
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFFaudio"))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "speech", Type: "openai-compatible", BaseURL: server.URL, Models: []string{"public-tts"}, ModelAliases: map[string]string{"public-tts": "upstream-tts"}, Capabilities: []string{"audio_speech"}}}}).(*Router)
	request := openai.AudioSpeechRequest{Model: "public-tts", Input: "hello", Voice: "alloy", ResponseFormat: "wav"}
	response, err := router.GenerateSpeech(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model, Messages: []openai.Message{{Role: "user", Content: request.Input}}}, AudioSpeechRequest: &request})
	if err != nil || model != "upstream-tts" || response.ContentType != "audio/wav" {
		t.Fatalf("response=%+v model=%q err=%v", response, model, err)
	}
	missing := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "speech", Type: "openai-compatible", BaseURL: server.URL, Models: []string{"public-tts"}, Capabilities: []string{"chat"}}}}).(*Router)
	if _, err := missing.GenerateSpeech(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, AudioSpeechRequest: &request}); err == nil {
		t.Fatal("endpoint without audio_speech capability was selected")
	}
}

type speechLimitReader struct{ read int }

func (r *speechLimitReader) Read(buffer []byte) (int, error) {
	remaining := maxAudioSpeechResponseBytes + 1 - r.read
	if remaining <= 0 {
		return 0, io.EOF
	}
	if len(buffer) > remaining {
		buffer = buffer[:remaining]
	}
	for index := range buffer {
		buffer[index] = 'x'
	}
	r.read += len(buffer)
	return len(buffer), nil
}

func TestAudioSpeechRejectsInvalidAndOversizedResponses(t *testing.T) {
	for _, contentType := range []string{"", "application/json", "invalid/type;"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write([]byte("payload"))
		}))
		_, err := NewOpenAICompatible(server.URL, "", false).GenerateSpeech(t.Context(), openai.AudioSpeechRequest{Model: "tts", Input: "hello", Voice: "alloy"})
		server.Close()
		if err == nil {
			t.Fatalf("accepted content type %q", contentType)
		}
	}
	reader := &speechLimitReader{}
	if _, err := readAudioSpeechResponse(reader); err == nil || reader.read != maxAudioSpeechResponseBytes+1 {
		t.Fatalf("oversized response was accepted: bytes=%d err=%v", reader.read, err)
	}
	if _, err := readAudioSpeechResponse(bytes.NewReader(nil)); err == nil {
		t.Fatal("empty response was accepted")
	}
}

func TestMistralAudioSpeechRejectedBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	_, err := NewMistral(server.URL, "secret", false).GenerateSpeech(context.Background(), openai.AudioSpeechRequest{Model: "tts", Input: "hello", Voice: "alloy"})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.UpstreamCode != "unsupported_operation" || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
