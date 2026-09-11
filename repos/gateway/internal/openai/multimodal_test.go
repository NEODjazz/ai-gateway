package openai

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestChatImageAttachmentsAcceptsBoundedDataURL(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))
	messages := []Message{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "describe"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + data}},
	}}}
	attachments, err := ChatImageAttachments(messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].MediaType != "image/png" || attachments[0].Data != data {
		t.Fatalf("unexpected attachments: %+v", attachments)
	}
}

func TestImageAttachmentsRejectsRemoteAndMalformedImages(t *testing.T) {
	for _, imageURL := range []string{"https://example.test/image.png", "data:image/svg+xml;base64,PHN2Zz4=", "data:image/png;base64,%%%"} {
		messages := []Message{{Role: "user", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL}}}}}
		if _, err := ChatImageAttachments(messages); err == nil {
			t.Errorf("image URL %q was accepted", imageURL)
		}
	}
}

func TestImageAttachmentsRejectsNonUserChatImage(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))
	messages := []Message{{Role: "assistant", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + data}}}}}
	if _, err := ChatImageAttachments(messages); err == nil {
		t.Fatal("assistant image was accepted")
	}
}

func TestResponseAudioAttachmentsValidateFormatSignatureAndBounds(t *testing.T) {
	wav := base64.StdEncoding.EncodeToString([]byte("RIFF\x00\x00\x00\x00WAVE"))
	input := []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": wav, "format": "wav"}}}
	attachments, err := ResponseAudioAttachments(input)
	if err != nil || len(attachments) != 1 || attachments[0].MediaType != "audio/wav" || attachments[0].Data != wav {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
	for _, invalid := range []any{
		[]any{map[string]any{"type": "input_audio", "input_audio": "bad"}},
		[]any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "%%%", "format": "wav"}}},
		[]any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": wav, "format": "flac"}}},
	} {
		if _, err := ResponseAudioAttachments(invalid); err == nil {
			t.Fatalf("invalid audio accepted: %#v", invalid)
		}
	}
}

func TestTextProjectionExcludesAndRestoresImagePayload(t *testing.T) {
	secretImage := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("binary-secret"))
	original := []any{
		map[string]any{"type": "text", "text": "user@example.com"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": secretImage}},
	}
	projection := TextOnlyProjection(original)
	if strings.Contains(string(mustJSON(t, projection)), secretImage) {
		t.Fatal("text projection contains image payload")
	}
	transformed := projection.([]any)
	transformed[0].(map[string]any)["text"] = "{{EMAIL_1}}"
	merged := MergeTextProjection(original, transformed).([]any)
	if merged[0].(map[string]any)["text"] != "{{EMAIL_1}}" {
		t.Fatalf("text was not merged: %+v", merged)
	}
	image := merged[1].(map[string]any)["image_url"].(map[string]any)["url"]
	if image != secretImage {
		t.Fatalf("image payload changed: %v", image)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
