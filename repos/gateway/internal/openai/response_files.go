package openai

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	MaxResponseFileAttachments = 5
	MaxResponseFileBytes       = 16 << 20
)

var ErrInvalidFileInput = errors.New("invalid file input")

type ResponseFileAttachment struct {
	Filename  string
	MediaType string
	Data      string
}

func ResponseFileAttachments(input any) ([]ResponseFileAttachment, error) {
	attachments := make([]ResponseFileAttachment, 0)
	total := 0
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
			if typeName == "input_file" {
				attachment, data, err := parseResponseFile(typed)
				if err != nil || len(attachments) >= MaxResponseFileAttachments || total > MaxResponseFileBytes-len(data) {
					return ErrInvalidFileInput
				}
				total += len(data)
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

func parseResponseFile(value map[string]any) (ResponseFileAttachment, []byte, error) {
	if len(value) != 3 {
		return ResponseFileAttachment{}, nil, ErrInvalidFileInput
	}
	fileData, dataOK := value["file_data"].(string)
	filename, filenameOK := value["filename"].(string)
	if !dataOK || !filenameOK || filename == "" || len(filename) > 256 || filepath.Base(filename) != filename || strings.ContainsAny(filename, "\x00/\\") || !strings.HasSuffix(strings.ToLower(filename), ".pdf") {
		return ResponseFileAttachment{}, nil, ErrInvalidFileInput
	}
	const prefix = "data:application/pdf;base64,"
	if !strings.HasPrefix(fileData, prefix) {
		return ResponseFileAttachment{}, nil, ErrInvalidFileInput
	}
	encoded := strings.TrimPrefix(fileData, prefix)
	if encoded == "" || base64.StdEncoding.DecodedLen(len(encoded)) > MaxResponseFileBytes {
		return ResponseFileAttachment{}, nil, ErrInvalidFileInput
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) < 5 || len(data) > MaxResponseFileBytes || string(data[:5]) != "%PDF-" {
		return ResponseFileAttachment{}, nil, ErrInvalidFileInput
	}
	return ResponseFileAttachment{Filename: filename, MediaType: "application/pdf", Data: encoded}, data, nil
}

func HasResponseFiles(request ResponseRequest) bool {
	attachments, err := ResponseFileAttachments(request.Input)
	return err == nil && len(attachments) > 0
}

func ChatFileAttachments(messages []Message) ([]ResponseFileAttachment, error) {
	attachments := make([]ResponseFileAttachment, 0)
	total := 0
	for _, message := range messages {
		found, err := ResponseFileAttachments(message.Content)
		if err != nil {
			return nil, err
		}
		if len(found) > 0 && message.Role != "user" {
			return nil, fmt.Errorf("%w: files are only accepted in user messages", ErrInvalidFileInput)
		}
		if len(attachments) > MaxResponseFileAttachments-len(found) {
			return nil, ErrInvalidFileInput
		}
		for _, attachment := range found {
			data, err := base64.StdEncoding.DecodeString(attachment.Data)
			if err != nil || total > MaxResponseFileBytes-len(data) {
				return nil, ErrInvalidFileInput
			}
			total += len(data)
		}
		attachments = append(attachments, found...)
	}
	return attachments, nil
}

func HasChatFileInput(request ChatCompletionRequest) bool {
	attachments, err := ChatFileAttachments(request.Messages)
	return err == nil && len(attachments) > 0
}
