package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const maxAudioSpeechResponseBytes = 32 << 20

func (OpenAICompatible) SupportsAudioSpeech() bool { return true }

func (p OpenAICompatible) GenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error) {
	if message := request.Validate(); message != "" {
		return openai.AudioSpeechResponse{}, &Error{Class: FailureClientRequest, Provider: p.providerName(), StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	payload, err := json.Marshal(struct {
		Model          string   `json:"model"`
		Input          string   `json:"input"`
		Voice          string   `json:"voice"`
		Instructions   string   `json:"instructions,omitempty"`
		ResponseFormat string   `json:"response_format,omitempty"`
		Speed          *float64 `json:"speed,omitempty"`
		StreamFormat   string   `json:"stream_format,omitempty"`
	}{request.Model, request.Input, request.Voice, request.Instructions, request.ResponseFormat, request.Speed, request.StreamFormat})
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "audio/speech"), bytes.NewReader(payload))
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.AudioSpeechResponse{}, responseStatusError(p.providerName(), response)
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	mediaType, _, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil || (!strings.HasPrefix(mediaType, "audio/") && mediaType != "application/octet-stream") {
		return openai.AudioSpeechResponse{}, errors.New("audio speech response has an unsupported content type")
	}
	data, err := readAudioSpeechResponse(response.Body)
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	return openai.AudioSpeechResponse{Data: data, ContentType: mediaType, Model: request.Model}, nil
}

func readAudioSpeechResponse(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxAudioSpeechResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("audio speech response is empty")
	}
	if len(data) > maxAudioSpeechResponseBytes {
		return nil, errors.New("audio speech response exceeds limit")
	}
	return data, nil
}
