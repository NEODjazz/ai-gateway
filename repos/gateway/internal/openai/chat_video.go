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
				attachment := VideoAttachment{Data: data}
				switch format {
				case "mp4":
					attachment.MediaType = "video/mp4"
				case "webm":
					attachment.MediaType = "video/webm"
				}
				decoded, err := base64.StdEncoding.DecodeString(data)
				if !dataOK || !formatOK || err != nil || len(decoded) == 0 || len(decoded) > MaxChatVideoBytes || !validVideoInputSignature(attachment.MediaType, decoded) {
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
	default:
		return false
	}
}

func HasChatVideoInput(request ChatCompletionRequest) bool {
	attachments, err := ChatVideoAttachments(request.Messages)
	return err == nil && len(attachments) > 0
}
