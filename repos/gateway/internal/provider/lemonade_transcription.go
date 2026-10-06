package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"

	"ai-gateway-gateway/internal/openai"
)

func (Lemonade) ReserveAudioMilliseconds(request openai.AudioTranscriptionRequest) (int, error) {
	data, err := base64.StdEncoding.DecodeString(request.File.Data)
	if err != nil {
		return 0, openai.ErrInvalidAudio
	}
	duration, err := wavDurationMilliseconds(data)
	if err != nil || duration <= 0 || duration > 7*24*60*60*1000 {
		return 0, errors.New("invalid WAV duration")
	}
	return duration, nil
}

func decodeLemonadeTranscription(reader io.Reader, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxAudioTranscriptionResponseBytes+1))
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	if len(payload) > maxAudioTranscriptionResponseBytes {
		return openai.AudioTranscriptionResponse{}, errors.New("Lemonade transcription response exceeds limit")
	}
	var response *openai.AudioTranscriptionResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	if response == nil {
		return openai.AudioTranscriptionResponse{}, errors.New("Lemonade transcription response must be an object")
	}
	if response.Usage == nil {
		// Charge the measured input audio, not invented provider token counts.
		duration, err := (Lemonade{}).ReserveAudioMilliseconds(request)
		if err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
		response.Duration = float64(duration) / 1000
		response.Usage = &openai.AudioTranscriptionUsage{Type: "duration", InputAudioMilliseconds: duration}
	}
	if message := response.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, errors.New(message)
	}
	return *response, nil
}
