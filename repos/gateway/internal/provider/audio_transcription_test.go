package provider

import (
	"bytes"
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

func transcriptionAttachment() openai.AudioAttachment {
	return openai.AudioAttachment{Filename: "meeting.wav", MediaType: "audio/wav", Data: base64.StdEncoding.EncodeToString([]byte("RIFF....WAVEdata"))}
}

const validTranscriptionResponse = `{"text":"hello","duration":1.5,"words":[{"word":"hello","start":0,"end":1.5}],"usage":{"type":"tokens","input_tokens":3,"output_tokens":1,"total_tokens":4}}`

type audioLimitReader struct{ read int }

func (r *audioLimitReader) Read(buffer []byte) (int, error) {
	remaining := maxAudioTranscriptionResponseBytes + 1 - r.read
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

func TestOpenAICompatibleAudioTranscriptionContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		if err := r.ParseMultipartForm(openai.MaxInferenceBodyBytes); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("provider") != "" || r.FormValue("model") != "upstream-audio" || r.FormValue("prompt") != "names" || r.FormValue("temperature") != "0.25" || len(r.MultipartForm.Value["timestamp_granularities[]"]) != 2 || strings.Join(r.MultipartForm.Value["languages[]"], ",") != "en,fr" || strings.Join(r.MultipartForm.Value["keywords[]"], ",") != "Acme,Jane" || r.FormValue("chunking_strategy") != `{"type":"server_vad","threshold":0.4}` || r.FormValue("known_speaker_names[]") != "Jane" || r.FormValue("known_speaker_references[]") != transcriptionAttachment().DataURL() {
			t.Fatalf("form=%v", r.MultipartForm.Value)
		}
		files := r.MultipartForm.File["file"]
		if len(files) != 1 || files[0].Filename != "meeting.wav" || files[0].Header.Get("Content-Type") != "audio/wav" {
			t.Fatalf("files=%+v", files)
		}
		file, _ := files[0].Open()
		defer file.Close()
		data, _ := io.ReadAll(file)
		if !bytes.Equal(data, []byte("RIFF....WAVEdata")) {
			t.Fatalf("audio=%q", data)
		}
		_, _ = w.Write([]byte(validTranscriptionResponse))
	}))
	defer server.Close()
	temperature := 0.25
	threshold := 0.4
	request := openai.AudioTranscriptionRequest{Provider: "deployment", Model: "upstream-audio", File: transcriptionAttachment(), Prompt: "names", ResponseFormat: "verbose_json", Temperature: &temperature, TimestampGranularities: []string{"word", "segment"}, Languages: []string{"en", "fr"}, Keywords: []string{"Acme", "Jane"}, ChunkingStrategy: &openai.AudioChunkingStrategy{Type: "server_vad", Threshold: &threshold}, KnownSpeakerNames: []string{"Jane"}, KnownSpeakerReferences: []openai.AudioAttachment{transcriptionAttachment()}}
	response, err := NewOpenAICompatible(server.URL+"/v1", "secret", false).TranscribeAudio(t.Context(), request)
	if err != nil || response.Text != "hello" || response.Usage == nil || response.Usage.TotalTokens != 4 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestAzureAudioTranscriptionContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/audio/transcriptions" || r.URL.Query().Get("api-version") != "2025-04-01-preview" || r.Header.Get("api-key") != "secret" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected Azure request: %s headers=%v", r.URL.String(), r.Header)
		}
		_, _ = w.Write([]byte(validTranscriptionResponse))
	}))
	defer server.Close()
	response, err := NewAzureOpenAI(server.URL, "secret", false, "2025-04-01-preview", "api_key").TranscribeAudio(t.Context(), openai.AudioTranscriptionRequest{Model: "audio", File: transcriptionAttachment()})
	if err != nil || response.Text != "hello" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestRouterAudioTranscriptionRequiresCapabilityAndAppliesAlias(t *testing.T) {
	var model string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(openai.MaxInferenceBodyBytes); err != nil {
			t.Fatal(err)
		}
		model = r.FormValue("model")
		_, _ = w.Write([]byte(validTranscriptionResponse))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "speech", Type: "openai-compatible", BaseURL: server.URL, Models: []string{"public-audio"}, ModelAliases: map[string]string{"public-audio": "upstream-audio"}, Capabilities: []string{"audio_transcription"}}}}).(*Router)
	request := openai.AudioTranscriptionRequest{Model: "public-audio", File: transcriptionAttachment()}
	response, err := router.TranscribeAudio(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, AudioTranscriptionRequest: &request})
	if err != nil || model != "upstream-audio" || response.Text != "hello" {
		t.Fatalf("response=%+v model=%q err=%v", response, model, err)
	}

	missing := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "speech", Type: "openai-compatible", BaseURL: server.URL, Models: []string{"public-audio"}, Capabilities: []string{"chat"}}}}).(*Router)
	if _, err := missing.TranscribeAudio(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, AudioTranscriptionRequest: &request}); err == nil {
		t.Fatal("endpoint without explicit capability was selected")
	}
}

func TestAudioTranscriptionResponseValidation(t *testing.T) {
	if _, err := decodeAudioTranscriptionResponse(strings.NewReader(validTranscriptionResponse)); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"text":"hello"}`,
		`{"text":"hello","usage":{"type":"duration","input_tokens":0,"output_tokens":0,"total_tokens":0}}`,
		`{"text":"hello","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":3}}`,
		validTranscriptionResponse + `{}`,
	} {
		if _, err := decodeAudioTranscriptionResponse(strings.NewReader(body)); err == nil {
			t.Fatalf("accepted invalid response %s", body)
		}
	}
	reader := &audioLimitReader{}
	if _, err := decodeAudioTranscriptionResponse(reader); err == nil || reader.read != maxAudioTranscriptionResponseBytes+1 {
		t.Fatalf("read=%d err=%v", reader.read, err)
	}
}

func TestMistralUnsupportedAudioTranscriptionParameterRejectedBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	client := NewMistral(server.URL, "secret", false)
	_, err := client.TranscribeAudio(context.Background(), openai.AudioTranscriptionRequest{Model: "audio", File: mistralWAVAttachment(1000), Prompt: "unsupported"})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.UpstreamCode != "unsupported_parameter" || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
