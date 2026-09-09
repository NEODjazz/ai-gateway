package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatAudioRequestValidation(t *testing.T) {
	for _, test := range []struct {
		body  string
		valid bool
	}{
		{`{"modalities":["text"]}`, true},
		{`{"modalities":["audio"],"audio":{"format":"mp3","voice":"alloy"}}`, true},
		{`{"modalities":["text","audio"],"audio":{"format":"pcm16","voice":{"id":"voice-1"}}}`, true},
		{`{"modalities":["audio"]}`, false},
		{`{"modalities":["audio"],"audio":{"format":"ogg","voice":"alloy"}}`, false},
		{`{"modalities":["audio"],"audio":{"format":"mp3","voice":""}}`, false},
		{`{"modalities":["audio"],"audio":{"format":"mp3","voice":{"id":""}}}`, false},
		{`{"modalities":["audio"],"audio":{"format":"mp3","voice":{"id":"voice-1","extra":true}}}`, false},
		{`{"modalities":["text","text"]}`, false},
		{`{"modalities":["video"]}`, false},
		{`{"audio":{"format":"mp3","voice":"alloy"}}`, false},
	} {
		t.Run(test.body, func(t *testing.T) {
			var options ChatGenerationOptions
			err := json.Unmarshal([]byte(test.body), &options)
			valid := err == nil && options.Validate() == ""
			if valid != test.valid {
				t.Fatalf("valid=%v decode=%v validation=%q", valid, err, options.Validate())
			}
			if valid {
				encoded, err := json.Marshal(options)
				if err != nil || !strings.Contains(string(encoded), `"audio"`) && ChatRequestsAudio(ChatCompletionRequest{ChatGenerationOptions: options}) {
					t.Fatalf("audio options did not round trip: %s %v", encoded, err)
				}
			}
		})
	}
}

func TestValidateChatAudioReferenceAndResponse(t *testing.T) {
	if err := ValidateChatAudioReference(&ChatAudio{ID: "audio-1"}); err != nil {
		t.Fatal(err)
	}
	data, transcript, expires := "aGVsbG8=", "hello", int64(123)
	complete := &ChatAudio{ID: "audio-1", Data: &data, Transcript: &transcript, ExpiresAt: &expires}
	if err := ValidateChatAudioResponse(complete); err != nil {
		t.Fatal(err)
	}
	invalidData := "not base64!"
	for _, audio := range []*ChatAudio{
		{},
		complete,
		{ID: "audio-1", Data: &invalidData, Transcript: &transcript, ExpiresAt: &expires},
	} {
		if audio == complete {
			if err := ValidateChatAudioReference(audio); err == nil {
				t.Fatal("response-only audio fields accepted in history")
			}
			continue
		}
		if err := ValidateChatAudioResponse(audio); err == nil {
			t.Fatal("invalid audio response accepted")
		}
	}
}
