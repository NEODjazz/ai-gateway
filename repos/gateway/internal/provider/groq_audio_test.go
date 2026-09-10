package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestGroqAudioSpeechMapsSupportedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/openai/v1/audio/speech" || r.Header.Get("Authorization") != "Bearer groq-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["provider"] != nil || payload["model"] != "tts-model" || payload["input"] != "hello" || payload["voice"] != "voice" || payload["response_format"] != "wav" || payload["speed"] != 1.25 || payload["instructions"] != nil || payload["stream_format"] != nil {
			t.Fatalf("payload=%#v", payload)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFFaudio"))
	}))
	defer server.Close()

	speed := 1.25
	client := NewGroq(server.URL+"/openai/v1", "groq-key", false)
	response, err := client.GenerateSpeech(t.Context(), openai.AudioSpeechRequest{Provider: "deployment", Model: "tts-model", Input: "hello", Voice: "voice", ResponseFormat: "wav", Speed: &speed})
	if err != nil || response.Model != "tts-model" || response.ContentType != "audio/wav" || !bytes.Equal(response.Data, []byte("RIFFaudio")) {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGroqAudioSpeechRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewGroq(server.URL, "key", false)
	tooSlow := 0.25
	tests := []struct {
		name, param, code string
		request           openai.AudioSpeechRequest
	}{
		{name: "instructions", param: "instructions", code: "unsupported_parameter", request: openai.AudioSpeechRequest{Instructions: "speak softly"}},
		{name: "stream format", param: "stream_format", code: "unsupported_parameter", request: openai.AudioSpeechRequest{StreamFormat: "audio"}},
		{name: "response format", param: "response_format", code: "unsupported_parameter", request: openai.AudioSpeechRequest{ResponseFormat: "opus"}},
		{name: "speed", param: "speed", code: "invalid_request", request: openai.AudioSpeechRequest{Speed: &tooSlow}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.request.Model = "tts-model"
			test.request.Input = "hello"
			test.request.Voice = "voice"
			_, err := client.GenerateSpeech(t.Context(), test.request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Provider != "groq" || failure.Param != test.param || failure.UpstreamCode != test.code || called {
				t.Fatalf("failure=%+v err=%v called=%v", failure, err, called)
			}
		})
	}
}
