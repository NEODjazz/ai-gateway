package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type speechRewriteModule struct{}

func (speechRewriteModule) Name() string   { return "speech-rewrite" }
func (speechRewriteModule) Required() bool { return true }
func (speechRewriteModule) Handle(_ context.Context, req *modules.RequestContext) error {
	req.Request.Messages[0].Content = "redacted"
	return nil
}

type nativeSpeechStreamProvider struct{ chatProvider }

func (p *nativeSpeechStreamProvider) StreamGenerateSpeech(_ context.Context, req modules.RequestContext, write provider.AudioSpeechStreamWriter) (openai.AudioSpeechResponse, bool, error) {
	p.request = req
	for _, payload := range []string{
		`{"type":"speech.audio.delta","audio":"SUQzYXVkaW8="}`,
		`{"type":"speech.audio.done","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`,
	} {
		if err := write(payload); err != nil {
			return openai.AudioSpeechResponse{}, true, err
		}
	}
	usage := openai.AudioSpeechUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}
	return openai.AudioSpeechResponse{ContentType: "audio/mpeg", Model: "tts", Usage: &usage}, true, nil
}

func TestAudioSpeechStreamsSSEThroughAuthenticatedLifecycle(t *testing.T) {
	llm := &nativeSpeechStreamProvider{}
	rates := &embeddingTokenRateStore{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"tts"}}}), llm, rates))
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(`{"model":"tts","input":"hello","voice":"alloy","stream_format":"sse"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(response.Body.String(), `"type":"speech.audio.delta"`) || !strings.Contains(response.Body.String(), `"type":"speech.audio.done"`) || strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatalf("status=%d content_type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if llm.request.AudioSpeechRequest == nil || llm.request.AudioSpeechRequest.StreamFormat != "sse" || rates.tokens != openai.AudioSpeechReserveTokens(*llm.request.AudioSpeechRequest) {
		t.Fatalf("request=%+v TPM=%d", llm.request.AudioSpeechRequest, rates.tokens)
	}
}

func TestAudioSpeechSSEFailsClearlyWithoutStreamingProvider(t *testing.T) {
	llm := &chatProvider{}
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(`{"model":"tts","input":"hello","voice":"alloy","stream_format":"sse"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	Routes(NewHandler(modules.NewPipeline(nil), llm)).ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"code":"streaming_unsupported"`) || llm.request.AudioSpeechRequest != nil {
		t.Fatalf("status=%d body=%s request=%+v", response.Code, response.Body.String(), llm.request)
	}
}

func TestAudioSpeechUsesAuthenticatedPipelineAndCharacterAccounting(t *testing.T) {
	llm := &chatProvider{}
	rates := &embeddingTokenRateStore{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"tts-*"}}}), llm, rates))
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(`{"provider":"speech","model":"tts-model","input":"Привет 👋","voice":"alloy","response_format":"mp3"}`))
	request.Header.Set("Authorization", "Bearer client-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "audio/mpeg" || response.Body.String() != "ID3audio" || llm.request.AudioSpeechRequest == nil || llm.request.APIKey != "" {
		t.Fatalf("status=%d headers=%v body=%q context=%+v", response.Code, response.Header(), response.Body.String(), llm.request)
	}
	if llm.request.InputCharacters != 8 || rates.tokens != openai.AudioSpeechReserveTokens(*llm.request.AudioSpeechRequest) {
		t.Fatalf("characters=%d TPM=%d", llm.request.InputCharacters, rates.tokens)
	}
}

func TestAudioSpeechUsesTransformedTextForProviderAndAccounting(t *testing.T) {
	llm := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{speechRewriteModule{}, accessPolicyModule{models: []string{"tts"}}}), llm))
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(`{"model":"tts","input":"private@example.com","voice":"alloy"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || llm.request.AudioSpeechRequest == nil || llm.request.AudioSpeechRequest.Input != "redacted" || llm.request.InputCharacters != len("redacted") {
		t.Fatalf("status=%d request=%+v", response.Code, llm.request)
	}
}

func TestAudioSpeechRejectsInvalidJSONBeforeProvider(t *testing.T) {
	for _, body := range []string{
		`{"model":"tts","input":"hello","voice":"alloy","unknown":true}`,
		`{"model":"tts","input":"hello","voice":"alloy","speed":5}`,
	} {
		llm := &chatProvider{}
		response := httptest.NewRecorder()
		Routes(NewHandler(modules.NewPipeline(nil), llm)).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest || llm.request.AudioSpeechRequest != nil {
			t.Fatalf("status=%d body=%s provider_request=%+v", response.Code, response.Body.String(), llm.request.AudioSpeechRequest)
		}
	}
}
