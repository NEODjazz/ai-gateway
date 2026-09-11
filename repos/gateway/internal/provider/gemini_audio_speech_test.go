package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestGeminiAudioSpeechContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiSpeechRequest
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/interactions" || r.Header.Get("x-goog-api-key") != "secret" || json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatalf("request=%s body=%+v", r.URL.String(), body)
		}
		if body.Model != "gemini-tts" || body.Input != "Speak warmly\n\nRead exactly: hello" || body.ResponseFormat.Type != "audio" || body.ResponseFormat.MIMEType != "audio/mp3" || body.ResponseFormat.Delivery != "inline" || len(body.GenerationConfig.SpeechConfig) != 1 || body.GenerationConfig.SpeechConfig[0].Voice != "Kore" {
			t.Fatalf("body=%+v", body)
		}
		_, _ = fmt.Fprintf(w, `{"status":"completed","steps":[{"type":"model_output","content":[{"type":"audio","data":%q,"mime_type":"audio/mp3"}]}]}`, base64.StdEncoding.EncodeToString([]byte("ID3audio")))
	}))
	defer server.Close()
	response, err := NewGemini(server.URL, "secret", false).GenerateSpeech(t.Context(), openai.AudioSpeechRequest{Model: "models/gemini-tts", Input: "hello", Voice: "Kore", Instructions: "Speak warmly"})
	if err != nil || response.ContentType != "audio/mpeg" || response.Model != "models/gemini-tts" || !bytes.Equal(response.Data, []byte("ID3audio")) {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGeminiAudioSpeechRejectsUnsupportedParametersBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	speed := 1.5
	for _, test := range []struct {
		name, param string
		request     openai.AudioSpeechRequest
	}{
		{"speed", "speed", openai.AudioSpeechRequest{Speed: &speed}},
		{"aac", "response_format", openai.AudioSpeechRequest{ResponseFormat: "aac"}},
		{"flac", "response_format", openai.AudioSpeechRequest{ResponseFormat: "flac"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.request.Model, test.request.Input, test.request.Voice = "model", "hello", "Kore"
			_, err := NewGemini(server.URL, "secret", false).GenerateSpeech(t.Context(), test.request)
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

func TestRouterRoutesNativeGeminiAudioSpeech(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiSpeechRequest
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "upstream" || body.ResponseFormat.MIMEType != "audio/wav" {
			t.Fatalf("body=%+v", body)
		}
		_, _ = fmt.Fprintf(w, `{"status":"completed","steps":[{"type":"model_output","content":[{"type":"audio","data":%q,"mime_type":"audio/wav"}]}]}`, base64.StdEncoding.EncodeToString([]byte("RIFFaudio")))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "gemini-speech", Type: "gemini", BaseURL: server.URL, Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"audio_speech"}}}}).(*Router)
	request := openai.AudioSpeechRequest{Model: "public", Input: "hello", Voice: "Kore", ResponseFormat: "wav"}
	response, err := router.GenerateSpeech(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, AudioSpeechRequest: &request})
	if err != nil || response.ContentType != "audio/wav" || !bytes.Equal(response.Data, []byte("RIFFaudio")) {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGeminiAudioSpeechRejectsMalformedOutput(t *testing.T) {
	request := openai.AudioSpeechRequest{Model: "model", Input: "hello", Voice: "Kore"}
	for _, payload := range []string{
		`{"status":"failed"}`,
		`{"status":"completed","steps":[]}`,
		`{"status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"not audio"}]}]}`,
		`{"status":"completed","steps":[{"type":"model_output","content":[{"type":"audio","data":"%%%","mime_type":"audio/mp3"}]}]}`,
		`{"status":"completed","steps":[{"type":"model_output","content":[{"type":"audio","data":"YQ==","mime_type":"audio/wav"}]}]}`,
	} {
		if _, err := decodeGeminiAudioSpeechResponse(bytes.NewBufferString(payload), request); err == nil {
			t.Fatalf("accepted payload=%s", payload)
		}
	}
}
