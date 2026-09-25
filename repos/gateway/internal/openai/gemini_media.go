package openai

import "errors"

var ErrInvalidGeminiMediaResolution = errors.New("invalid Gemini media resolution")
var ErrInvalidGeminiMediaProcessing = errors.New("invalid Gemini media processing")

type GeminiMediaResolution struct {
	Level string `json:"level"`
}

func ValidGeminiMediaResolution(level string, perPart bool) bool {
	switch level {
	case "MEDIA_RESOLUTION_UNSPECIFIED", "MEDIA_RESOLUTION_LOW", "MEDIA_RESOLUTION_MEDIUM", "MEDIA_RESOLUTION_HIGH":
		return true
	case "MEDIA_RESOLUTION_ULTRA_HIGH":
		return perPart
	default:
		return false
	}
}

func GeminiPartMediaResolution(part map[string]any) (*GeminiMediaResolution, error) {
	value, found := part["gemini_media_resolution"]
	if !found {
		return nil, nil
	}
	level, ok := value.(string)
	if !ok || !ValidGeminiMediaResolution(level, true) {
		return nil, ErrInvalidGeminiMediaResolution
	}
	switch part["type"] {
	case "image_url", "input_audio", "input_video", "input_file":
		return &GeminiMediaResolution{Level: level}, nil
	default:
		return nil, ErrInvalidGeminiMediaResolution
	}
}

func HasChatGeminiPartMediaResolution(request ChatCompletionRequest) bool {
	for _, message := range request.Messages {
		parts, ok := message.Content.([]any)
		if !ok {
			continue
		}
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if ok {
				if _, found := part["gemini_media_resolution"]; found {
					return true
				}
			}
		}
	}
	return false
}

func ValidateChatGeminiPartMediaResolutions(request ChatCompletionRequest) error {
	for _, message := range request.Messages {
		parts, ok := message.Content.([]any)
		if !ok {
			continue
		}
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			resolution, err := GeminiPartMediaResolution(part)
			if err != nil || resolution != nil && message.Role != "user" {
				return ErrInvalidGeminiMediaResolution
			}
		}
	}
	return nil
}

func ValidGeminiMediaProcessing(value string) bool {
	switch value {
	case "MEDIA_PROCESSING_UNSPECIFIED", "STATIC", "AGENTIC":
		return true
	default:
		return false
	}
}

func GeminiPartMediaProcessing(part map[string]any) (string, error) {
	value, found := part["gemini_media_processing"]
	if !found {
		return "", nil
	}
	processing, ok := value.(string)
	if !ok || !ValidGeminiMediaProcessing(processing) || part["type"] != "input_video" {
		return "", ErrInvalidGeminiMediaProcessing
	}
	return processing, nil
}

func HasChatGeminiPartMediaProcessing(request ChatCompletionRequest) bool {
	for _, message := range request.Messages {
		parts, ok := message.Content.([]any)
		if !ok {
			continue
		}
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if ok {
				if _, found := part["gemini_media_processing"]; found {
					return true
				}
			}
		}
	}
	return false
}

func ValidateChatGeminiPartMediaProcessing(request ChatCompletionRequest) error {
	for _, message := range request.Messages {
		parts, ok := message.Content.([]any)
		if !ok {
			continue
		}
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			processing, err := GeminiPartMediaProcessing(part)
			if err != nil || processing != "" && message.Role != "user" {
				return ErrInvalidGeminiMediaProcessing
			}
		}
	}
	return nil
}
