package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
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
	body, contentType := audioHTTPBody(t, [][2]string{{"provider", "speech"}, {"model", "audio-model"}, {"prompt", "speaker@example.com"}, {"response_format", "json"}, {"mode", "SMART"}, {"languages[]", "en"}, {"languages[]", "de"}, {"keywords[]", "Acme"}, {"keywords[]", "Jane"}, {"chunking_strategy", `{"type":"server_vad","threshold":0.4}`}, {"known_speaker_names[]", "Jane"}, {"known_speaker_references[]", reference}}, "sample.wav", []byte("RIFF....WAVEdata"))
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
	if got := llm.request.AudioTranscriptionRequest; got.Mode != "SMART" || len(got.Languages) != 2 || len(got.Keywords) != 2 || got.ChunkingStrategy == nil || got.ChunkingStrategy.Type != "server_vad" || len(got.KnownSpeakerNames) != 1 || len(got.KnownSpeakerReferences) != 1 {
		t.Fatalf("request=%+v", got)
	}
}

func TestAudioTranslationUsesAuthenticatedPipeline(t *testing.T) {
	llm := &chatProvider{}
	rates := &embeddingTokenRateStore{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"audio-*"}}}), llm, rates))
	body, contentType := audioHTTPBody(t, [][2]string{{"provider", "speech"}, {"model", "audio-model"}, {"language", "en"}, {"prompt", "product names"}, {"response_format", "json"}}, "sample.wav", []byte("RIFF....WAVEdata"))
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/translations", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer client-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || llm.request.AudioTranscriptionRequest == nil || llm.request.APIKey != "" || llm.request.Metadata["gateway.api_type"] != "audio_translation" {
		t.Fatalf("status=%d body=%s context=%+v", response.Code, response.Body.String(), llm.request)
	}
	if rates.tokens != openai.AudioTranscriptionReserveTokens(*llm.request.AudioTranscriptionRequest) {
		t.Fatalf("TPM reserve=%d", rates.tokens)
	}
}

func TestAudioMultipartAcceptsSignatureValidatedFormats(t *testing.T) {
	formats := []struct {
		filename, mediaType string
		data                []byte
	}{
		{"sample.aif", "audio/aiff", []byte("FORM\x00\x00\x00\x00AIFFpayload")},
		{"sample.aiff", "audio/aiff", []byte("FORM\x00\x00\x00\x00AIFCpayload")},
		{"sample.aac", "audio/aac", []byte("\xff\xf1\x50\x80\x00\x1f\xfc")},
		{"sample.opus", "audio/opus", []byte("OggSpayload")},
		{"sample.m4a", "audio/m4a", []byte("\x00\x00\x00\x18ftypisom")},
	}
	for _, format := range formats {
		t.Run(format.filename, func(t *testing.T) {
			mediaType, err := audioMediaType(format.filename, "application/octet-stream", format.data)
			if err != nil || mediaType != format.mediaType {
				t.Fatalf("media type=%q err=%v", mediaType, err)
			}
		})
	}
	if _, err := audioMediaType("sample.aac", "audio/wav", []byte("\xff\xf1\x50\x80\x00\x1f\xfc")); err == nil {
		t.Fatal("explicit mismatched MIME type was accepted")
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
		{name: "invalid stream", fields: [][2]string{{"model", "audio"}, {"stream", "yes"}}, filename: "sample.wav", data: []byte("RIFF....WAVEdata")},
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

type nativeAudioStreamProvider struct{ chatProvider }

func (p *nativeAudioStreamProvider) StreamTranscribeAudio(_ context.Context, req modules.RequestContext, write provider.AudioTranscriptionStreamWriter) (openai.AudioTranscriptionResponse, bool, error) {
	p.request = req
	for _, payload := range []string{
		`{"type":"transcript.text.delta","delta":"hello"}`,
		`{"type":"transcript.text.done","text":"hello","usage":{"type":"tokens","input_tokens":3,"output_tokens":1,"total_tokens":4}}`,
	} {
		if err := write(payload); err != nil {
			return openai.AudioTranscriptionResponse{}, true, err
		}
	}
	return openai.AudioTranscriptionResponse{Text: "hello", Usage: &openai.AudioTranscriptionUsage{Type: "tokens", InputTokens: 3, OutputTokens: 1, TotalTokens: 4}}, true, nil
}

func TestAudioTranscriptionStreamsNativeEvents(t *testing.T) {
	llm := &nativeAudioStreamProvider{}
	rates := &embeddingTokenRateStore{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"audio-*"}}}), llm, rates))
	body, contentType := audioHTTPBody(t, [][2]string{{"model", "audio-model"}, {"stream", "true"}}, "sample.wav", []byte("RIFF....WAVEdata"))
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(response.Body.String(), `"type":"transcript.text.delta"`) || !strings.Contains(response.Body.String(), `"type":"transcript.text.done"`) || strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatalf("status=%d content_type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if llm.request.AudioTranscriptionRequest == nil || !llm.request.AudioTranscriptionRequest.Stream || rates.tokens != openai.AudioTranscriptionReserveTokens(*llm.request.AudioTranscriptionRequest) {
		t.Fatalf("request=%+v TPM=%d", llm.request.AudioTranscriptionRequest, rates.tokens)
	}
}

func TestAudioTranscriptionSynthesizesFinalStreamAndTranslationRejectsStream(t *testing.T) {
	llm := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), llm))
	body, contentType := audioHTTPBody(t, [][2]string{{"model", "audio-model"}, {"stream", "true"}}, "sample.wav", []byte("RIFF....WAVEdata"))
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(response.Body.String(), `"type":"transcript.text.done"`) || strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatalf("status=%d content_type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if llm.request.AudioTranscriptionRequest == nil || llm.request.AudioTranscriptionRequest.Stream {
		t.Fatalf("sync fallback request=%+v", llm.request.AudioTranscriptionRequest)
	}

	llm.request = modules.RequestContext{}
	body, contentType = audioHTTPBody(t, [][2]string{{"model", "audio-model"}, {"stream", "true"}}, "sample.wav", []byte("RIFF....WAVEdata"))
	request = httptest.NewRequest(http.MethodPost, "/v1/audio/translations", body)
	request.Header.Set("Content-Type", contentType)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || llm.request.AudioTranscriptionRequest != nil {
		t.Fatalf("status=%d body=%s request=%+v", response.Code, response.Body.String(), llm.request)
	}
}
