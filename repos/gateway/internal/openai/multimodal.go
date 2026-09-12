package openai

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	MaxImageAttachments   = 8
	MaxImageBytes         = 8 << 20
	MaxTotalImageBytes    = 16 << 20
	MaxInferenceBodyBytes = 24 << 20
)

var ErrInvalidImage = errors.New("invalid image input")

const MaxResponseAudioAttachments = 8

type ImageAttachment struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data_base64"`
}

func ChatImageAttachments(messages []Message) ([]ImageAttachment, error) {
	var attachments []ImageAttachment
	for _, message := range messages {
		found, err := imageAttachments(message.Content)
		if err != nil {
			return nil, err
		}
		if len(found) > 0 && message.Role != "user" {
			return nil, fmt.Errorf("%w: images are only accepted in user messages", ErrInvalidImage)
		}
		attachments = append(attachments, found...)
	}
	return validateAttachmentLimits(attachments)
}

func ResponseImageAttachments(input any) ([]ImageAttachment, error) {
	attachments, err := imageAttachments(input)
	if err != nil {
		return nil, err
	}
	return validateAttachmentLimits(attachments)
}

func ResponseAudioAttachments(input any) ([]AudioAttachment, error) {
	var attachments []AudioAttachment
	var total int
	var walk func(any) error
	walk = func(value any) error {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				if err := walk(item); err != nil {
					return err
				}
			}
		case map[string]any:
			typeName, _ := typed["type"].(string)
			if typeName == "input_audio" {
				audio, ok := typed["input_audio"].(map[string]any)
				if !ok {
					return ErrInvalidAudio
				}
				data, dataOK := audio["data"].(string)
				format, formatOK := audio["format"].(string)
				mediaType, filename := "", ""
				switch format {
				case "wav":
					mediaType, filename = "audio/wav", "input.wav"
				case "mp3":
					mediaType, filename = "audio/mpeg", "input.mp3"
				}
				attachment := AudioAttachment{Filename: filename, MediaType: mediaType, Data: data}
				if !dataOK || !formatOK || ValidateAudioAttachment(attachment) != nil || len(attachments) >= MaxResponseAudioAttachments {
					return ErrInvalidAudio
				}
				decoded, _ := base64.StdEncoding.DecodeString(data)
				if total > MaxAudioBytes-len(decoded) {
					return ErrInvalidAudio
				}
				total += len(decoded)
				attachments = append(attachments, attachment)
				return nil
			}
			for _, nested := range typed {
				if err := walk(nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(input); err != nil {
		return nil, err
	}
	return attachments, nil
}

func ChatAudioAttachments(messages []Message) ([]AudioAttachment, error) {
	var attachments []AudioAttachment
	for _, message := range messages {
		found, err := ResponseAudioAttachments(message.Content)
		if err != nil {
			return nil, err
		}
		if len(found) > 0 && message.Role != "user" {
			return nil, fmt.Errorf("%w: audio is only accepted in user messages", ErrInvalidAudio)
		}
		if len(attachments) > MaxResponseAudioAttachments-len(found) {
			return nil, ErrInvalidAudio
		}
		attachments = append(attachments, found...)
	}
	return attachments, nil
}

func HasChatAudioInput(request ChatCompletionRequest) bool {
	attachments, err := ChatAudioAttachments(request.Messages)
	return err == nil && len(attachments) > 0
}

func HasResponseAudio(request ResponseRequest) bool {
	attachments, err := ResponseAudioAttachments(request.Input)
	return err == nil && len(attachments) > 0
}

func imageAttachments(value any) ([]ImageAttachment, error) {
	var attachments []ImageAttachment
	var walk func(any) error
	walk = func(value any) error {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				if err := walk(item); err != nil {
					return err
				}
			}
		case map[string]any:
			typeName, _ := typed["type"].(string)
			switch typeName {
			case "image_url":
				image, ok := typed["image_url"].(map[string]any)
				if !ok {
					return fmt.Errorf("%w: image_url must be an object", ErrInvalidImage)
				}
				value, ok := image["url"].(string)
				if !ok {
					return fmt.Errorf("%w: image_url.url is required", ErrInvalidImage)
				}
				attachment, err := ParseDataImageURL(value)
				if err != nil {
					return err
				}
				attachments = append(attachments, attachment)
				return nil
			case "input_image":
				value, ok := typed["image_url"].(string)
				if !ok {
					return fmt.Errorf("%w: input_image.image_url is required", ErrInvalidImage)
				}
				attachment, err := ParseDataImageURL(value)
				if err != nil {
					return err
				}
				attachments = append(attachments, attachment)
				return nil
			}
			for _, nested := range typed {
				if err := walk(nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(value); err != nil {
		return nil, err
	}
	return attachments, nil
}

func ParseDataImageURL(value string) (ImageAttachment, error) {
	header, data, found := strings.Cut(value, ",")
	if !found || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return ImageAttachment{}, fmt.Errorf("%w: only base64 data image URLs are supported", ErrInvalidImage)
	}
	mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return ImageAttachment{}, fmt.Errorf("%w: unsupported media type %q", ErrInvalidImage, mediaType)
	}
	if data == "" || base64.StdEncoding.DecodedLen(len(data)) > MaxImageBytes {
		return ImageAttachment{}, fmt.Errorf("%w: image exceeds %d bytes", ErrInvalidImage, MaxImageBytes)
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(decoded) == 0 {
		return ImageAttachment{}, fmt.Errorf("%w: malformed base64 image", ErrInvalidImage)
	}
	if !validImageSignature(mediaType, decoded) {
		return ImageAttachment{}, fmt.Errorf("%w: image bytes do not match media type %q", ErrInvalidImage, mediaType)
	}
	return ImageAttachment{MediaType: mediaType, Data: data}, nil
}

func ValidateImageAttachments(attachments []ImageAttachment) error {
	validated := make([]ImageAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		parsed, err := ParseDataImageURL("data:" + attachment.MediaType + ";base64," + attachment.Data)
		if err != nil {
			return err
		}
		validated = append(validated, parsed)
	}
	_, err := validateAttachmentLimits(validated)
	return err
}

func validImageSignature(mediaType string, data []byte) bool {
	switch mediaType {
	case "image/jpeg":
		return len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
	case "image/png":
		return len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n"
	case "image/gif":
		return len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a")
	case "image/webp":
		return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
	default:
		return false
	}
}

func validateAttachmentLimits(attachments []ImageAttachment) ([]ImageAttachment, error) {
	if len(attachments) > MaxImageAttachments {
		return nil, fmt.Errorf("%w: at most %d images are allowed", ErrInvalidImage, MaxImageAttachments)
	}
	total := 0
	for _, attachment := range attachments {
		decoded, err := base64.StdEncoding.DecodeString(attachment.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed base64 image", ErrInvalidImage)
		}
		total += len(decoded)
		if total > MaxTotalImageBytes {
			return nil, fmt.Errorf("%w: images exceed %d bytes in total", ErrInvalidImage, MaxTotalImageBytes)
		}
	}
	return attachments, nil
}

func HasChatImages(request ChatCompletionRequest) bool {
	attachments, err := ChatImageAttachments(request.Messages)
	return err == nil && len(attachments) > 0
}

func HasResponseImages(request ResponseRequest) bool {
	attachments, err := ResponseImageAttachments(request.Input)
	return err == nil && len(attachments) > 0
}

func IsMediaContent(value map[string]any) bool {
	typeName, _ := value["type"].(string)
	switch typeName {
	case "image_url", "input_image", "image", "input_audio", "audio", "input_file", "file":
		return true
	default:
		return false
	}
}

func TransformTextContent(value any, transform func(string) string) any {
	switch typed := value.(type) {
	case string:
		return transform(typed)
	case []any:
		for index := range typed {
			typed[index] = TransformTextContent(typed[index], transform)
		}
		return typed
	case map[string]any:
		if IsMediaContent(typed) {
			return typed
		}
		for key, nested := range typed {
			if key == "type" || key == "role" || key == "name" || key == "id" || key == "phase" || key == "encrypted_content" || key == "call_id" || key == "tool_call_id" || key == "prompt_cache_breakpoint" {
				continue
			}
			typed[key] = TransformTextContent(nested, transform)
		}
		return typed
	default:
		return value
	}
}

func TextOnlyProjection(value any) any {
	switch typed := value.(type) {
	case []any:
		projected := make([]any, len(typed))
		for index, item := range typed {
			projected[index] = TextOnlyProjection(item)
		}
		return projected
	case map[string]any:
		if IsMediaContent(typed) {
			return map[string]any{"type": typed["type"]}
		}
		projected := make(map[string]any, len(typed))
		for key, nested := range typed {
			if key == "phase" || key == "encrypted_content" || key == "call_id" || key == "tool_call_id" {
				continue
			}
			projected[key] = TextOnlyProjection(nested)
		}
		return projected
	default:
		return value
	}
}

func MergeTextProjection(original any, transformed any) any {
	switch typed := original.(type) {
	case string:
		if value, ok := transformed.(string); ok {
			return value
		}
		return original
	case []any:
		other, ok := transformed.([]any)
		if !ok || len(other) != len(typed) {
			return original
		}
		merged := make([]any, len(typed))
		for index := range typed {
			merged[index] = MergeTextProjection(typed[index], other[index])
		}
		return merged
	case map[string]any:
		if IsMediaContent(typed) {
			return original
		}
		other, ok := transformed.(map[string]any)
		if !ok {
			return original
		}
		merged := make(map[string]any, len(typed))
		for key, value := range typed {
			if key == "type" || key == "role" || key == "name" || key == "id" || key == "phase" || key == "encrypted_content" || key == "call_id" || key == "tool_call_id" {
				merged[key] = value
				continue
			}
			merged[key] = MergeTextProjection(value, other[key])
		}
		return merged
	default:
		return original
	}
}
