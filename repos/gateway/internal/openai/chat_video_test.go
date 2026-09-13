package openai

import (
	"encoding/base64"
	"testing"
)

func TestChatVideoAttachmentsValidateFormatsRolesAndLimits(t *testing.T) {
	tests := []struct {
		format string
		data   []byte
		media  string
	}{
		{format: "mp4", data: []byte("\x00\x00\x00\x18ftypisom"), media: "video/mp4"},
		{format: "webm", data: []byte("\x1a\x45\xdf\xa3content"), media: "video/webm"},
		{format: "mpeg", data: []byte("\x00\x00\x01\xbapayload"), media: "video/mpeg"},
		{format: "mpg", data: []byte("\x00\x00\x01\xb3payload"), media: "video/mpg"},
		{format: "mov", data: []byte("\x00\x00\x00\x18ftypqt  "), media: "video/mov"},
		{format: "avi", data: []byte("RIFF\x00\x00\x00\x00AVI payload"), media: "video/avi"},
		{format: "flv", data: []byte("FLV\x01\x05payload"), media: "video/x-flv"},
		{format: "wmv", data: []byte("\x30\x26\xb2\x75\x8e\x66\xcf\x11\xa6\xd9\x00\xaa\x00\x62\xce\x6cpayload"), media: "video/wmv"},
		{format: "3gpp", data: []byte("\x00\x00\x00\x18ftyp3gp5"), media: "video/3gpp"},
	}
	for _, test := range tests {
		part := map[string]any{"type": "input_video", "input_video": map[string]any{"data": base64.StdEncoding.EncodeToString(test.data), "format": test.format}}
		request := ChatCompletionRequest{Messages: []Message{{Role: "user", Content: []any{part}}}}
		attachments, err := ChatVideoAttachments(request.Messages)
		if err != nil || len(attachments) != 1 || attachments[0].MediaType != test.media || !HasChatVideoInput(request) {
			t.Fatalf("format=%s attachments=%+v err=%v", test.format, attachments, err)
		}
		request.Messages[0].Role = "assistant"
		if _, err := ChatVideoAttachments(request.Messages); err == nil {
			t.Fatalf("assistant %s video accepted", test.format)
		}
	}
	many := make([]any, MaxChatVideoAttachments+1)
	for index := range many {
		many[index] = map[string]any{"type": "input_video", "input_video": map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("\x00\x00\x00\x18ftypisom")), "format": "mp4"}}
	}
	if _, err := ChatVideoAttachments([]Message{{Role: "user", Content: many}}); err == nil {
		t.Fatal("too many videos accepted")
	}
}

func TestChatVideoAttachmentsRejectMalformedInputs(t *testing.T) {
	invalid := []any{
		map[string]any{"type": "input_video", "input_video": map[string]any{"data": "bm90LXZpZGVv", "format": "mp4"}},
		map[string]any{"type": "input_video", "input_video": map[string]any{"data": "GkXfo2NvbnRlbnQ=", "format": "avi"}},
		map[string]any{"type": "input_video", "input_video": map[string]any{"data": "%%%", "format": "webm"}},
		map[string]any{"type": "input_video", "input_video": map[string]any{"data": "GkXfo2NvbnRlbnQ=", "format": "webm", "extra": true}},
		map[string]any{"type": "input_video", "input_video": map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("\x00\x00\x00\x18ftypisom")), "format": "mov"}},
		map[string]any{"type": "input_video", "input_video": map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("RIFF\x00\x00\x00\x00WAVEpayload")), "format": "avi"}},
	}
	for _, part := range invalid {
		if _, err := ChatVideoAttachments([]Message{{Role: "user", Content: []any{part}}}); err == nil {
			t.Fatalf("invalid video accepted: %#v", part)
		}
	}
}
