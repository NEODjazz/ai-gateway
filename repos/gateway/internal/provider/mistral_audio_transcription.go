package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const mistralMaxAudioMilliseconds = 60 * 60 * 1000

func (Mistral) ReserveAudioMilliseconds(request openai.AudioTranscriptionRequest) (int, error) {
	data, err := base64.StdEncoding.DecodeString(request.File.Data)
	if err != nil {
		return 0, openai.ErrInvalidAudio
	}
	switch strings.ToLower(request.File.MediaType) {
	case "audio/wav", "audio/wave", "audio/x-wav":
		duration, err := wavDurationMilliseconds(data)
		if err != nil || duration <= 0 || duration > mistralMaxAudioMilliseconds {
			return 0, errors.New("WAV duration is invalid or exceeds 60 minutes")
		}
		return duration, nil
	case "audio/flac", "audio/ogg", "audio/webm", "video/webm", "audio/mpeg", "audio/mp3":
		// Compressed containers need codec-aware parsing. Reserve the documented
		// provider limit so a request can never bypass a duration-priced budget.
		return mistralMaxAudioMilliseconds, nil
	default:
		return 0, errors.New("Mistral transcription supports WAV, MP3, FLAC, OGG and WEBM audio")
	}
}

func wavDurationMilliseconds(data []byte) (int, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, openai.ErrInvalidAudio
	}
	byteRate := uint32(0)
	dataSize := uint32(0)
	for offset := 12; offset+8 <= len(data); {
		size := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		start := offset + 8
		end64 := uint64(start) + uint64(size)
		if end64 > uint64(len(data)) {
			return 0, openai.ErrInvalidAudio
		}
		end := int(end64)
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if size < 16 {
				return 0, openai.ErrInvalidAudio
			}
			byteRate = binary.LittleEndian.Uint32(data[start+8 : start+12])
		case "data":
			dataSize = size
		}
		offset = end + int(size&1)
	}
	if byteRate == 0 || dataSize == 0 {
		return 0, openai.ErrInvalidAudio
	}
	milliseconds := (uint64(dataSize)*1000 + uint64(byteRate) - 1) / uint64(byteRate)
	if milliseconds == 0 || milliseconds > math.MaxInt {
		return 0, openai.ErrInvalidAudio
	}
	return int(milliseconds), nil
}

func (p Mistral) TranscribeAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	if message := request.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, mistralAudioClientError(message)
	}
	if request.Prompt != "" || len(request.Include) > 0 || len(request.Languages) > 0 || request.ChunkingStrategy != nil || len(request.KnownSpeakerNames) > 0 || len(request.KnownSpeakerReferences) > 0 {
		return openai.AudioTranscriptionResponse{}, mistralAudioClientError("unsupported Mistral transcription parameter")
	}
	if _, err := p.ReserveAudioMilliseconds(request); err != nil {
		return openai.AudioTranscriptionResponse{}, mistralAudioClientError(err.Error())
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{"model": request.Model, "language": request.Language}
	if request.Temperature != nil {
		fields["temperature"] = strconv.FormatFloat(*request.Temperature, 'g', -1, 64)
	}
	if request.ResponseFormat == "diarized_json" {
		fields["diarize"] = "true"
	}
	for name, value := range fields {
		if value != "" {
			if err := writer.WriteField(name, value); err != nil {
				return openai.AudioTranscriptionResponse{}, err
			}
		}
	}
	for _, value := range request.Keywords {
		if err := writer.WriteField("context_bias", value); err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
	}
	for _, value := range request.TimestampGranularities {
		if err := writer.WriteField("timestamp_granularities", value); err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
	}
	if err := writeAudioPart(writer, request.File); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	if err := writer.Close(); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "audio/transcriptions"), &body)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", writer.FormDataContentType())
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.AudioTranscriptionResponse{}, responseStatusError("mistral", response)
	}
	return decodeMistralAudioTranscription(response.Body)
}

func mistralAudioClientError(message string) error {
	return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Err: errors.New(message)}
}

type mistralAudioTranscriptionResponse struct {
	Model    string `json:"model"`
	Text     string `json:"text"`
	Language string `json:"language"`
	Segments []struct {
		ID        any     `json:"id"`
		Type      string  `json:"type"`
		SpeakerID string  `json:"speaker_id"`
		Text      string  `json:"text"`
		Start     float64 `json:"start"`
		End       float64 `json:"end"`
	} `json:"segments"`
	Usage struct {
		PromptAudioSeconds float64 `json:"prompt_audio_seconds"`
		PromptTokens       int     `json:"prompt_tokens"`
		CompletionTokens   int     `json:"completion_tokens"`
		TotalTokens        int     `json:"total_tokens"`
	} `json:"usage"`
}

func decodeMistralAudioTranscription(reader io.Reader) (openai.AudioTranscriptionResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxAudioTranscriptionResponseBytes+1))
	if err != nil || len(payload) > maxAudioTranscriptionResponseBytes {
		return openai.AudioTranscriptionResponse{}, errors.New("audio transcription response exceeds limit")
	}
	var wire *mistralAudioTranscriptionResponse
	if err := json.Unmarshal(payload, &wire); err != nil || wire == nil {
		return openai.AudioTranscriptionResponse{}, errors.New("invalid Mistral transcription response")
	}
	inputTokens := wire.Usage.TotalTokens - wire.Usage.CompletionTokens
	audioTokens := inputTokens - wire.Usage.PromptTokens
	result := openai.AudioTranscriptionResponse{
		Text: wire.Text, Language: wire.Language, Duration: wire.Usage.PromptAudioSeconds,
		Usage: &openai.AudioTranscriptionUsage{Type: "tokens", InputTokens: inputTokens, OutputTokens: wire.Usage.CompletionTokens, TotalTokens: wire.Usage.TotalTokens, InputTokenDetails: &openai.AudioTranscriptionInputTokenDetails{TextTokens: wire.Usage.PromptTokens, AudioTokens: audioTokens}},
	}
	for index, segment := range wire.Segments {
		id := segment.ID
		if id == nil {
			id = index
		}
		result.Segments = append(result.Segments, openai.AudioTranscriptionSegment{ID: id, Type: segment.Type, Speaker: segment.SpeakerID, Text: segment.Text, Start: segment.Start, End: segment.End})
	}
	if message := result.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, errors.New(message)
	}
	return result, nil
}
