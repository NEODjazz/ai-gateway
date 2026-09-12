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

func (OpenAICompatible) SupportsAudioSpeech() bool            { return true }
func (p OpenAICompatible) SupportsAudioSpeechStreaming() bool { return p.upstreamStream }

func (p OpenAICompatible) ValidateAudioSpeechParameters(request openai.AudioSpeechRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: p.providerName(), StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	return rejectParameters(p.providerName(), parameterCheck{"language", request.Language != ""})
}

func (p OpenAICompatible) GenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error) {
	if err := p.ValidateAudioSpeechParameters(request); err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	if request.StreamFormat == "sse" {
		return openai.AudioSpeechResponse{}, ErrStreamingUnsupported
	}
	payload, err := audioSpeechRequestPayload(request)
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	return p.sendBufferedSpeech(ctx, request, payload)
}

func audioSpeechRequestPayload(request openai.AudioSpeechRequest) ([]byte, error) {
	payload, err := json.Marshal(struct {
		Model          string   `json:"model"`
		Input          string   `json:"input"`
		Voice          string   `json:"voice"`
		Instructions   string   `json:"instructions,omitempty"`
		ResponseFormat string   `json:"response_format,omitempty"`
		Speed          *float64 `json:"speed,omitempty"`
		StreamFormat   string   `json:"stream_format,omitempty"`
	}{request.Model, request.Input, request.Voice, request.Instructions, request.ResponseFormat, request.Speed, request.StreamFormat})
	return payload, err
}

func (p OpenAICompatible) sendBufferedSpeech(ctx context.Context, request openai.AudioSpeechRequest, payload []byte) (openai.AudioSpeechResponse, error) {
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

func (p OpenAICompatible) StreamGenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest, write AudioSpeechStreamWriter) (openai.AudioSpeechResponse, error) {
	if err := p.ValidateAudioSpeechParameters(request); err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	if !p.upstreamStream {
		return openai.AudioSpeechResponse{}, ErrStreamingUnsupported
	}
	request.StreamFormat = "sse"
	payload, err := audioSpeechRequestPayload(request)
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
	if mediaType := response.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(mediaType), "text/event-stream") {
		return openai.AudioSpeechResponse{}, errors.New("provider returned a non-SSE audio speech stream")
	}
	usage, err := streamAudioSpeech(&responseStreamReader{source: response.Body, remaining: maxAudioSpeechResponseBytes * 2}, write)
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	return openai.AudioSpeechResponse{ContentType: request.ExpectedContentType(), Model: request.Model, Usage: &usage}, nil
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
