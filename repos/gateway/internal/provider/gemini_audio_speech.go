package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const maxGeminiAudioSpeechResponseBytes = 45 << 20

type geminiSpeechRequest struct {
	Model            string                     `json:"model"`
	Input            string                     `json:"input"`
	ResponseFormat   geminiSpeechResponseFormat `json:"response_format"`
	GenerationConfig geminiSpeechGeneration     `json:"generation_config"`
}

type geminiSpeechResponseFormat struct {
	Type     string `json:"type"`
	MIMEType string `json:"mime_type"`
	Delivery string `json:"delivery"`
}

type geminiSpeechGeneration struct {
	SpeechConfig []geminiSpeakerConfig `json:"speech_config"`
}

type geminiSpeakerConfig struct {
	Voice string `json:"voice"`
}

type geminiSpeechResponse struct {
	Status string `json:"status"`
	Steps  []struct {
		Type    string `json:"type"`
		Content []struct {
			Type     string `json:"type"`
			Data     string `json:"data"`
			MIMEType string `json:"mime_type"`
			URI      string `json:"uri"`
		} `json:"content"`
	} `json:"steps"`
}

func (Gemini) SupportsAudioSpeech() bool { return true }

func (g Gemini) GenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error) {
	if message := request.Validate(); message != "" {
		return openai.AudioSpeechResponse{}, &Error{Class: FailureClientRequest, Provider: "gemini", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if err := rejectParameters("gemini",
		parameterCheck{"speed", request.Speed != nil},
		parameterCheck{"response_format", request.ResponseFormat == "aac" || request.ResponseFormat == "flac"},
	); err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	model := strings.TrimPrefix(request.Model, "models/")
	if model == "" || strings.ContainsAny(model, "/\\?#%") || model == "." || model == ".." {
		return openai.AudioSpeechResponse{}, geminiInvalid("model")
	}
	format := request.ResponseFormat
	if format == "" {
		format = "mp3"
	}
	mimeType := map[string]string{"mp3": "audio/mp3", "opus": "audio/ogg_opus", "wav": "audio/wav", "pcm": "audio/l16"}[format]
	input := request.Input
	if request.Instructions != "" {
		input = request.Instructions + "\n\nRead exactly: " + request.Input
	}
	payload, err := json.Marshal(geminiSpeechRequest{
		Model: model,
		Input: input,
		ResponseFormat: geminiSpeechResponseFormat{
			Type: "audio", MIMEType: mimeType, Delivery: "inline",
		},
		GenerationConfig: geminiSpeechGeneration{SpeechConfig: []geminiSpeakerConfig{{Voice: request.Voice}}},
	})
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	base, err := url.Parse(g.baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return openai.AudioSpeechResponse{}, errors.New("invalid Gemini base URL")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiBaseURL(g.baseURL)+"/interactions", bytes.NewReader(payload))
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if err := g.authorize(httpRequest); err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	response, err := g.client.Do(httpRequest)
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.AudioSpeechResponse{}, responseStatusError("gemini", response)
	}
	return decodeGeminiAudioSpeechResponse(response.Body, request)
}

func decodeGeminiAudioSpeechResponse(reader io.Reader, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxGeminiAudioSpeechResponseBytes+1))
	if err != nil || len(payload) > maxGeminiAudioSpeechResponseBytes {
		return openai.AudioSpeechResponse{}, errors.New("Gemini audio speech response exceeds limit")
	}
	var upstream geminiSpeechResponse
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if decoder.Decode(&upstream) != nil || decoder.Decode(&struct{}{}) != io.EOF || upstream.Status != "completed" {
		return openai.AudioSpeechResponse{}, errors.New("Gemini returned invalid audio speech response")
	}
	var encoded, mediaType string
	for _, step := range upstream.Steps {
		if step.Type != "model_output" {
			continue
		}
		if len(step.Content) != 1 || step.Content[0].Type != "audio" || step.Content[0].Data == "" || step.Content[0].URI != "" || encoded != "" {
			return openai.AudioSpeechResponse{}, errors.New("Gemini returned invalid audio speech content")
		}
		encoded, mediaType = step.Content[0].Data, step.Content[0].MIMEType
	}
	if encoded == "" || !geminiSpeechMIMETypeMatches(request.ResponseFormat, mediaType) {
		return openai.AudioSpeechResponse{}, errors.New("Gemini returned unexpected audio speech format")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > maxAudioSpeechResponseBytes {
		return openai.AudioSpeechResponse{}, errors.New("Gemini returned invalid audio speech data")
	}
	return openai.AudioSpeechResponse{Data: data, ContentType: request.ExpectedContentType(), Model: request.Model}, nil
}

func geminiSpeechMIMETypeMatches(format, mediaType string) bool {
	if format == "" {
		format = "mp3"
	}
	switch format {
	case "mp3":
		return mediaType == "audio/mp3" || mediaType == "audio/mpeg"
	case "opus":
		return mediaType == "audio/ogg" || mediaType == "audio/opus" || mediaType == "audio/ogg_opus"
	case "wav":
		return mediaType == "audio/wav"
	case "pcm":
		return mediaType == "audio/l16"
	default:
		return false
	}
}
