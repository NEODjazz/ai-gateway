package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

type audioSpeechStreamEvent struct {
	Type  string                   `json:"type"`
	Audio string                   `json:"audio,omitempty"`
	Usage *openai.AudioSpeechUsage `json:"usage,omitempty"`
}

func streamAudioSpeech(body io.Reader, write AudioSpeechStreamWriter) (openai.AudioSpeechUsage, error) {
	decodedBytes := 0
	done := false
	var usage openai.AudioSpeechUsage
	err := scanSSEData(body, func(payload string) error {
		if done {
			return errors.New("provider returned audio speech events after completion")
		}
		var event audioSpeechStreamEvent
		decoder := json.NewDecoder(strings.NewReader(payload))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("provider returned an invalid audio speech stream event")
		}
		switch event.Type {
		case "speech.audio.delta":
			if event.Audio == "" || event.Usage != nil {
				return errors.New("provider returned an invalid audio speech delta")
			}
			chunk, err := base64.StdEncoding.DecodeString(event.Audio)
			if err != nil || len(chunk) == 0 || decodedBytes > maxAudioSpeechResponseBytes-len(chunk) {
				return errors.New("provider returned invalid or oversized audio speech data")
			}
			decodedBytes += len(chunk)
		case "speech.audio.done":
			if event.Audio != "" || event.Usage == nil || event.Usage.Validate() != "" || decodedBytes == 0 {
				return errors.New("provider returned an invalid audio speech completion")
			}
			usage = *event.Usage
			done = true
		default:
			return errors.New("provider returned an unsupported audio speech stream event")
		}
		return write(payload)
	})
	if err != nil {
		return openai.AudioSpeechUsage{}, err
	}
	if !done {
		return openai.AudioSpeechUsage{}, errors.New("provider audio speech stream ended before completion")
	}
	return usage, nil
}
