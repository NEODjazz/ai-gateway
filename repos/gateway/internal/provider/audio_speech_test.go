package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestOpenAICompatibleAudioSpeechSSEContract(t *testing.T) {
	audio := base64.StdEncoding.EncodeToString([]byte("ID3audio"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["stream_format"] != "sse" || payload["model"] != "tts" {
			t.Fatalf("payload=%v", payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"speech.audio.delta\",\"audio\":\""+audio+"\"}\n\ndata: {\"type\":\"speech.audio.done\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}\n\n")
	}))
	defer server.Close()
	request := openai.AudioSpeechRequest{Model: "tts", Input: "hello", Voice: "alloy", ResponseFormat: "mp3", StreamFormat: "sse"}
	var events []string
	response, err := NewOpenAICompatible(server.URL, "", true).StreamGenerateSpeech(t.Context(), request, func(payload string) error {
		events = append(events, payload)
		return nil
	})
	if err != nil || len(events) != 2 || response.Model != "tts" || response.ContentType != "audio/mpeg" || response.Usage == nil || response.Usage.TotalTokens != 5 || len(response.Data) != 0 {
		t.Fatalf("response=%+v events=%v err=%v", response, events, err)
	}
}

func TestAudioSpeechSSERejectsInvalidLifecycle(t *testing.T) {
	audio := base64.StdEncoding.EncodeToString([]byte("audio"))
	for name, body := range map[string]string{
		"missing done":   "data: {\"type\":\"speech.audio.delta\",\"audio\":\"" + audio + "\"}\n\n",
		"invalid base64": "data: {\"type\":\"speech.audio.delta\",\"audio\":\"%%%\"}\n\n",
		"done first":     "data: {\"type\":\"speech.audio.done\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}\n\n",
		"invalid usage":  "data: {\"type\":\"speech.audio.delta\",\"audio\":\"" + audio + "\"}\n\ndata: {\"type\":\"speech.audio.done\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":3}}\n\n",
		"after done":     "data: {\"type\":\"speech.audio.delta\",\"audio\":\"" + audio + "\"}\n\ndata: {\"type\":\"speech.audio.done\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}\n\ndata: {\"type\":\"speech.audio.delta\",\"audio\":\"" + audio + "\"}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := streamAudioSpeech(strings.NewReader(body), func(string) error { return nil }); err == nil {
				t.Fatal("invalid stream accepted")
			}
		})
	}
}

type speechStreamLifecycleRecorder struct{ characters, tokens int }

func (*speechStreamLifecycleRecorder) Name() string   { return "speech-stream-recorder" }
func (*speechStreamLifecycleRecorder) Required() bool { return true }
func (m *speechStreamLifecycleRecorder) Handle(_ context.Context, req *modules.RequestContext) error {
	m.characters = req.AudioSpeechRequest.InputCharacters()
	return nil
}
func (*speechStreamLifecycleRecorder) PostResponseEnabled() bool { return true }
func (m *speechStreamLifecycleRecorder) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	if req.AudioSpeechResponse != nil && req.AudioSpeechResponse.Usage != nil {
		m.tokens = req.AudioSpeechResponse.Usage.TotalTokens
	}
	return nil
}

func TestRouterAudioSpeechSSESettlesUsageAndStopsFallbackAfterData(t *testing.T) {
	audio := base64.StdEncoding.EncodeToString([]byte("audio"))
	secondCalls := 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"speech.audio.delta\",\"audio\":\""+audio+"\"}\n\ndata: {\"type\":\"speech.audio.done\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":3}}\n\n")
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"speech.audio.delta\",\"audio\":\""+audio+"\"}\n\ndata: {\"type\":\"speech.audio.done\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}\n\n")
	}))
	defer second.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "first", Type: "openai-compatible", BaseURL: first.URL, Stream: true, Priority: 1, Models: []string{"tts"}, Capabilities: []string{"audio_speech"}},
		{Name: "second", Type: "openai-compatible", BaseURL: second.URL, Stream: true, Priority: 2, Models: []string{"tts"}, Capabilities: []string{"audio_speech"}},
	}}).(*Router)
	request := openai.AudioSpeechRequest{Model: "tts", Input: "hello", Voice: "alloy", StreamFormat: "sse"}
	ctx := modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "tts", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, AudioSpeechRequest: &request}
	_, streamed, err := router.StreamGenerateSpeech(t.Context(), ctx, func(string) error { return nil })
	if err == nil || !streamed || secondCalls != 0 {
		t.Fatalf("streamed=%v second_calls=%d err=%v", streamed, secondCalls, err)
	}

	recorder := &speechStreamLifecycleRecorder{}
	success := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"speech.audio.delta\",\"audio\":\""+audio+"\"}\n\ndata: {\"type\":\"speech.audio.done\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}\n\n")
	}))
	defer success.Close()
	router = New(Config{Modules: modules.NewPipeline([]modules.Module{recorder}), Endpoints: []config.ProviderEndpointConfig{{Name: "success", Type: "openai-compatible", BaseURL: success.URL, Stream: true, Models: []string{"tts"}, Capabilities: []string{"audio_speech"}}}}).(*Router)
	response, streamed, err := router.StreamGenerateSpeech(t.Context(), ctx, func(string) error { return nil })
	if err != nil || !streamed || response.Usage == nil || recorder.characters != 5 || recorder.tokens != 5 {
		t.Fatalf("response=%+v streamed=%v recorder=%+v err=%v", response, streamed, recorder, err)
	}
}

func TestRouterAudioSpeechSSEFallsBackBeforeFirstEvent(t *testing.T) {
	firstCalls, secondCalls := 0, 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer first.Close()
	audio := base64.StdEncoding.EncodeToString([]byte("audio"))
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"speech.audio.delta\",\"audio\":\""+audio+"\"}\n\ndata: {\"type\":\"speech.audio.done\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}\n\n")
	}))
	defer second.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "first", Type: "openai-compatible", BaseURL: first.URL, Stream: true, Priority: 1, Models: []string{"tts"}, Capabilities: []string{"audio_speech"}},
		{Name: "second", Type: "openai-compatible", BaseURL: second.URL, Stream: true, Priority: 2, Models: []string{"tts"}, Capabilities: []string{"audio_speech"}},
	}}).(*Router)
	request := openai.AudioSpeechRequest{Model: "tts", Input: "hello", Voice: "alloy", StreamFormat: "sse"}
	ctx := modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "tts", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, AudioSpeechRequest: &request}
	response, streamed, err := router.StreamGenerateSpeech(t.Context(), ctx, func(string) error { return nil })
	if err != nil || !streamed || firstCalls != 1 || secondCalls != 1 || response.Usage == nil || response.Usage.TotalTokens != 2 {
		t.Fatalf("response=%+v streamed=%v calls=%d/%d err=%v", response, streamed, firstCalls, secondCalls, err)
	}
}

func TestOpenAICompatibleAudioSpeechRejectsLanguageBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	_, err := NewOpenAICompatible(server.URL, "", false).GenerateSpeech(t.Context(), openai.AudioSpeechRequest{Model: "tts", Input: "hello", Voice: "alloy", Language: "en"})
	var failure *Error
	if !errors.As(err, &failure) || failure.Param != "language" || failure.UpstreamCode != "unsupported_parameter" || called {
		t.Fatalf("err=%v called=%v", err, called)
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
