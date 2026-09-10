package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestMistralAudioSpeechMapsNativeContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/speech" || r.Header.Get("Authorization") != "Bearer mistral-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "voxtral-tts" || payload["input"] != "hello" || payload["voice_id"] != "voice-id" || payload["response_format"] != "mp3" || payload["stream"] != false || payload["voice"] != nil {
			t.Fatalf("payload=%#v", payload)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"audio_data": base64.StdEncoding.EncodeToString([]byte("OggSaudio"))})
	}))
	defer server.Close()

	client := NewMistral(server.URL+"/v1", "mistral-key", false)
	response, err := client.GenerateSpeech(t.Context(), openai.AudioSpeechRequest{Provider: "deployment", Model: "voxtral-tts", Input: "hello", Voice: "voice-id"})
	if err != nil || response.Model != "voxtral-tts" || response.ContentType != "audio/mpeg" || !bytes.Equal(response.Data, []byte("OggSaudio")) {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestMistralAudioSpeechRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewMistral(server.URL, "key", false)
	speed := 1.0
	tests := []struct {
		name, param string
		request     openai.AudioSpeechRequest
	}{
		{name: "instructions", param: "instructions", request: openai.AudioSpeechRequest{Instructions: "speak softly"}},
		{name: "speed", param: "speed", request: openai.AudioSpeechRequest{Speed: &speed}},
		{name: "stream format", param: "stream_format", request: openai.AudioSpeechRequest{StreamFormat: "audio"}},
		{name: "response format", param: "response_format", request: openai.AudioSpeechRequest{ResponseFormat: "aac"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.request.Model = "voxtral-tts"
			test.request.Input = "hello"
			test.request.Voice = "voice-id"
			_, err := client.GenerateSpeech(t.Context(), test.request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Provider != "mistral" || failure.Param != test.param || failure.UpstreamCode != "unsupported_parameter" || called {
				t.Fatalf("failure=%+v err=%v called=%v", failure, err, called)
			}
		})
	}
}

func TestMistralAudioSpeechValidatesBoundedResponse(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString([]byte("audio"))
	for name, body := range map[string]string{
		"null":            `null`,
		"missing data":    `{}`,
		"invalid base64":  `{"audio_data":"!"}`,
		"trailing object": `{"audio_data":"` + valid + `"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeMistralAudioSpeech(strings.NewReader(body)); err == nil {
				t.Fatalf("accepted %s", body)
			}
		})
	}
	reader := &mistralSpeechLimitReader{}
	if _, err := decodeMistralAudioSpeech(reader); err == nil {
		t.Fatal("oversized response was accepted")
	}
}

type mistralSpeechLimitReader struct{ read int }

func (r *mistralSpeechLimitReader) Read(buffer []byte) (int, error) {
	remaining := maxMistralAudioSpeechResponseBytes + 1 - r.read
	if remaining <= 0 {
		return 0, nil
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
