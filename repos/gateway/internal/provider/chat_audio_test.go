package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestCompatibleChatAudioJSONAndStream(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var received map[string]json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
					return
				}
				if !streaming {
					_, _ = fmt.Fprint(w, `{"id":"chat-audio","model":"audio-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello","audio":{"id":"audio-1","data":"aGVsbG8=","expires_at":123,"transcript":"hello"}},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5,"completion_tokens_details":{"audio_tokens":2}}}`)
					return
				}
				_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-audio\",\"model\":\"audio-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"audio\":{\"id\":\"audio-1\",\"data\":\"aGVs\",\"transcript\":\"hel\"}}}]}\n\n")
				_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-audio\",\"model\":\"audio-model\",\"choices\":[{\"index\":0,\"delta\":{\"audio\":{\"data\":\"bG8=\",\"transcript\":\"lo\"}}}]}\n\n")
				_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-audio\",\"model\":\"audio-model\",\"choices\":[{\"index\":0,\"delta\":{\"audio\":{\"expires_at\":123}},\"finish_reason\":\"stop\"}]}\n\n")
				_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5,\"completion_tokens_details\":{\"audio_tokens\":2}}}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()

			request := openai.ChatCompletionRequest{
				ChatGenerationOptions: openai.ChatGenerationOptions{Modalities: []string{"text", "audio"}, Audio: &openai.ChatAudioOptions{Format: "mp3", Voice: openai.ChatAudioVoice{ID: "voice-1"}}},
				Model:                 "audio-model", Stream: streaming, Messages: []openai.Message{{Role: "assistant", Audio: &openai.ChatAudio{ID: "audio-old"}}},
			}
			client := NewOpenAICompatible(server.URL, "", true)
			var response openai.ChatCompletionResponse
			var err error
			var payloads []string
			if streaming {
				response, err = client.StreamChatCompletions(context.Background(), request, func(payload string) error { payloads = append(payloads, payload); return nil })
			} else {
				response, err = client.ChatCompletions(context.Background(), request)
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(received["modalities"]) != `["text","audio"]` || string(received["audio"]) != `{"format":"mp3","voice":{"id":"voice-1"}}` || !strings.Contains(string(received["messages"]), `"audio":{"id":"audio-old"}`) {
				t.Fatalf("audio request lost: %+v", received)
			}
			audio := response.Choices[0].Message.Audio
			if audio == nil || audio.Data == nil || *audio.Data != "aGVsbG8=" || audio.Transcript == nil || *audio.Transcript != "hello" || audio.ExpiresAt == nil || *audio.ExpiresAt != 123 || response.Usage.CompletionTokensDetails == nil || response.Usage.CompletionTokensDetails.AudioTokens != 2 {
				t.Fatalf("audio response lost: %+v", response)
			}
			if streaming && len(payloads) != 4 {
				t.Fatalf("stream payload count=%d", len(payloads))
			}
		})
	}
}

func TestCompatibleChatRejectsInvalidAudioBeforeReturn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","audio":{"id":"audio-1","data":"invalid!","expires_at":123,"transcript":"hello"}}}]}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Modalities: []string{"audio"}, Audio: &openai.ChatAudioOptions{Format: "mp3", Voice: openai.ChatAudioVoice{Name: "alloy"}}}}
	if _, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(t.Context(), request); err == nil || !strings.Contains(err.Error(), "audio") {
		t.Fatalf("invalid audio accepted: %v", err)
	}
}

func TestCompatibleChatRequiresRequestedAudio(t *testing.T) {
	request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Modalities: []string{"audio"}, Audio: &openai.ChatAudioOptions{Format: "mp3", Voice: openai.ChatAudioVoice{Name: "alloy"}}}}
	for name, payload := range map[string]string{
		"missing":      `{"choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`,
		"zero choices": `{"choices":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, payload) }))
			defer server.Close()
			if _, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(t.Context(), request); err == nil || !strings.Contains(err.Error(), "omitted requested chat audio") {
				t.Fatalf("missing audio accepted: %v", err)
			}
		})
	}

	data, transcript, expiry := "aA==", "h", int64(1)
	response := openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{Audio: &openai.ChatAudio{ID: "audio", Data: &data, Transcript: &transcript, ExpiresAt: &expiry}}}}}
	if err := validateRequestedChatAudio(openai.ChatCompletionRequest{}, response); err == nil || !strings.Contains(err.Error(), "unrequested") {
		t.Fatalf("unsolicited audio accepted: %v", err)
	}
}

func TestChatAudioStreamRejectsInvalidFragmentBeforeDelivery(t *testing.T) {
	wrote := false
	payload := "data: {\"choices\":[{\"index\":0,\"delta\":{\"audio\":{\"data\":\"invalid!\"}}}]}\n\n"
	if _, err := streamChatCompletionData(strings.NewReader(payload), "audio-model", func(string) error { wrote = true; return nil }); err == nil || wrote {
		t.Fatalf("invalid audio fragment delivered: err=%v wrote=%v", err, wrote)
	}
}

func TestChatAudioRoutingAndCachePolicy(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Modalities: []string{"audio"}, Audio: &openai.ChatAudioOptions{Format: "mp3", Voice: openai.ChatAudioVoice{Name: "alloy"}}}, Model: "audio-model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	if got := strings.Join(requiredChatCapabilities(request.Request, true), ","); got != "chat,stream,audio" {
		t.Fatalf("audio routing requirements=%s", got)
	}
	if !validDeploymentCapabilities([]string{"chat", "audio"}) || !requiresExplicitEndpointCapability([]string{"chat", "audio"}) || hasExplicitEndpointCapabilities([]string{"chat"}, []string{"chat", "audio"}) {
		t.Fatal("audio capability is not an explicit deployment contract")
	}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache accepted audio output")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "audio"}); ok {
		t.Fatal("semantic cache accepted audio output")
	}
	history := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "assistant", Content: "hello", Audio: &openai.ChatAudio{ID: "audio-old"}}}}}
	if got := strings.Join(requiredChatCapabilities(history.Request, false), ","); got != "chat,audio" {
		t.Fatalf("audio history routing requirements=%s", got)
	}
	if _, _, ok := semanticRequest(history, Endpoint{Name: "audio"}); ok {
		t.Fatal("semantic cache accepted audio history")
	}
}

func TestChatAudioHistoryIsRejectedByNativeAdapters(t *testing.T) {
	request := openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "assistant", Audio: &openai.ChatAudio{ID: "audio-old"}}}}
	for _, client := range []Client{NewAnthropic("http://unused.invalid", "", false), NewOllama("http://unused.invalid", false), NewGemini("http://unused.invalid", "", false), Demo{}} {
		var failure *Error
		err := validateChatAdapter(client, request)
		if !errors.As(err, &failure) || failure.Param != "messages.audio" {
			t.Fatalf("%T silently accepted audio history: %v", client, err)
		}
	}
}

func TestCompatibleChatRejectsInvalidAudioHistory(t *testing.T) {
	data := "aA=="
	for _, message := range []openai.Message{
		{Role: "user", Audio: &openai.ChatAudio{ID: "audio-old"}},
		{Role: "assistant", Audio: &openai.ChatAudio{ID: "audio-old", Data: &data}},
	} {
		var failure *Error
		err := NewOpenAICompatible("http://unused.invalid", "", false).ValidateChatParameters(openai.ChatCompletionRequest{Messages: []openai.Message{message}})
		if !errors.As(err, &failure) || failure.Param != "messages.audio" || failure.UpstreamCode != "invalid_request" {
			t.Fatalf("invalid compatible audio history accepted: %v", err)
		}
	}
}

func TestChatAudioRejectsAnonymizedPrompt(t *testing.T) {
	request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Modalities: []string{"audio"}, Audio: &openai.ChatAudioOptions{Format: "mp3", Voice: openai.ChatAudioVoice{Name: "alloy"}}}}
	if err := audioAnonymizationError(request, modules.RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var failure *Error
	err := audioAnonymizationError(request, modules.RequestContext{Metadata: map[string]string{"provider.endpoint.name": "audio"}, AnonymizationValues: map[string]string{"{{EMAIL_1}}": "user@example.com"}})
	if !errors.As(err, &failure) || failure.Class != FailureContentPolicy || failure.Param != "audio" || failure.UpstreamCode != "audio_anonymization_unsupported" {
		t.Fatalf("anonymized audio was not rejected: %v", err)
	}
}

func TestChatAudioHistoryBindsExactDeployment(t *testing.T) {
	request := openai.ChatCompletionRequest{Model: "audio-model", Messages: []openai.Message{{Role: "assistant", Audio: &openai.ChatAudio{ID: "audio-old"}}}}
	candidates := []Endpoint{{Name: "deployment-a", RoutingModel: "audio-model"}, {Name: "deployment-b", RoutingModel: "audio-model"}, {Name: "deployment-a", RoutingModel: "fallback-model", FallbackStage: 1}}
	if _, err := bindChatAudioHistory(request, candidates); err == nil {
		t.Fatal("unbound audio history accepted")
	}
	request.Provider = "deployment-a"
	bound, err := bindChatAudioHistory(request, candidates)
	if err != nil || len(bound) != 1 || bound[0].Name != "deployment-a" || bound[0].FallbackStage != 0 {
		t.Fatalf("audio history binding failed: candidates=%+v err=%v", bound, err)
	}
	request.Provider = "openai-compatible"
	if _, err := bindChatAudioHistory(request, candidates); err == nil {
		t.Fatal("provider type accepted as an audio history owner")
	}
}
