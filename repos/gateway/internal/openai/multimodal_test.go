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
	formats := []struct {
		format, mediaType string
		data              []byte
	}{
		{"wav", "audio/wav", []byte("RIFF\x00\x00\x00\x00WAVE")},
		{"mp3", "audio/mpeg", []byte("ID3payload")},
		{"flac", "audio/flac", []byte("fLaCpayload")},
		{"ogg", "audio/ogg", []byte("OggSpayload")},
		{"opus", "audio/opus", []byte("OggSpayload")},
		{"aiff", "audio/aiff", []byte("FORM\x00\x00\x00\x00AIFFpayload")},
		{"aac", "audio/aac", []byte("\xff\xf1\x50\x80\x00\x1f\xfc")},
		{"webm", "audio/webm", []byte("\x1a\x45\xdf\xa3payload")},
		{"m4a", "audio/m4a", []byte("\x00\x00\x00\x18ftypisom")},
	}
	for _, format := range formats {
		encoded := base64.StdEncoding.EncodeToString(format.data)
		input := []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": encoded, "format": format.format}}}
		attachments, err := ResponseAudioAttachments(input)
		if err != nil || len(attachments) != 1 || attachments[0].MediaType != format.mediaType || attachments[0].Data != encoded {
			t.Fatalf("format=%s attachments=%+v err=%v", format.format, attachments, err)
		}
	}
	wav := base64.StdEncoding.EncodeToString([]byte("RIFF\x00\x00\x00\x00WAVE"))
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

func TestChatAudioAttachmentsRequireUserRole(t *testing.T) {
	wav := base64.StdEncoding.EncodeToString([]byte("RIFF\x00\x00\x00\x00WAVE"))
	content := []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": wav, "format": "wav"}}}
	attachments, err := ChatAudioAttachments([]Message{{Role: "user", Content: content}})
	if err != nil || len(attachments) != 1 || attachments[0].MediaType != "audio/wav" {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
	if _, err := ChatAudioAttachments([]Message{{Role: "assistant", Content: content}}); err == nil {
		t.Fatal("assistant audio input accepted")
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

func TestTextProjectionExcludesVideoPayload(t *testing.T) {
	secretVideo := base64.StdEncoding.EncodeToString([]byte("\x00\x00\x00\x18ftypprivate-video"))
	original := []any{
		map[string]any{"type": "text", "text": "describe"},
		map[string]any{"type": "input_video", "input_video": map[string]any{"data": secretVideo, "format": "mp4"}},
	}
	projection := TextOnlyProjection(original)
	if strings.Contains(string(mustJSON(t, projection)), secretVideo) {
		t.Fatal("text projection contains video payload")
	}
	transformed := TransformTextContent(original, strings.ToUpper).([]any)
	if transformed[1].(map[string]any)["input_video"].(map[string]any)["data"] != secretVideo {
		t.Fatal("video payload was transformed as text")
	}
}

func TestTextProjectionTransformsAndMergesStringSlices(t *testing.T) {
	original := []string{"first@example.com", "second@example.com"}
	projection, ok := TextOnlyProjection(original).([]string)
	if !ok || len(projection) != 2 || projection[0] != original[0] {
		t.Fatalf("projection=%#v", projection)
	}
	projection[0] = "changed"
	if original[0] == "changed" {
		t.Fatal("text projection aliases the original string slice")
	}

	transformed, ok := TransformTextContent(append([]string(nil), original...), strings.ToUpper).([]string)
	if !ok || transformed[0] != "FIRST@EXAMPLE.COM" || transformed[1] != "SECOND@EXAMPLE.COM" {
		t.Fatalf("transformed=%#v", transformed)
	}
	merged, ok := MergeTextProjection(original, []any{"{{EMAIL_1}}", "{{EMAIL_2}}"}).([]string)
	if !ok || merged[0] != "{{EMAIL_1}}" || merged[1] != "{{EMAIL_2}}" {
		t.Fatalf("merged=%#v", merged)
	}
	if invalid := MergeTextProjection(original, []any{"only one"}).([]string); invalid[0] != original[0] || len(invalid) != len(original) {
		t.Fatalf("invalid projection changed input=%#v", invalid)
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
