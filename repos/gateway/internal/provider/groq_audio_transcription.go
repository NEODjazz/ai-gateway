package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const (
	groqMinimumBilledAudioMilliseconds = 10 * 1000
	groqMaximumBilledAudioMilliseconds = 7 * 24 * 60 * 60 * 1000
)

func (Groq) ReserveAudioMilliseconds(request openai.AudioTranscriptionRequest) (int, error) {
	data, err := base64.StdEncoding.DecodeString(request.File.Data)
	if err != nil {
		return 0, openai.ErrInvalidAudio
	}
	switch strings.ToLower(request.File.MediaType) {
	case "audio/wav", "audio/wave", "audio/x-wav":
		duration, err := wavDurationMilliseconds(data)
		if err != nil || duration > groqMaximumBilledAudioMilliseconds {
			return 0, errors.New("WAV duration is invalid")
		}
		return max(duration, groqMinimumBilledAudioMilliseconds), nil
	case "audio/flac":
		duration, err := flacDurationMilliseconds(data)
		if err != nil || duration > groqMaximumBilledAudioMilliseconds {
			return 0, errors.New("FLAC duration is invalid")
		}
		return max(duration, groqMinimumBilledAudioMilliseconds), nil
	case "audio/ogg":
		duration, err := oggDurationMilliseconds(data)
		if err != nil || duration > groqMaximumBilledAudioMilliseconds {
			return 0, errors.New("OGG duration is invalid")
		}
		return max(duration, groqMinimumBilledAudioMilliseconds), nil
	case "audio/mpeg", "audio/mp3":
		duration, err := mp3DurationMilliseconds(data)
		if err != nil || duration > groqMaximumBilledAudioMilliseconds {
			return 0, errors.New("MP3 duration is invalid")
		}
		return max(duration, groqMinimumBilledAudioMilliseconds), nil
	case "audio/mp4", "video/mp4", "audio/x-m4a":
		duration, err := mp4DurationMilliseconds(data)
		if err != nil || duration > groqMaximumBilledAudioMilliseconds {
			return 0, errors.New("MP4 audio track duration is invalid")
		}
		return max(duration, groqMinimumBilledAudioMilliseconds), nil
	case "audio/webm", "video/webm":
		duration, err := webmAudioDurationMilliseconds(data)
		if err != nil || duration > groqMaximumBilledAudioMilliseconds {
			return 0, errors.New("audio-only WebM duration is invalid")
		}
		return max(duration, groqMinimumBilledAudioMilliseconds), nil
	default:
		return 0, errors.New("Groq transcription requires a supported audio container with reliable duration metadata")
	}
}

func (g Groq) ReserveTranslationAudioMilliseconds(request openai.AudioTranscriptionRequest) (int, error) {
	return g.ReserveAudioMilliseconds(request)
}

func (g Groq) TranscribeAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	if err := g.validateAudioRequest(request); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	return g.sendAudioRequest(ctx, request, "audio/transcriptions", true)
}

func (g Groq) TranslateAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	if err := g.validateAudioRequest(request); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	if request.Language != "" && request.Language != "en" {
		return openai.AudioTranscriptionResponse{}, groqAudioClientError("unsupported_parameter", "language", "Groq audio translation only accepts language=en")
	}
	if len(request.TimestampGranularities) > 0 {
		return openai.AudioTranscriptionResponse{}, groqAudioClientError("unsupported_parameter", "timestamp_granularities", "Groq audio translation does not support timestamp granularities")
	}
	return g.sendAudioRequest(ctx, request, "audio/translations", false)
}

func (g Groq) validateAudioRequest(request openai.AudioTranscriptionRequest) error {
	if message := request.Validate(); message != "" {
		return groqAudioClientError("invalid_request", "", message)
	}
	if err := rejectParameters("groq",
		parameterCheck{"include", len(request.Include) > 0},
		parameterCheck{"languages", len(request.Languages) > 0},
		parameterCheck{"keywords", len(request.Keywords) > 0},
		parameterCheck{"chunking_strategy", request.ChunkingStrategy != nil},
		parameterCheck{"known_speaker_names", len(request.KnownSpeakerNames) > 0},
		parameterCheck{"known_speaker_references", len(request.KnownSpeakerReferences) > 0},
		parameterCheck{"response_format", request.ResponseFormat == "diarized_json"},
	); err != nil {
		return err
	}
	if request.Prompt != "" && openai.EstimateContextTokens(request.Prompt) > 224 {
		return groqAudioClientError("invalid_request", "prompt", "prompt exceeds the 224 token limit")
	}
	return nil
}

func (g Groq) sendAudioRequest(ctx context.Context, request openai.AudioTranscriptionRequest, path string, includeTimestamps bool) (openai.AudioTranscriptionResponse, error) {
	billableDuration, err := g.ReserveAudioMilliseconds(request)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, groqAudioClientError("unsupported_audio", "file", err.Error())
	}
	actualDuration := billableDuration
	if billableDuration == groqMinimumBilledAudioMilliseconds {
		data, _ := base64.StdEncoding.DecodeString(request.File.Data)
		switch strings.ToLower(request.File.MediaType) {
		case "audio/wav", "audio/wave", "audio/x-wav":
			actualDuration, _ = wavDurationMilliseconds(data)
		case "audio/flac":
			actualDuration, _ = flacDurationMilliseconds(data)
		case "audio/ogg":
			actualDuration, _ = oggDurationMilliseconds(data)
		case "audio/mpeg", "audio/mp3":
			actualDuration, _ = mp3DurationMilliseconds(data)
		case "audio/mp4", "video/mp4", "audio/x-m4a":
			actualDuration, _ = mp4DurationMilliseconds(data)
		case "audio/webm", "video/webm":
			actualDuration, _ = webmAudioDurationMilliseconds(data)
		}
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"model": request.Model, "language": request.Language, "prompt": request.Prompt,
		"response_format": "verbose_json",
	}
	if request.Temperature != nil {
		fields["temperature"] = strconv.FormatFloat(*request.Temperature, 'g', -1, 64)
	}
	for name, value := range fields {
		if value != "" {
			if err := writer.WriteField(name, value); err != nil {
				return openai.AudioTranscriptionResponse{}, err
			}
		}
	}
	if includeTimestamps {
		for _, value := range request.TimestampGranularities {
			if err := writer.WriteField("timestamp_granularities[]", value); err != nil {
				return openai.AudioTranscriptionResponse{}, err
			}
		}
	}
	if err := writeAudioPart(writer, request.File); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	if err := writer.Close(); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(g.compatible.baseURL, path), &body)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", writer.FormDataContentType())
	if g.compatible.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+g.compatible.apiKey)
	}
	response, err := g.compatible.client.Do(httpRequest)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.AudioTranscriptionResponse{}, responseStatusError("groq", response)
	}
	return decodeGroqAudioTranscription(response.Body, actualDuration, billableDuration)
}

func groqAudioClientError(code, param, message string) error {
	return &Error{Class: FailureClientRequest, Provider: "groq", StatusCode: http.StatusBadRequest, UpstreamCode: code, Param: param, Err: errors.New(message)}
}

func decodeGroqAudioTranscription(reader io.Reader, actualDuration, billableDuration int) (openai.AudioTranscriptionResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxAudioTranscriptionResponseBytes+1))
	if err != nil || len(payload) > maxAudioTranscriptionResponseBytes {
		return openai.AudioTranscriptionResponse{}, errors.New("audio transcription response exceeds limit")
	}
	var result *openai.AudioTranscriptionResponse
	if err := json.Unmarshal(payload, &result); err != nil || result == nil {
		return openai.AudioTranscriptionResponse{}, errors.New("invalid Groq transcription response")
	}
	result.Duration = float64(actualDuration) / 1000
	result.Usage = &openai.AudioTranscriptionUsage{Type: "duration", InputAudioMilliseconds: billableDuration}
	if message := result.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, errors.New(message)
	}
	return *result, nil
}
