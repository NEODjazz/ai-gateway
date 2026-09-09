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
	"net/textproto"
	"path/filepath"
	"strconv"

	"ai-gateway-gateway/internal/openai"
)

const maxAudioTranscriptionResponseBytes = 8 << 20

func (OpenAICompatible) SupportsAudioTranscription() bool { return true }

func (p OpenAICompatible) TranscribeAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	if message := request.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, &Error{Class: FailureClientRequest, Provider: p.providerName(), StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{"model": request.Model, "language": request.Language, "prompt": request.Prompt, "response_format": request.ResponseFormat}
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
	for _, value := range request.TimestampGranularities {
		if err := writer.WriteField("timestamp_granularities[]", value); err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
	}
	for _, value := range request.Include {
		if err := writer.WriteField("include[]", value); err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
	}
	for _, value := range request.Languages {
		if err := writer.WriteField("languages[]", value); err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
	}
	for _, value := range request.Keywords {
		if err := writer.WriteField("keywords[]", value); err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
	}
	for _, value := range request.KnownSpeakerNames {
		if err := writer.WriteField("known_speaker_names[]", value); err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
	}
	for _, reference := range request.KnownSpeakerReferences {
		if err := writer.WriteField("known_speaker_references[]", reference.DataURL()); err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
	}
	if request.ChunkingStrategy != nil {
		value, err := request.ChunkingStrategy.MultipartValue()
		if err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
		if err := writer.WriteField("chunking_strategy", value); err != nil {
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
		return openai.AudioTranscriptionResponse{}, responseStatusError(p.providerName(), response)
	}
	return decodeAudioTranscriptionResponse(response.Body)
}

func writeAudioPart(writer *multipart.Writer, attachment openai.AudioAttachment) error {
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+filepath.Base(attachment.Filename)+`"`)
	header.Set("Content-Type", attachment.MediaType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	data, err := base64.StdEncoding.DecodeString(attachment.Data)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

func decodeAudioTranscriptionResponse(reader io.Reader) (openai.AudioTranscriptionResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxAudioTranscriptionResponseBytes+1))
	if err != nil || len(payload) > maxAudioTranscriptionResponseBytes {
		return openai.AudioTranscriptionResponse{}, errors.New("audio transcription response exceeds limit")
	}
	var response *openai.AudioTranscriptionResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	if response == nil {
		return openai.AudioTranscriptionResponse{}, errors.New("audio transcription response must be an object")
	}
	if message := response.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, errors.New(message)
	}
	return *response, nil
}
