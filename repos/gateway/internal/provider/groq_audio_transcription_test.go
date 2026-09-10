package provider

import (
	"context"
	"encoding/base64"
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

func TestGroqAudioTranscriptionContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/openai/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer groq-key" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		if err := r.ParseMultipartForm(openai.MaxInferenceBodyBytes); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("model") != "whisper-large-v3-turbo" || r.FormValue("language") != "en" || r.FormValue("prompt") != "product names" || r.FormValue("temperature") != "0.25" || r.FormValue("response_format") != "verbose_json" || strings.Join(r.MultipartForm.Value["timestamp_granularities[]"], ",") != "word,segment" {
			t.Fatalf("form=%v", r.MultipartForm.Value)
		}
		files := r.MultipartForm.File["file"]
		if len(files) != 1 || files[0].Filename != "meeting.wav" || files[0].Header.Get("Content-Type") != "audio/wav" {
			t.Fatalf("files=%+v", files)
		}
		_, _ = io.WriteString(w, `{"task":"transcribe","language":"english","duration":99,"text":"hello","words":[{"word":"hello","start":0,"end":1.25}],"segments":[{"id":0,"seek":0,"start":0,"end":1.25,"text":"hello","tokens":[1,2],"temperature":0,"avg_logprob":-0.1,"compression_ratio":1.1,"no_speech_prob":0.01}],"x_groq":{"id":"request-1"}}`)
	}))
	defer server.Close()
	temperature := 0.25
	request := openai.AudioTranscriptionRequest{Model: "whisper-large-v3-turbo", File: mistralWAVAttachment(1250), Language: "en", Prompt: "product names", ResponseFormat: "verbose_json", Temperature: &temperature, TimestampGranularities: []string{"word", "segment"}}
	response, err := NewGroq(server.URL+"/openai/v1", "groq-key", false).TranscribeAudio(t.Context(), request)
	if err != nil || response.Text != "hello" || response.Duration != 1.25 || len(response.Words) != 1 || len(response.Segments) != 1 || response.Usage == nil || response.Usage.Type != "duration" || response.Usage.InputAudioMilliseconds != groqMinimumBilledAudioMilliseconds {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGroqAudioTranscriptionRejectsUnsupportedParametersAndFormats(t *testing.T) {
	client := NewGroq("https://example.test/openai/v1", "secret", false)
	requests := []openai.AudioTranscriptionRequest{
		{Model: "whisper", File: mistralWAVAttachment(1000), Keywords: []string{"Acme"}},
		{Model: "whisper", File: mistralWAVAttachment(1000), ResponseFormat: "diarized_json"},
		{Model: "whisper", File: mistralWAVAttachment(1000), Prompt: strings.Repeat("word ", 1000)},
		{Model: "whisper", File: openai.AudioAttachment{Filename: "audio.mp3", MediaType: "audio/mpeg", Data: base64.StdEncoding.EncodeToString([]byte("ID3payload"))}},
	}
	for _, request := range requests {
		if _, err := client.TranscribeAudio(t.Context(), request); err == nil {
			t.Fatalf("unsupported request accepted: %+v", request)
		}
	}
	if duration, err := client.ReserveAudioMilliseconds(openai.AudioTranscriptionRequest{File: mistralWAVAttachment(12500)}); err != nil || duration != 12500 {
		t.Fatalf("duration=%d err=%v", duration, err)
	}
	if duration, err := client.ReserveAudioMilliseconds(openai.AudioTranscriptionRequest{File: flacAttachment(12500)}); err != nil || duration != 12500 {
		t.Fatalf("FLAC duration=%d err=%v", duration, err)
	}
}

func TestRouterGroqTranscriptionUsesMinimumBillableDuration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"text":"hello"}`)
	}))
	defer server.Close()
	recorder := &transcriptionLifecycleRecorder{}
	router := New(Config{Modules: modules.NewPipeline([]modules.Module{recorder}), Endpoints: []config.ProviderEndpointConfig{{Name: "speech", Type: "groq", BaseURL: server.URL + "/openai/v1", Models: []string{"public-audio"}, ModelAliases: map[string]string{"public-audio": "whisper-large-v3-turbo"}, Capabilities: []string{"audio_transcription"}}}}).(*Router)
	request := openai.AudioTranscriptionRequest{Model: "public-audio", File: mistralWAVAttachment(1250)}
	response, err := router.TranscribeAudio(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, AudioTranscriptionRequest: &request})
	if err != nil || response.Text != "hello" || recorder.reserved != 10000 || recorder.settled != 10000 || response.Duration != 1.25 {
		t.Fatalf("response=%+v recorder=%+v err=%v", response, recorder, err)
	}
}

func TestDecodeGroqAudioTranscriptionValidation(t *testing.T) {
	if _, err := decodeGroqAudioTranscription(strings.NewReader(`{"text":"hello"}`), 0, 10000); err == nil {
		t.Fatal("zero actual duration accepted")
	}
	reader := &audioLimitReader{}
	if _, err := decodeGroqAudioTranscription(reader, 1000, 10000); err == nil || reader.read != maxAudioTranscriptionResponseBytes+1 {
		t.Fatalf("oversized response err=%v read=%d", err, reader.read)
	}
	var failure *Error
	_, err := NewGroq("https://example.test", "", false).TranscribeAudio(context.Background(), openai.AudioTranscriptionRequest{Model: "whisper", File: mistralWAVAttachment(1000), Include: []string{"logprobs"}})
	if !errors.As(err, &failure) || failure.Provider != "groq" || failure.Param != "include" {
		t.Fatalf("failure=%+v err=%v", failure, err)
	}
}
