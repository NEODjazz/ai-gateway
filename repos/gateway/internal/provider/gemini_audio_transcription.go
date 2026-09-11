package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

func (Gemini) SupportsAudioTranscription() bool { return true }
func (Gemini) SupportsAudioTranslation() bool   { return true }

func (g Gemini) TranscribeAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	if err := validateGeminiAudioTranscriptionRequest(request); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	languages := append([]string(nil), request.Languages...)
	if len(languages) == 0 && request.Language != "" {
		languages = []string{request.Language}
	}
	prompt := "Transcribe the speech verbatim. Return only the transcript text."
	if request.Prompt != "" {
		prompt += " Context and spelling guidance: " + request.Prompt
	}
	body := geminiRequest{
		Contents:   []geminiContent{{Parts: []geminiPart{{Text: prompt}, {InlineData: &geminiInlineData{MIMEType: request.File.MediaType, Data: request.File.Data}}}}},
		Generation: geminiGeneration{Temperature: request.Temperature, ResponseMIMEType: "text/plain", AudioTranscription: &geminiAudioTranscriptionConfig{LanguageCodes: languages, CustomVocabulary: append([]string(nil), request.Keywords...)}},
	}
	return g.generateAudioText(ctx, request, body)
}

func (g Gemini) TranslateAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	if err := validateGeminiAudioTranslationRequest(request); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	prompt := "Translate all speech in the audio into English. Return only the translated text."
	if request.Prompt != "" {
		prompt += " Context and spelling guidance: " + request.Prompt
	}
	body := geminiRequest{
		Contents:   []geminiContent{{Parts: []geminiPart{{Text: prompt}, {InlineData: &geminiInlineData{MIMEType: request.File.MediaType, Data: request.File.Data}}}}},
		Generation: geminiGeneration{Temperature: request.Temperature, ResponseMIMEType: "text/plain"},
	}
	return g.generateAudioText(ctx, request, body)
}

func (g Gemini) generateAudioText(ctx context.Context, request openai.AudioTranscriptionRequest, body geminiRequest) (openai.AudioTranscriptionResponse, error) {
	model := strings.TrimPrefix(request.Model, "models/")
	if model == "" || strings.ContainsAny(model, "/\\?#%") || model == "." || model == ".." {
		return openai.AudioTranscriptionResponse{}, geminiInvalid("model")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	base, err := url.Parse(g.baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return openai.AudioTranscriptionResponse{}, errors.New("invalid Gemini base URL")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiBaseURL(g.baseURL)+"/models/"+url.PathEscape(model)+":generateContent", bytes.NewReader(encoded))
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if err := g.authorize(httpRequest); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	response, err := g.client.Do(httpRequest)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.AudioTranscriptionResponse{}, responseStatusError("gemini", response)
	}
	return decodeGeminiAudioTextResponse(response.Body)
}

func validateGeminiAudioTranslationRequest(request openai.AudioTranscriptionRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "gemini", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if !oneOfOrEmptyImageValue(request.File.MediaType, "audio/wav", "audio/mpeg", "audio/mp3", "audio/ogg", "audio/flac", "audio/webm") {
		return geminiInvalid("file")
	}
	return rejectParameters("gemini",
		parameterCheck{"language", request.Language != "" && request.Language != "en"},
		parameterCheck{"response_format", request.ResponseFormat != "" && request.ResponseFormat != "json"},
		parameterCheck{"timestamp_granularities", len(request.TimestampGranularities) > 0},
		parameterCheck{"include", len(request.Include) > 0},
		parameterCheck{"languages", len(request.Languages) > 0},
		parameterCheck{"keywords", len(request.Keywords) > 0},
		parameterCheck{"chunking_strategy", request.ChunkingStrategy != nil},
		parameterCheck{"known_speaker_names", len(request.KnownSpeakerNames) > 0},
		parameterCheck{"known_speaker_references", len(request.KnownSpeakerReferences) > 0},
	)
}

func validateGeminiAudioTranscriptionRequest(request openai.AudioTranscriptionRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "gemini", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if !oneOfOrEmptyImageValue(request.File.MediaType, "audio/wav", "audio/mpeg", "audio/mp3", "audio/ogg", "audio/flac", "audio/webm") {
		return geminiInvalid("file")
	}
	return rejectParameters("gemini",
		parameterCheck{"response_format", request.ResponseFormat != "" && request.ResponseFormat != "json"},
		parameterCheck{"timestamp_granularities", len(request.TimestampGranularities) > 0},
		parameterCheck{"include", len(request.Include) > 0},
		parameterCheck{"chunking_strategy", request.ChunkingStrategy != nil},
		parameterCheck{"known_speaker_names", len(request.KnownSpeakerNames) > 0},
		parameterCheck{"known_speaker_references", len(request.KnownSpeakerReferences) > 0},
	)
}

func decodeGeminiAudioTextResponse(reader io.Reader) (openai.AudioTranscriptionResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxAudioTranscriptionResponseBytes+1))
	if err != nil || len(payload) > maxAudioTranscriptionResponseBytes {
		return openai.AudioTranscriptionResponse{}, errors.New("Gemini audio response exceeds limit")
	}
	var upstream geminiResponse
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if decoder.Decode(&upstream) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(upstream.Candidates) != 1 || upstream.Usage == nil {
		return openai.AudioTranscriptionResponse{}, errors.New("Gemini returned invalid audio response")
	}
	var textParts []string
	for _, part := range upstream.Candidates[0].Content.Parts {
		if part.Thought {
			continue
		}
		if part.Text == "" || part.InlineData != nil || part.FunctionCall != nil || part.FunctionResponse != nil {
			return openai.AudioTranscriptionResponse{}, errors.New("Gemini returned invalid audio content")
		}
		textParts = append(textParts, part.Text)
	}
	usage := upstream.Usage
	if usage.Prompt < 0 || usage.Total <= 0 || usage.Total < usage.Prompt || usage.Candidates < 0 || usage.Thoughts < 0 || usage.Candidates > int(^uint(0)>>1)-usage.Thoughts || usage.Total < usage.Prompt+usage.Candidates+usage.Thoughts {
		return openai.AudioTranscriptionResponse{}, errors.New("Gemini returned inconsistent audio usage")
	}
	result := openai.AudioTranscriptionResponse{Text: strings.TrimSpace(strings.Join(textParts, "")), Usage: &openai.AudioTranscriptionUsage{Type: "tokens", InputTokens: usage.Prompt, OutputTokens: usage.Total - usage.Prompt, TotalTokens: usage.Total}}
	if message := result.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, errors.New(message)
	}
	return result, nil
}
