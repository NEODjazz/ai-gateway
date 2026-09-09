package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type speechRewriteModule struct{}

func (speechRewriteModule) Name() string   { return "speech-rewrite" }
func (speechRewriteModule) Required() bool { return true }
func (speechRewriteModule) Handle(_ context.Context, req *modules.RequestContext) error {
	req.Request.Messages[0].Content = "redacted"
	return nil
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
		`{"model":"tts","input":"hello","voice":"alloy","stream_format":"sse"}`,
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
