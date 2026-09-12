package provider

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

type audioTranscriptionStreamEvent struct {
	Type      string                              `json:"type"`
	Delta     string                              `json:"delta,omitempty"`
	Text      string                              `json:"text,omitempty"`
	SegmentID string                              `json:"segment_id,omitempty"`
	ID        any                                 `json:"id,omitempty"`
	Speaker   string                              `json:"speaker,omitempty"`
	Start     float64                             `json:"start,omitempty"`
	End       float64                             `json:"end,omitempty"`
	Languages []openai.AudioTranscriptionLanguage `json:"languages,omitempty"`
	Logprobs  []openai.AudioTranscriptionLogprob  `json:"logprobs,omitempty"`
	Usage     *openai.AudioTranscriptionUsage     `json:"usage,omitempty"`
}

func streamAudioTranscription(body io.Reader, request openai.AudioTranscriptionRequest, write AudioTranscriptionStreamWriter) (openai.AudioTranscriptionResponse, error) {
	var transcript strings.Builder
	var result openai.AudioTranscriptionResponse
	done := false
	err := scanSSEData(body, func(payload string) error {
		if done {
			return errors.New("provider returned transcription events after completion")
		}
		var event audioTranscriptionStreamEvent
		decoder := json.NewDecoder(strings.NewReader(payload))
		if err := decoder.Decode(&event); err != nil {
			return errors.New("provider returned an invalid transcription stream event")
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return errors.New("provider returned trailing transcription stream data")
		}
		switch event.Type {
		case "transcript.text.delta":
			if event.Delta == "" || utf8.RuneCountInString(event.SegmentID) > 256 || transcript.Len() > maxAudioTranscriptionResponseBytes-len(event.Delta) || !validStreamTranscriptionLogprobs(event.Logprobs) {
				return errors.New("provider returned invalid transcription delta")
			}
			transcript.WriteString(event.Delta)
		case "transcript.text.segment":
			if request.ResponseFormat != "diarized_json" {
				return errors.New("provider returned a diarized segment for another response format")
			}
			probe := openai.AudioTranscriptionResponse{Text: "valid", Segments: []openai.AudioTranscriptionSegment{{ID: event.ID, Speaker: event.Speaker, Text: event.Text, Start: event.Start, End: event.End}}, Usage: validStreamTranscriptionUsage()}
			if probe.Validate() != "" {
				return errors.New("provider returned invalid transcription segment")
			}
		case "transcript.text.done":
			if event.Text != transcript.String() || !validStreamTranscriptionLogprobs(event.Logprobs) {
				return errors.New("provider returned inconsistent transcription completion")
			}
			result = openai.AudioTranscriptionResponse{Text: event.Text, Languages: event.Languages, Logprobs: event.Logprobs, Usage: event.Usage}
			if message := result.Validate(); message != "" {
				return errors.New(message)
			}
			done = true
		default:
			return errors.New("provider returned an unsupported transcription stream event")
		}
		return write(payload)
	})
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	if !done {
		return openai.AudioTranscriptionResponse{}, errors.New("provider transcription stream ended before completion")
	}
	return result, nil
}

func validStreamTranscriptionLogprobs(values []openai.AudioTranscriptionLogprob) bool {
	probe := openai.AudioTranscriptionResponse{Text: "valid", Logprobs: values, Usage: validStreamTranscriptionUsage()}
	return probe.Validate() == ""
}

func validStreamTranscriptionUsage() *openai.AudioTranscriptionUsage {
	return &openai.AudioTranscriptionUsage{Type: "tokens", InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
}
