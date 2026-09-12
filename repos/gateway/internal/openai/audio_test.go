package openai

import (
	"encoding/base64"
	"math"
	"strings"
	"testing"
)

func testAudioAttachment() AudioAttachment {
	return AudioAttachment{Filename: "sample.wav", MediaType: "audio/wav", Data: base64.StdEncoding.EncodeToString([]byte("RIFF....WAVEdata"))}
}

func testSpeakerReference() AudioAttachment {
	attachment, err := ParseDataAudioURL("data:audio/wav;base64," + testAudioAttachment().Data)
	if err != nil {
		panic(err)
	}
	return attachment
}

func TestAudioTranscriptionRequestValidationAndReserve(t *testing.T) {
	temperature := 0.5
	prefix := 300
	silence := 500
	threshold := 0.45
	request := AudioTranscriptionRequest{Model: "transcribe", File: testAudioAttachment(), Prompt: "speaker names", ResponseFormat: "verbose_json", Temperature: &temperature, TimestampGranularities: []string{"word", "segment"}, Languages: []string{"en", "pt-BR"}, Keywords: []string{"Acme", "Jane Doe"}, ChunkingStrategy: &AudioChunkingStrategy{Type: "server_vad", PrefixPaddingMS: &prefix, SilenceDurationMS: &silence, Threshold: &threshold}, KnownSpeakerNames: []string{"Jane"}, KnownSpeakerReferences: []AudioAttachment{testSpeakerReference()}}
	if message := request.Validate(); message != "" {
		t.Fatal(message)
	}
	chunking, err := request.ChunkingStrategy.MultipartValue()
	if err != nil {
		t.Fatal(err)
	}
	wantInput := EstimateContextTokens(strings.Join([]string{"speaker names", "en", "pt-BR", "Acme", "Jane Doe", "Jane", chunking}, "\n")) + 2*((len([]byte("RIFF....WAVEdata"))+3)/4)
	if got := AudioTranscriptionInputTokens(request); got != wantInput {
		t.Fatalf("input tokens=%d want %d", got, wantInput)
	}
	if got := AudioTranscriptionReserveTokens(request); got != wantInput+DefaultOutputTokenReserve {
		t.Fatalf("reserve=%d", got)
	}
}

func TestAudioTranscriptionModeIsValidatedAndAccounted(t *testing.T) {
	baseline := AudioTranscriptionRequest{Model: "transcribe", File: testAudioAttachment()}
	smart := baseline
	smart.Mode = "SMART"
	if message := smart.Validate(); message != "" {
		t.Fatal(message)
	}
	if AudioTranscriptionInputTokens(smart) <= AudioTranscriptionInputTokens(baseline) {
		t.Fatal("transcription mode omitted from token estimate")
	}
	invalid := baseline
	invalid.Mode = "smart"
	if invalid.Validate() == "" {
		t.Fatal("invalid transcription mode accepted")
	}
}

func TestKnownSpeakerReferencesAreBoundedDataAudioURLs(t *testing.T) {
	reference := testSpeakerReference()
	if reference.MediaType != "audio/wav" || reference.DataURL() != "data:audio/wav;base64,"+testAudioAttachment().Data {
		t.Fatalf("reference=%+v", reference)
	}
	for _, value := range []string{"https://example.test/sample.wav", "data:text/plain;base64,SGVsbG8=", "data:audio/wav;base64,bm90IGF1ZGlv", "data:audio/wav;base64,%%%"} {
		if _, err := ParseDataAudioURL(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	oversized := append([]byte("RIFF....WAVE"), make([]byte, MaxKnownSpeakerReferenceBytes)...)
	if _, err := ParseDataAudioURL("data:audio/wav;base64," + base64.StdEncoding.EncodeToString(oversized)); err == nil {
		t.Fatal("oversized speaker reference was accepted")
	}
	five := []AudioAttachment{reference, reference, reference, reference, reference}
	if err := ValidateKnownSpeakerReferences(five); err == nil {
		t.Fatal("too many speaker references were accepted")
	}
}

func TestParseAudioChunkingStrategy(t *testing.T) {
	for _, value := range []string{"auto", `{"type":"server_vad"}`, `{"type":"server_vad","prefix_padding_ms":250,"silence_duration_ms":600,"threshold":0.4}`} {
		strategy, err := ParseAudioChunkingStrategy(value)
		if err != nil {
			t.Fatalf("value=%s err=%v", value, err)
		}
		encoded, err := strategy.MultipartValue()
		if err != nil || encoded == "" {
			t.Fatalf("strategy=%+v encoded=%q err=%v", strategy, encoded, err)
		}
	}
	for _, value := range []string{"", "null", `{"type":"automatic"}`, `{"type":"auto","threshold":0.5}`, `{"type":"server_vad","threshold":2}`, `{"type":"server_vad","extra":1}`, `{"type":"server_vad"}{}`} {
		if _, err := ParseAudioChunkingStrategy(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestAudioTranscriptionRequestRejectsUnsafeInput(t *testing.T) {
	badTemperature := math.NaN()
	badThreshold := math.Inf(1)
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
		{Model: "m", File: testAudioAttachment(), Languages: []string{"english"}},
		{Model: "m", File: testAudioAttachment(), Keywords: []string{" "}},
		{Model: "m", File: testAudioAttachment(), ChunkingStrategy: &AudioChunkingStrategy{Type: "server_vad", Threshold: &badThreshold}},
		{Model: "m", File: testAudioAttachment(), KnownSpeakerNames: []string{"Jane"}},
		{Model: "m", File: testAudioAttachment(), KnownSpeakerNames: []string{" "}, KnownSpeakerReferences: []AudioAttachment{testSpeakerReference()}},
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
