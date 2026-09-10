package provider

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func mistralWAVAttachment(milliseconds int) openai.AudioAttachment {
	byteRate := 8000
	dataSize := byteRate * milliseconds / 1000
	payload := make([]byte, 44+dataSize)
	copy(payload[0:4], "RIFF")
	binary.LittleEndian.PutUint32(payload[4:8], uint32(len(payload)-8))
	copy(payload[8:12], "WAVE")
	copy(payload[12:16], "fmt ")
	binary.LittleEndian.PutUint32(payload[16:20], 16)
	binary.LittleEndian.PutUint16(payload[20:22], 1)
	binary.LittleEndian.PutUint16(payload[22:24], 1)
	binary.LittleEndian.PutUint32(payload[24:28], 8000)
	binary.LittleEndian.PutUint32(payload[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(payload[32:34], 1)
	binary.LittleEndian.PutUint16(payload[34:36], 8)
	copy(payload[36:40], "data")
	binary.LittleEndian.PutUint32(payload[40:44], uint32(dataSize))
	return openai.AudioAttachment{Filename: "meeting.wav", MediaType: "audio/wav", Data: base64.StdEncoding.EncodeToString(payload)}
}

func TestMistralAudioTranscriptionContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		if err := r.ParseMultipartForm(openai.MaxInferenceBodyBytes); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("model") != "voxtral-mini-latest" || r.FormValue("language") != "en" || r.FormValue("temperature") != "0.25" || r.FormValue("diarize") != "" || strings.Join(r.MultipartForm.Value["context_bias"], ",") != "Acme,Jane" || strings.Join(r.MultipartForm.Value["timestamp_granularities"], ",") != "segment" {
			t.Fatalf("form=%v", r.MultipartForm.Value)
		}
		files := r.MultipartForm.File["file"]
		if len(files) != 1 || files[0].Filename != "meeting.wav" || files[0].Header.Get("Content-Type") != "audio/wav" {
			t.Fatalf("files=%+v", files)
		}
		_, _ = io.WriteString(w, `{"model":"voxtral-mini-2507","text":"hello","language":"en","segments":[{"speaker_id":"speaker-1","text":"hello","start":0,"end":1.25}],"usage":{"prompt_audio_seconds":1.25,"prompt_tokens":4,"completion_tokens":6,"total_tokens":30}}`)
	}))
	defer server.Close()
	temperature := 0.25
	request := openai.AudioTranscriptionRequest{Model: "voxtral-mini-latest", File: mistralWAVAttachment(1250), Language: "en", ResponseFormat: "verbose_json", Temperature: &temperature, TimestampGranularities: []string{"segment"}, Keywords: []string{"Acme", "Jane"}}
	response, err := NewMistral(server.URL+"/v1", "secret", false).TranscribeAudio(t.Context(), request)
	if err != nil || response.Text != "hello" || response.Duration != 1.25 || response.Usage == nil || response.Usage.InputTokens != 24 || response.Usage.InputTokenDetails == nil || response.Usage.InputTokenDetails.AudioTokens != 20 || len(response.Segments) != 1 || response.Segments[0].Speaker != "speaker-1" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestMistralAudioTranscriptionRejectsUnsupportedParametersAndFormats(t *testing.T) {
	client := NewMistral("https://example.test/v1", "secret", false)
	for _, request := range []openai.AudioTranscriptionRequest{
		{Model: "voxtral", File: mistralWAVAttachment(1000), Prompt: "instruction"},
		{Model: "voxtral", File: openai.AudioAttachment{Filename: "audio.m4a", MediaType: "audio/mp4", Data: base64.StdEncoding.EncodeToString([]byte("....ftyp...."))}},
	} {
		if _, err := client.TranscribeAudio(t.Context(), request); err == nil {
			t.Fatalf("unsupported request accepted: %+v", request)
		}
	}
	if duration, err := client.ReserveAudioMilliseconds(openai.AudioTranscriptionRequest{File: openai.AudioAttachment{Filename: "audio.mp3", MediaType: "audio/mpeg", Data: base64.StdEncoding.EncodeToString([]byte("ID3payload"))}}); err != nil || duration != mistralMaxAudioMilliseconds {
		t.Fatalf("compressed reserve duration=%d err=%v", duration, err)
	}
}

type transcriptionLifecycleRecorder struct{ reserved, settled int }

func (*transcriptionLifecycleRecorder) Name() string   { return "transcription-recorder" }
func (*transcriptionLifecycleRecorder) Required() bool { return true }
func (m *transcriptionLifecycleRecorder) Handle(_ context.Context, req *modules.RequestContext) error {
	m.reserved = req.InputAudioMilliseconds
	return nil
}
func (*transcriptionLifecycleRecorder) PostResponseEnabled() bool { return true }
func (m *transcriptionLifecycleRecorder) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.settled = req.InputAudioMilliseconds
	return nil
}

func TestRouterMistralTranscriptionReservesAndSettlesDuration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"model":"voxtral-mini-2507","text":"hello","language":"en","segments":[],"usage":{"prompt_audio_seconds":1.2,"prompt_tokens":4,"completion_tokens":6,"total_tokens":30}}`)
	}))
	defer server.Close()
	recorder := &transcriptionLifecycleRecorder{}
	router := New(Config{Modules: modules.NewPipeline([]modules.Module{recorder}), Endpoints: []config.ProviderEndpointConfig{{Name: "speech", Type: "mistral", BaseURL: server.URL + "/v1", Models: []string{"public-audio"}, ModelAliases: map[string]string{"public-audio": "voxtral-mini-latest"}, Capabilities: []string{"audio_transcription"}}}}).(*Router)
	request := openai.AudioTranscriptionRequest{Model: "public-audio", File: mistralWAVAttachment(1250)}
	response, err := router.TranscribeAudio(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, AudioTranscriptionRequest: &request})
	if err != nil || response.Text != "hello" || recorder.reserved != 1250 || recorder.settled != 1200 {
		t.Fatalf("response=%+v recorder=%+v err=%v", response, recorder, err)
	}
}
