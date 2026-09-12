package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var errMessagesFileUnavailable = errors.New("document file is unavailable")
var errMessagesFileStorageUnavailable = errors.New("document file storage is unavailable")

func (h Handler) resolveMessagesRequestFileReferences(ctx context.Context, identity modules.RequestContext, request *messagesRequest) error {
	owner := fileOwnerKey(identity)
	for messageIndex := range request.Messages {
		content := bytes.TrimSpace(request.Messages[messageIndex].Content)
		if len(content) == 0 || content[0] != '[' {
			continue
		}
		var blocks []map[string]any
		if json.Unmarshal(content, &blocks) != nil {
			return errMessagesFileUnavailable
		}
		changed := false
		for _, block := range blocks {
			if block["type"] != "document" {
				continue
			}
			source, _ := block["source"].(map[string]any)
			if source["type"] != "file" {
				continue
			}
			fileID, _ := source["file_id"].(string)
			file, err := h.ownedMessagesFile(ctx, owner, fileID)
			if err != nil {
				return err
			}
			switch file.ContentType {
			case "application/pdf":
				block["source"] = map[string]any{"type": "base64", "media_type": "application/pdf", "data": base64.StdEncoding.EncodeToString(file.Content)}
			case "text/plain":
				if !utf8.Valid(file.Content) {
					return errMessagesFileUnavailable
				}
				block["source"] = map[string]any{"type": "text", "media_type": "text/plain", "data": string(file.Content)}
			default:
				return errMessagesFileUnavailable
			}
			changed = true
		}
		if changed {
			encoded, err := json.Marshal(blocks)
			if err != nil {
				return errMessagesFileUnavailable
			}
			request.Messages[messageIndex].Content = encoded
		}
	}
	_, err := request.chat()
	return err
}

func (h Handler) ownedMessagesFile(ctx context.Context, owner, fileID string) (filestate.File, error) {
	if h.files == nil {
		return filestate.File{}, errMessagesFileStorageUnavailable
	}
	file, err := h.files.Get(ctx, owner, fileID, true)
	if errors.Is(err, filestate.ErrUnavailable) {
		return filestate.File{}, errMessagesFileStorageUnavailable
	}
	if err != nil || file.Purpose != "user_data" || int64(len(file.Content)) != file.Bytes {
		return filestate.File{}, errMessagesFileUnavailable
	}
	return file, nil
}

func (h Handler) resolveMessagesFileReferences(ctx context.Context, identity modules.RequestContext, request *openai.ChatCompletionRequest) error {
	if !openai.HasChatFileReferences(*request) {
		return nil
	}
	textRunes := 0
	for _, message := range request.Messages {
		parts, _ := message.Content.([]any)
		for _, part := range parts {
			object, _ := part.(map[string]any)
			if object["type"] == "input_document" {
				text, _ := object["text"].(string)
				textRunes += utf8.RuneCountInString(text)
			}
		}
	}
	owner := fileOwnerKey(identity)
	for messageIndex := range request.Messages {
		parts, ok := request.Messages[messageIndex].Content.([]any)
		if !ok {
			continue
		}
		for partIndex := range parts {
			object, ok := parts[partIndex].(map[string]any)
			if !ok || object["type"] != "input_file_reference" {
				continue
			}
			fileID, _ := object["file_id"].(string)
			file, err := h.ownedMessagesFile(ctx, owner, fileID)
			if err != nil {
				return err
			}
			switch file.ContentType {
			case "application/pdf":
				parts[partIndex] = map[string]any{
					"type":      "input_file",
					"file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(file.Content),
					"filename":  file.Filename,
				}
			case "text/plain":
				runes := utf8.RuneCount(file.Content)
				if !utf8.Valid(file.Content) || strings.TrimSpace(string(file.Content)) == "" || runes > 262144 || textRunes > 1048576-runes {
					return errMessagesFileUnavailable
				}
				textRunes += runes
				parts[partIndex] = map[string]any{"type": "input_document", "text": string(file.Content)}
			default:
				return errMessagesFileUnavailable
			}
		}
		request.Messages[messageIndex].Content = parts
	}
	if _, err := openai.ChatFileAttachments(request.Messages); err != nil {
		return errMessagesFileUnavailable
	}
	return nil
}
