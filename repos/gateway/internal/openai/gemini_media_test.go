package openai

import "testing"

func TestGeminiPartMediaResolutionValidation(t *testing.T) {
	request := ChatCompletionRequest{Messages: []Message{{Role: "user", Content: []any{
		map[string]any{"type": "input_video", "input_video": map[string]any{"data": "value", "format": "mp4"}, "gemini_media_resolution": "MEDIA_RESOLUTION_ULTRA_HIGH"},
	}}}}
	if !HasChatGeminiPartMediaResolution(request) || ValidateChatGeminiPartMediaResolutions(request) != nil {
		t.Fatal("valid per-part resolution rejected")
	}
	request.Messages[0].Role = "assistant"
	if ValidateChatGeminiPartMediaResolutions(request) == nil {
		t.Fatal("assistant media resolution accepted")
	}
	request.Messages[0].Role = "user"
	request.Messages[0].Content = []any{map[string]any{"type": "input_video", "gemini_media_resolution": "invalid"}}
	if ValidateChatGeminiPartMediaResolutions(request) == nil {
		t.Fatal("invalid media resolution accepted")
	}
}
