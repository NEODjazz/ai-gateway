package gateway

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func audioHTTPBody(t *testing.T, fields [][2]string, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			t.Fatal(err)
		}
	}
	if filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func TestAudioTranscriptionUsesAuthenticatedPipelineAndReservesTPM(t *testing.T) {
	llm := &chatProvider{}
	rates := &embeddingTokenRateStore{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"audio-*"}}}), llm, rates))
	reference := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString([]byte("RIFF....WAVEdata"))
	body, contentType := audioHTTPBody(t, [][2]string{{"provider", "speech"}, {"model", "audio-model"}, {"prompt", "speaker@example.com"}, {"response_format", "json"}, {"languages[]", "en"}, {"languages[]", "de"}, {"keywords[]", "Acme"}, {"keywords[]", "Jane"}, {"chunking_strategy", `{"type":"server_vad","threshold":0.4}`}, {"known_speaker_names[]", "Jane"}, {"known_speaker_references[]", reference}}, "sample.wav", []byte("RIFF....WAVEdata"))
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer client-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || llm.request.AudioTranscriptionRequest == nil || llm.request.APIKey != "" {
		t.Fatalf("status=%d body=%s context=%+v", response.Code, response.Body.String(), llm.request)
	}
	if rates.tokens != openai.AudioTranscriptionReserveTokens(*llm.request.AudioTranscriptionRequest) {
		t.Fatalf("TPM reserve=%d", rates.tokens)
	}
	if got := llm.request.AudioTranscriptionRequest; len(got.Languages) != 2 || len(got.Keywords) != 2 || got.ChunkingStrategy == nil || got.ChunkingStrategy.Type != "server_vad" || len(got.KnownSpeakerNames) != 1 || len(got.KnownSpeakerReferences) != 1 {
		t.Fatalf("request=%+v", got)
	}
}

func TestAudioTranscriptionRejectsMalformedMultipartBeforeProvider(t *testing.T) {
	for _, test := range []struct {
		name     string
		fields   [][2]string
		filename string
		data     []byte
	}{
		{name: "invalid signature", fields: [][2]string{{"model", "audio"}}, filename: "sample.wav", data: []byte("not audio")},
		{name: "unknown field", fields: [][2]string{{"model", "audio"}, {"unknown", "value"}}, filename: "sample.wav", data: []byte("RIFF....WAVEdata")},
		{name: "duplicate scalar", fields: [][2]string{{"model", "audio"}, {"model", "duplicate"}}, filename: "sample.wav", data: []byte("RIFF....WAVEdata")},
		{name: "invalid chunking", fields: [][2]string{{"model", "audio"}, {"chunking_strategy", `{"type":"server_vad","threshold":2}`}}, filename: "sample.wav", data: []byte("RIFF....WAVEdata")},
		{name: "invalid language", fields: [][2]string{{"model", "audio"}, {"languages[]", "english"}}, filename: "sample.wav", data: []byte("RIFF....WAVEdata")},
		{name: "speaker mismatch", fields: [][2]string{{"model", "audio"}, {"known_speaker_names[]", "Jane"}}, filename: "sample.wav", data: []byte("RIFF....WAVEdata")},
		{name: "remote speaker reference", fields: [][2]string{{"model", "audio"}, {"known_speaker_names[]", "Jane"}, {"known_speaker_references[]", "https://example.test/sample.wav"}}, filename: "sample.wav", data: []byte("RIFF....WAVEdata")},
	} {
		t.Run(test.name, func(t *testing.T) {
			llm := &chatProvider{}
			body, contentType := audioHTTPBody(t, test.fields, test.filename, test.data)
			request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
			request.Header.Set("Content-Type", contentType)
			response := httptest.NewRecorder()
			Routes(NewHandler(modules.NewPipeline(nil), llm)).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || llm.request.AudioTranscriptionRequest != nil {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
