package provider

import (
	"context"

	"ai-gateway-gateway/internal/openai"
)

func (Lemonade) SupportsImageGeneration() bool    { return true }
func (Lemonade) SupportsImageEdit() bool          { return true }
func (Lemonade) SupportsImageVariation() bool     { return true }
func (Lemonade) SupportsAudioTranscription() bool { return true }
func (Lemonade) SupportsAudioSpeech() bool        { return true }
func (Lemonade) UsesImageUnitUsage() bool         { return true }

func lemonadeImageFormat(format string) error {
	if format != "" && format != "b64_json" {
		return rejectParameters("lemonade", parameterCheck{"response_format", true})
	}
	return nil
}

func (p Lemonade) ValidateImageGenerationParameters(request openai.ImageGenerationRequest) error {
	if err := lemonadeRequestFields(request, "model", "prompt", "size", "n", "response_format", "seed"); err != nil {
		return err
	}
	if request.N != nil && *request.N != 1 {
		return lemonadeInvalidParameter("n", "Lemonade image generation supports n=1")
	}
	if err := lemonadeImageFormat(request.ResponseFormat); err != nil {
		return err
	}
	return p.compatible.ValidateImageGenerationParameters(request)
}

func (p Lemonade) GenerateImage(ctx context.Context, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	if err := p.ValidateImageGenerationParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	request.ResponseFormat = "b64_json"
	return p.compatible.GenerateImage(ctx, request)
}

func (p Lemonade) ValidateImageEditParameters(request openai.ImageEditRequest) error {
	// Attachments have already passed the public file boundary. Avoid copying
	// their binary payloads merely to check optional control fields.
	controls := request
	controls.Images, controls.Mask = nil, nil
	if err := lemonadeRequestFields(controls, "model", "prompt", "images", "size", "n", "response_format"); err != nil {
		return err
	}
	if len(request.Images) != 1 {
		return lemonadeInvalidParameter("images", "Lemonade image editing requires exactly one image")
	}
	if err := lemonadeImageFormat(request.ResponseFormat); err != nil {
		return err
	}
	return p.compatible.ValidateImageEditParameters(request)
}

func (p Lemonade) EditImage(ctx context.Context, request openai.ImageEditRequest) (openai.ImageGenerationResponse, error) {
	if err := p.ValidateImageEditParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	request.ResponseFormat = "b64_json"
	return p.compatible.EditImage(ctx, request)
}

func (p Lemonade) ValidateImageVariationParameters(request openai.ImageVariationRequest) error {
	controls := request
	controls.Image = openai.ImageAttachment{}
	if err := lemonadeRequestFields(controls, "model", "image", "size", "n", "response_format"); err != nil {
		return err
	}
	if err := lemonadeImageFormat(request.ResponseFormat); err != nil {
		return err
	}
	return p.compatible.ValidateImageVariationParameters(request)
}

func (p Lemonade) CreateImageVariation(ctx context.Context, request openai.ImageVariationRequest) (openai.ImageGenerationResponse, error) {
	if err := p.ValidateImageVariationParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	request.ResponseFormat = "b64_json"
	return p.compatible.CreateImageVariation(ctx, request)
}

func (p Lemonade) ValidateAudioTranscriptionParameters(request openai.AudioTranscriptionRequest) error {
	controls := request
	controls.File = openai.AudioAttachment{}
	if err := lemonadeRequestFields(controls, "model", "file", "language", "response_format"); err != nil {
		return err
	}
	if request.File.MediaType != "audio/wav" && request.File.MediaType != "audio/x-wav" && request.File.MediaType != "audio/wave" {
		return lemonadeInvalidParameter("file", "Lemonade transcription requires WAV audio")
	}
	if request.ResponseFormat == "diarized_json" {
		return rejectParameters("lemonade", parameterCheck{"response_format", true})
	}
	return p.compatible.ValidateAudioTranscriptionParameters(request)
}

func (p Lemonade) TranscribeAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	if err := p.ValidateAudioTranscriptionParameters(request); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	if _, err := p.ReserveAudioMilliseconds(request); err != nil {
		return openai.AudioTranscriptionResponse{}, lemonadeInvalidParameter("file", "Lemonade transcription requires WAV with a valid duration")
	}
	return p.compatible.TranscribeAudio(ctx, request)
}

func (p Lemonade) ValidateAudioSpeechParameters(request openai.AudioSpeechRequest) error {
	if err := lemonadeRequestFields(request, "model", "input", "voice", "speed", "response_format"); err != nil {
		return err
	}
	// Native PCM rate/channel headers differ between Lemonade backends. The
	// public speech contract cannot safely label those bytes as fixed-rate PCM.
	if request.ResponseFormat != "" && request.ResponseFormat != "wav" && request.ResponseFormat != "mp3" && request.ResponseFormat != "opus" {
		return rejectParameters("lemonade", parameterCheck{"response_format", true})
	}
	return p.compatible.ValidateAudioSpeechParameters(request)
}

func (p Lemonade) GenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error) {
	if err := p.ValidateAudioSpeechParameters(request); err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	return p.compatible.GenerateSpeech(ctx, request)
}
