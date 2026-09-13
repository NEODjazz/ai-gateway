package openai

import (
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	MaxChatVideoAttachments = 4
	MaxChatVideoBytes       = 16 << 20
)

var ErrInvalidVideoInput = errors.New("invalid video input")

type VideoAttachment struct {
	MediaType string
	Data      string
}

func ChatVideoAttachments(messages []Message) ([]VideoAttachment, error) {
	attachments := make([]VideoAttachment, 0)
	total := 0
	for _, message := range messages {
		found, err := videoAttachments(message.Content)
		if err != nil {
			return nil, err
		}
		if len(found) > 0 && message.Role != "user" {
			return nil, fmt.Errorf("%w: videos are only accepted in user messages", ErrInvalidVideoInput)
		}
		if len(attachments) > MaxChatVideoAttachments-len(found) {
			return nil, ErrInvalidVideoInput
		}
		for _, attachment := range found {
			data, err := base64.StdEncoding.DecodeString(attachment.Data)
			if err != nil || total > MaxChatVideoBytes-len(data) {
				return nil, ErrInvalidVideoInput
			}
			total += len(data)
		}
		attachments = append(attachments, found...)
	}
	return attachments, nil
}

func videoAttachments(value any) ([]VideoAttachment, error) {
	attachments := make([]VideoAttachment, 0)
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
			if typeName == "input_video" {
				if len(typed) != 2 {
					return ErrInvalidVideoInput
				}
				video, ok := typed["input_video"].(map[string]any)
				if !ok || len(video) != 2 {
					return ErrInvalidVideoInput
				}
				data, dataOK := video["data"].(string)
				format, formatOK := video["format"].(string)
				mediaType, known := videoInputMediaType(format)
				attachment := VideoAttachment{Data: data, MediaType: mediaType}
				decoded, err := base64.StdEncoding.DecodeString(data)
				if !dataOK || !formatOK || !known || err != nil || len(decoded) == 0 || len(decoded) > MaxChatVideoBytes || !validVideoInputSignature(attachment.MediaType, decoded) {
					return ErrInvalidVideoInput
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

func validVideoInputSignature(mediaType string, data []byte) bool {
	switch mediaType {
	case "video/mp4":
		return len(data) >= 12 && string(data[4:8]) == "ftyp"
	case "video/webm":
		return len(data) >= 4 && data[0] == 0x1a && data[1] == 0x45 && data[2] == 0xdf && data[3] == 0xa3
	case "video/mpeg", "video/mpg":
		return len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 1 && (data[3] == 0xba || data[3] == 0xb3)
	case "video/mov":
		return len(data) >= 12 && string(data[4:8]) == "ftyp" && string(data[8:12]) == "qt  "
	case "video/avi":
		return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "AVI "
	case "video/x-flv":
		return len(data) >= 5 && string(data[:3]) == "FLV" && data[3] == 1
	case "video/wmv":
		return len(data) >= 16 && string(data[:16]) == "\x30\x26\xb2\x75\x8e\x66\xcf\x11\xa6\xd9\x00\xaa\x00\x62\xce\x6c"
	case "video/3gpp":
		return len(data) >= 12 && string(data[4:8]) == "ftyp" && string(data[8:11]) == "3gp"
	default:
		return false
	}
}

func videoInputMediaType(format string) (string, bool) {
	mediaTypes := map[string]string{
		"mp4": "video/mp4", "webm": "video/webm", "mpeg": "video/mpeg",
		"mpg": "video/mpg", "mov": "video/mov", "avi": "video/avi",
		"flv": "video/x-flv", "wmv": "video/wmv", "3gpp": "video/3gpp",
	}
	mediaType, ok := mediaTypes[format]
	return mediaType, ok
}

// VideoInputFormat maps a supported native MIME type to the bounded internal
// video representation used by provider adapters.
func VideoInputFormat(mediaType string) (string, bool) {
	formats := map[string]string{
		"video/mp4": "mp4", "video/webm": "webm", "video/mpeg": "mpeg",
		"video/mpg": "mpg", "video/mov": "mov", "video/avi": "avi",
		"video/x-flv": "flv", "video/wmv": "wmv", "video/3gpp": "3gpp",
	}
	format, ok := formats[mediaType]
	return format, ok
}

func HasChatVideoInput(request ChatCompletionRequest) bool {
	attachments, err := ChatVideoAttachments(request.Messages)
	return err == nil && len(attachments) > 0
}
