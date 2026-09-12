package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var errMessagesFileUnavailable = errors.New("document file is unavailable")
var errMessagesFileStorageUnavailable = errors.New("document file storage is unavailable")
var errMessagesRemoteUnavailable = errors.New("remote content is unavailable")

func (h Handler) resolveMessagesRequestDocumentReferences(ctx context.Context, identity modules.RequestContext, request *messagesRequest) error {
	owner := fileOwnerKey(identity)
	resolvedImageBytes := 0
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
			if block["type"] == "image" {
				source, _ := block["source"].(map[string]any)
				if source["type"] == "file" {
					fileID, _ := source["file_id"].(string)
					file, err := h.ownedMessagesFile(ctx, owner, fileID)
					if err != nil {
						return err
					}
					if !supportedA2AImageType(file.ContentType) || file.ContentType == "" || resolvedImageBytes > openai.MaxTotalImageBytes-len(file.Content) {
						return errMessagesFileUnavailable
					}
					resolvedImageBytes += len(file.Content)
					block["source"] = map[string]any{"type": "base64", "media_type": file.ContentType, "data": base64.StdEncoding.EncodeToString(file.Content)}
					changed = true
					continue
				}
				if source["type"] == "url" {
					remoteURL, _ := source["url"].(string)
					data, mediaType, err := h.fetchMessagesImage(ctx, remoteURL, openai.MaxTotalImageBytes-resolvedImageBytes)
					if err != nil {
						return err
					}
					resolvedImageBytes += len(data)
					block["source"] = map[string]any{"type": "base64", "media_type": mediaType, "data": base64.StdEncoding.EncodeToString(data)}
					changed = true
				}
				continue
			}
			if block["type"] != "document" {
				continue
			}
			source, _ := block["source"].(map[string]any)
			if source["type"] == "url" {
				remoteURL, _ := source["url"].(string)
				data, err := h.fetchMessagesPDF(ctx, remoteURL)
				if err != nil {
					return err
				}
				block["source"] = map[string]any{"type": "base64", "media_type": "application/pdf", "data": base64.StdEncoding.EncodeToString(data)}
				changed = true
				continue
			}
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

func (h Handler) fetchMessagesImage(ctx context.Context, remoteURL string, remaining int) ([]byte, string, error) {
	if h.a2aHTTPClient == nil {
		return nil, "", errMessagesRemoteUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL, nil)
	if err != nil {
		return nil, "", errors.New("image URL is invalid")
	}
	request.Header.Set("Accept", "image/jpeg, image/png, image/gif, image/webp")
	response, err := h.a2aHTTPClient.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", errMessagesRemoteUnavailable, err)
	}
	data, mediaType, err := readA2ARemoteContent(response, "", remaining, 0, 0, 0, remaining)
	if err != nil {
		if errors.Is(err, errA2ARemoteUnavailable) {
			return nil, "", fmt.Errorf("%w: %v", errMessagesRemoteUnavailable, err)
		}
		return nil, "", err
	}
	if !strings.HasPrefix(mediaType, "image/") {
		return nil, "", errors.New("image URL content type is invalid")
	}
	return data, mediaType, nil
}

func (h Handler) fetchMessagesPDF(ctx context.Context, remoteURL string) ([]byte, error) {
	if h.a2aHTTPClient == nil {
		return nil, errMessagesRemoteUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL, nil)
	if err != nil {
		return nil, errors.New("document URL is invalid")
	}
	request.Header.Set("Accept", "application/pdf")
	response, err := h.a2aHTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMessagesRemoteUnavailable, err)
	}
	data, mediaType, err := readA2ARemoteContent(response, "application/pdf", 0, 0, 0, openai.MaxResponseFileBytes, openai.MaxResponseFileBytes)
	if err != nil {
		if errors.Is(err, errA2ARemoteUnavailable) {
			return nil, fmt.Errorf("%w: %v", errMessagesRemoteUnavailable, err)
		}
		return nil, err
	}
	if mediaType != "application/pdf" {
		return nil, errors.New("document URL must return application/pdf")
	}
	return data, nil
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

func (h Handler) resolveMessagesDocumentReferences(ctx context.Context, identity modules.RequestContext, request *openai.ChatCompletionRequest) error {
	if !openai.HasChatResolvableReferences(*request) {
		return nil
	}
	images, err := openai.ChatImageAttachments(request.Messages)
	if err != nil {
		return err
	}
	imageBytes := 0
	for _, image := range images {
		decoded, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil {
			return err
		}
		imageBytes += len(decoded)
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
			if !ok {
				continue
			}
			if object["type"] == "input_url_document" {
				remoteURL, _ := object["url"].(string)
				data, err := h.fetchMessagesPDF(ctx, remoteURL)
				if err != nil {
					return err
				}
				parts[partIndex] = map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(data), "filename": "input.pdf"}
				continue
			}
			if object["type"] == "input_url_image" {
				remoteURL, _ := object["url"].(string)
				data, mediaType, err := h.fetchMessagesImage(ctx, remoteURL, openai.MaxTotalImageBytes-imageBytes)
				if err != nil {
					return err
				}
				imageBytes += len(data)
				parts[partIndex] = map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)}}
				continue
			}
			if object["type"] == "input_file_image_reference" {
				fileID, _ := object["file_id"].(string)
				file, err := h.ownedMessagesFile(ctx, owner, fileID)
				if err != nil {
					return err
				}
				if !referenceMediaTypeMatches(object, file.ContentType) || !supportedA2AImageType(file.ContentType) || file.ContentType == "" || imageBytes > openai.MaxTotalImageBytes-len(file.Content) {
					return errMessagesFileUnavailable
				}
				imageBytes += len(file.Content)
				parts[partIndex] = map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + file.ContentType + ";base64," + base64.StdEncoding.EncodeToString(file.Content)}}
				continue
			}
			if object["type"] == "input_file_audio_reference" {
				fileID, _ := object["file_id"].(string)
				file, err := h.ownedMessagesFile(ctx, owner, fileID)
				if err != nil {
					return err
				}
				format, filename := generateAudioFormat(file.ContentType)
				encoded := base64.StdEncoding.EncodeToString(file.Content)
				if !referenceMediaTypeMatches(object, file.ContentType) || format == "" || openai.ValidateAudioAttachment(openai.AudioAttachment{Filename: filename, MediaType: file.ContentType, Data: encoded}) != nil {
					return errMessagesFileUnavailable
				}
				parts[partIndex] = map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": encoded, "format": format}}
				continue
			}
			if object["type"] == "input_file_video_reference" {
				fileID, _ := object["file_id"].(string)
				file, err := h.ownedMessagesFile(ctx, owner, fileID)
				if err != nil {
					return err
				}
				format := ""
				switch file.ContentType {
				case "video/mp4":
					format = "mp4"
				case "video/webm":
					format = "webm"
				}
				video := map[string]any{"type": "input_video", "input_video": map[string]any{"data": base64.StdEncoding.EncodeToString(file.Content), "format": format}}
				if !referenceMediaTypeMatches(object, file.ContentType) || format == "" {
					return errMessagesFileUnavailable
				}
				if _, err := openai.ChatVideoAttachments([]openai.Message{{Role: "user", Content: []any{video}}}); err != nil {
					return errMessagesFileUnavailable
				}
				parts[partIndex] = video
				continue
			}
			if object["type"] != "input_file_reference" {
				continue
			}
			fileID, _ := object["file_id"].(string)
			file, err := h.ownedMessagesFile(ctx, owner, fileID)
			if err != nil {
				return err
			}
			if !referenceMediaTypeMatches(object, file.ContentType) {
				return errMessagesFileUnavailable
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
	if _, err := openai.ChatImageAttachments(request.Messages); err != nil {
		return err
	}
	if _, err := openai.ChatAudioAttachments(request.Messages); err != nil {
		return errMessagesFileUnavailable
	}
	if _, err := openai.ChatVideoAttachments(request.Messages); err != nil {
		return errMessagesFileUnavailable
	}
	return nil
}

func referenceMediaTypeMatches(reference map[string]any, actual string) bool {
	expected, _ := reference["media_type"].(string)
	return expected == "" || expected == actual
}

func generateAudioFormat(mediaType string) (string, string) {
	switch mediaType {
	case "audio/wav":
		return "wav", "input.wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3", "input.mp3"
	case "audio/flac":
		return "flac", "input.flac"
	case "audio/ogg":
		return "ogg", "input.ogg"
	case "audio/opus":
		return "opus", "input.opus"
	case "audio/aiff":
		return "aiff", "input.aiff"
	case "audio/aac":
		return "aac", "input.aac"
	case "audio/webm":
		return "webm", "input.webm"
	case "audio/mp4", "audio/m4a":
		return "m4a", "input.m4a"
	default:
		return "", ""
	}
}
