package provider

import (
	"errors"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestLemonadeUnsupportedParameters(t *testing.T) {
	client := NewLemonade("http://unused.invalid", "", true)
	store, limit, penalty := true, 64, 0.5
	for _, test := range []struct {
		name string
		err  func() error
	}{
		{"chat logprobs", func() error {
			return client.ValidateChatParameters(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &store}})
		}},
		{"chat service tier", func() error {
			return client.ValidateChatParameters(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "priority"}})
		}},
		{"chat store", func() error {
			return client.ValidateChatParameters(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Store: &store}})
		}},
		{"chat token aliases", func() error {
			return client.ValidateChatParameters(openai.ChatCompletionRequest{MaxTokens: &limit, MaxCompletionTokens: &limit})
		}},
		{"chat penalty range", func() error {
			return client.ValidateChatParameters(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{RepetitionPenalty: &penalty}})
		}},
		{"chat stream obfuscation", func() error {
			return client.ValidateChatParameters(openai.ChatCompletionRequest{StreamOptions: &openai.ChatStreamOptions{IncludeObfuscation: &store}})
		}},
		{"chat custom tools", func() error {
			return client.ValidateChatParameters(openai.ChatCompletionRequest{Tools: []openai.Tool{{Type: "custom"}}})
		}},
		{"chat legacy tools", func() error {
			return client.ValidateChatParameters(openai.ChatCompletionRequest{Functions: []openai.FunctionDefinition{{Name: "run"}}})
		}},
		{"chat prompt cache", func() error {
			return client.ValidateChatParameters(openai.ChatCompletionRequest{Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "run", PromptCacheBreakpoint: &openai.PromptCacheBreakpoint{Mode: "ephemeral"}}}}})
		}},
		{"response store", func() error { return client.ValidateResponseParameters(openai.ResponseRequest{Store: &store}) }},
		{"response continuity", func() error {
			return client.ValidateResponseParameters(openai.ResponseRequest{PreviousResponse: "resp-old"})
		}},
		{"response conversation", func() error {
			return client.ValidateResponseParameters(openai.ResponseRequest{Conversation: &openai.ResponseConversation{ID: "conversation"}})
		}},
		{"response background", func() error { return client.ValidateResponseParameters(openai.ResponseRequest{Background: true}) }},
		{"response managed tools", func() error {
			return client.ValidateResponseParameters(openai.ResponseRequest{Tools: []openai.ResponseTool{{Type: "web_search"}}})
		}},
		{"completion logprobs", func() error { return client.ValidateCompletionParameters(openai.CompletionRequest{Logprobs: &limit}) }},
		{"embedding dimensions", func() error {
			return client.ValidateEmbeddingParameters(openai.EmbeddingRequest{Input: "hello", Dimensions: &limit})
		}},
		{"embedding token IDs", func() error { return client.ValidateEmbeddingParameters(openai.EmbeddingRequest{Input: []int{1, 2}}) }},
		{"rerank document objects", func() error {
			return client.ValidateRerankParameters(openai.RerankRequest{Documents: []any{map[string]any{"text": "hello"}}})
		}},
		{"rerank options", func() error { return client.ValidateRerankParameters(openai.RerankRequest{TopN: &limit}) }},
		{"image URL output", func() error {
			return client.ValidateImageGenerationParameters(openai.ImageGenerationRequest{ResponseFormat: "url"})
		}},
		{"image generation count", func() error {
			return client.ValidateImageGenerationParameters(openai.ImageGenerationRequest{N: &limit})
		}},
		{"image stream", func() error {
			return client.ValidateImageGenerationParameters(openai.ImageGenerationRequest{Stream: true})
		}},
		{"image edit array", func() error {
			return client.ValidateImageEditParameters(openai.ImageEditRequest{Images: []openai.ImageAttachment{{}, {}}})
		}},
		{"speech PCM format", func() error {
			return client.ValidateAudioSpeechParameters(openai.AudioSpeechRequest{ResponseFormat: "pcm"})
		}},
		{"speech SSE stream", func() error {
			return client.ValidateAudioSpeechParameters(openai.AudioSpeechRequest{StreamFormat: "sse"})
		}},
		{"transcription stream", func() error {
			return client.ValidateAudioTranscriptionParameters(openai.AudioTranscriptionRequest{Stream: true})
		}},
		{"transcription MP3", func() error {
			return client.ValidateAudioTranscriptionParameters(openai.AudioTranscriptionRequest{File: openai.AudioAttachment{MediaType: "audio/mpeg"}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var providerError *Error
			if err := test.err(); !errors.As(err, &providerError) || providerError.Provider != "lemonade" || providerError.Class != FailureClientRequest {
				t.Fatalf("expected attributable client error, got %v", err)
			}
		})
	}
}

func TestLemonadeSchemaBoundsPreserveSemantics(t *testing.T) {
	for _, test := range []struct {
		name    string
		schema  any
		wantErr bool
	}{
		{"numeric constraints", map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "number", "minimum": 1, "maximum": 100, "exclusiveMinimum": 0, "default": 1}}}, false},
		{"length below threshold", map[string]any{"properties": map[string]any{"value": map[string]any{"minLength": 1999, "maxLength": 1999}}}, false},
		{"array below threshold", map[string]any{"properties": map[string]any{"value": map[string]any{"minItems": 2000, "maxItems": 2000}}}, false},
		{"length threshold", map[string]any{"properties": map[string]any{"value": map[string]any{"maxLength": 2000}}}, true},
		{"array threshold", map[string]any{"$defs": map[string]any{"value": map[string]any{"minItems": 2001}}}, true},
		{"union threshold", map[string]any{"anyOf": []any{map[string]any{"maxItems": 2001}}}, true},
		{"numeric overflow", map[string]any{"maxLength": 1e30}, true},
		{"negative bound", map[string]any{"maxLength": -1}, true},
		{"fractional bound", map[string]any{"maxItems": 1.5}, true},
		{"wrong type", map[string]any{"maxLength": "2000"}, true},
		{"non-object root", "string", true},
		{"literal annotation", map[string]any{"type": "object", "default": map[string]any{"maxLength": 9999}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateLemonadeToolSchema(test.schema); (err != nil) != test.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestLemonadeCapabilityProfile(t *testing.T) {
	for _, profile := range ManagedProviderCapabilityProfiles() {
		if profile.Type != "lemonade" {
			continue
		}
		for _, operation := range []string{"chat", "completions", "responses", "embeddings", "rerank", "image_generation", "image_edit", "image_variation", "audio_transcription", "audio_speech", "stream"} {
			if !containsString(profile.Operations, operation) {
				t.Errorf("operation %s omitted", operation)
			}
		}
		for _, unsupported := range []string{"file_input", "mcp", "background_responses", "realtime", "moderation", "fine_tuning", "video", "container", "audio_translation", "web_search"} {
			if containsString(profile.Capabilities, unsupported) {
				t.Errorf("unsupported capability %s advertised", unsupported)
			}
		}
		if !containsString(profile.ChatParameters.SupportedOptions, "repetition_penalty") || containsString(profile.ChatParameters.SupportedOptions, "service_tier") {
			t.Fatalf("incorrect parameter policy: %+v", profile.ChatParameters)
		}
		return
	}
	t.Fatal("Lemonade capability profile missing")
}
