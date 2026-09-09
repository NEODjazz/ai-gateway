package openai

import (
	"strings"
	"testing"
)

func TestAudioSpeechRequestValidationAndAccounting(t *testing.T) {
	speed := 1.25
	request := AudioSpeechRequest{Model: "tts", Input: "Привет 👋", Voice: "alloy", Instructions: "Speak clearly", ResponseFormat: "wav", Speed: &speed, StreamFormat: "audio"}
	if message := request.Validate(); message != "" {
		t.Fatal(message)
	}
	if request.InputCharacters() != 8 {
		t.Fatalf("characters=%d", request.InputCharacters())
	}
	if AudioSpeechReserveTokens(request) <= 0 || request.ExpectedContentType() != "audio/wav" {
		t.Fatalf("reserve=%d content_type=%q", AudioSpeechReserveTokens(request), request.ExpectedContentType())
	}
}

func TestAudioSpeechRequestRejectsUnsupportedInput(t *testing.T) {
	tooSlow, tooFast := 0.24, 4.01
	for _, request := range []AudioSpeechRequest{
		{},
		{Model: "tts", Input: "hello", Voice: "alloy", ResponseFormat: "json"},
		{Model: "tts", Input: "hello", Voice: "alloy", StreamFormat: "sse"},
		{Model: "tts", Input: "hello", Voice: "alloy", Speed: &tooSlow},
		{Model: "tts", Input: "hello", Voice: "alloy", Speed: &tooFast},
		{Model: "tts", Input: strings.Repeat("x", MaxSpeechInputCharacters+1), Voice: "alloy"},
	} {
		if request.Validate() == "" {
			t.Fatalf("accepted invalid request: %+v", request)
		}
	}
}
