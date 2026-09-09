package openai

import (
	"encoding/base64"
	"math"
	"testing"
)

func testAudioAttachment() AudioAttachment {
	return AudioAttachment{Filename: "sample.wav", MediaType: "audio/wav", Data: base64.StdEncoding.EncodeToString([]byte("RIFF....WAVEdata"))}
}

func TestAudioTranscriptionRequestValidationAndReserve(t *testing.T) {
	temperature := 0.5
	request := AudioTranscriptionRequest{Model: "transcribe", File: testAudioAttachment(), Prompt: "speaker names", ResponseFormat: "verbose_json", Temperature: &temperature, TimestampGranularities: []string{"word", "segment"}}
	if message := request.Validate(); message != "" {
		t.Fatal(message)
	}
	wantInput := EstimateContextTokens(request.Prompt) + (len([]byte("RIFF....WAVEdata"))+3)/4
	if got := AudioTranscriptionInputTokens(request); got != wantInput {
		t.Fatalf("input tokens=%d want %d", got, wantInput)
	}
	if got := AudioTranscriptionReserveTokens(request); got != wantInput+DefaultOutputTokenReserve {
		t.Fatalf("reserve=%d", got)
	}
}

func TestAudioTranscriptionRequestRejectsUnsafeInput(t *testing.T) {
	badTemperature := math.NaN()
	for _, request := range []AudioTranscriptionRequest{
		{},
		{Model: "m", File: AudioAttachment{Filename: "../sample.wav", MediaType: "audio/wav", Data: testAudioAttachment().Data}},
		{Model: "m", File: AudioAttachment{Filename: "sample.wav", MediaType: "audio/wav", Data: base64.StdEncoding.EncodeToString([]byte("not audio"))}},
		{Model: "m", File: testAudioAttachment(), ResponseFormat: "text"},
		{Model: "m", File: testAudioAttachment(), Temperature: &badTemperature},
		{Model: "m", File: testAudioAttachment(), TimestampGranularities: []string{"word", "word"}},
		{Model: "m", File: testAudioAttachment(), TimestampGranularities: []string{"word"}, ResponseFormat: "json"},
		{Model: "m", File: testAudioAttachment(), Include: []string{"unknown"}},
		{Model: "m", File: testAudioAttachment(), Include: []string{"logprobs"}, ResponseFormat: "verbose_json"},
	} {
		if message := request.Validate(); message == "" {
			t.Fatalf("accepted invalid request: %+v", request)
		}
	}
}

func TestAudioTranscriptionResponseRequiresExactUsage(t *testing.T) {
	valid := AudioTranscriptionResponse{Text: "hello", Duration: 1, Words: []AudioTranscriptionWord{{Word: "hello", Start: 0, End: 1}}, Usage: &AudioTranscriptionUsage{Type: "tokens", InputTokens: 3, OutputTokens: 1, TotalTokens: 4}}
	if message := valid.Validate(); message != "" {
		t.Fatal(message)
	}
	invalid := []AudioTranscriptionResponse{
		{},
		{Text: "hello", Usage: nil},
		{Text: "hello", Usage: &AudioTranscriptionUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 3}},
		{Text: "hello", Duration: math.Inf(1), Usage: valid.Usage},
		{Text: "hello", Words: []AudioTranscriptionWord{{Word: "bad", Start: 2, End: 1}}, Usage: valid.Usage},
	}
	for _, response := range invalid {
		if message := response.Validate(); message == "" {
			t.Fatalf("accepted invalid response: %+v", response)
		}
	}
	diarized := AudioTranscriptionResponse{Text: "hello", Duration: 1, Segments: []AudioTranscriptionSegment{{ID: "seg_1", Type: "transcript.text.segment", Speaker: "A", Text: "hello", Start: 0, End: 1}}, Usage: valid.Usage}
	if message := diarized.Validate(); message != "" {
		t.Fatalf("diarized response: %s", message)
	}
}
