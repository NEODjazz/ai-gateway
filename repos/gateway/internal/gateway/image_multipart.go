package gateway

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const maxImageFormFieldBytes = 128 << 10

func decodeImageEditRequest(w http.ResponseWriter, r *http.Request) (openai.ImageEditRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, openai.MaxInferenceBodyBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "multipart/form-data is required")
		return openai.ImageEditRequest{}, false
	}
	var request openai.ImageEditRequest
	seen := map[string]bool{}
	totalImageBytes := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid or oversized multipart body")
			return openai.ImageEditRequest{}, false
		}
		name := part.FormName()
		if name == "image[]" {
			name = "image"
		}
		if name == "image" || name == "mask" {
			data, readErr := io.ReadAll(io.LimitReader(part, openai.MaxImageBytes+1))
			_ = part.Close()
			if readErr != nil || len(data) == 0 || len(data) > openai.MaxImageBytes {
				writeError(w, http.StatusBadRequest, "invalid_image", "image file is empty or exceeds the per-file limit")
				return openai.ImageEditRequest{}, false
			}
			totalImageBytes += len(data)
			if totalImageBytes > openai.MaxTotalImageBytes {
				writeError(w, http.StatusBadRequest, "invalid_image", "image files exceed the total size limit")
				return openai.ImageEditRequest{}, false
			}
			attachment, attachmentErr := multipartImageAttachment(part.Header.Get("Content-Type"), data)
			if attachmentErr != nil {
				writeError(w, http.StatusBadRequest, "invalid_image", attachmentErr.Error())
				return openai.ImageEditRequest{}, false
			}
			if name == "mask" {
				if seen[name] {
					writeError(w, http.StatusBadRequest, "invalid_request", "mask may only be provided once")
					return openai.ImageEditRequest{}, false
				}
				seen[name] = true
				request.Mask = &attachment
			} else {
				request.Images = append(request.Images, attachment)
				if len(request.Images) > openai.MaxImageAttachments {
					writeError(w, http.StatusBadRequest, "invalid_image", "too many image files")
					return openai.ImageEditRequest{}, false
				}
			}
			continue
		}
		if !imageEditScalarField(name) || seen[name] || part.FileName() != "" {
			_ = part.Close()
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown, repeated or malformed multipart field")
			return openai.ImageEditRequest{}, false
		}
		seen[name] = true
		value, readErr := io.ReadAll(io.LimitReader(part, maxImageFormFieldBytes+1))
		_ = part.Close()
		if readErr != nil || len(value) > maxImageFormFieldBytes || !setImageEditField(&request, name, string(value)) {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid multipart field "+strconv.Quote(name))
			return openai.ImageEditRequest{}, false
		}
	}
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return openai.ImageEditRequest{}, false
	}
	return request, true
}

func decodeImageVariationRequest(w http.ResponseWriter, r *http.Request) (openai.ImageVariationRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, openai.MaxInferenceBodyBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "multipart/form-data is required")
		return openai.ImageVariationRequest{}, false
	}
	var request openai.ImageVariationRequest
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid or oversized multipart body")
			return openai.ImageVariationRequest{}, false
		}
		name := part.FormName()
		limit := int64(maxImageFormFieldBytes + 1)
		if name == "image" {
			limit = int64(openai.MaxImageBytes + 1)
		}
		value, readErr := io.ReadAll(io.LimitReader(part, limit))
		_ = part.Close()
		if readErr != nil || seen[name] {
			writeError(w, http.StatusBadRequest, "invalid_request", "repeated or unreadable multipart field")
			return openai.ImageVariationRequest{}, false
		}
		seen[name] = true
		if name == "image" {
			if len(value) == 0 || len(value) > openai.MaxImageBytes {
				writeError(w, http.StatusBadRequest, "invalid_image", "image file is empty or exceeds the per-file limit")
				return openai.ImageVariationRequest{}, false
			}
			attachment, attachmentErr := multipartImageAttachment(part.Header.Get("Content-Type"), value)
			if attachmentErr != nil {
				writeError(w, http.StatusBadRequest, "invalid_image", attachmentErr.Error())
				return openai.ImageVariationRequest{}, false
			}
			request.Image = attachment
			continue
		}
		if part.FileName() != "" || len(value) > maxImageFormFieldBytes || !setImageVariationField(&request, name, string(value)) {
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown or malformed multipart field")
			return openai.ImageVariationRequest{}, false
		}
	}
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return openai.ImageVariationRequest{}, false
	}
	return request, true
}

func setImageVariationField(request *openai.ImageVariationRequest, name, value string) bool {
	switch name {
	case "provider":
		request.Provider = value
	case "model":
		request.Model = value
	case "response_format":
		request.ResponseFormat = value
	case "size":
		request.Size = value
	case "user":
		request.User = value
	case "n":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return false
		}
		request.N = &parsed
	default:
		return false
	}
	return true
}

func multipartImageAttachment(contentType string, data []byte) (openai.ImageAttachment, error) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = http.DetectContentType(data)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return openai.ParseDataImageURL("data:" + strings.ToLower(mediaType) + ";base64," + encoded)
}

func imageEditScalarField(name string) bool {
	switch name {
	case "provider", "model", "prompt", "n", "quality", "response_format", "size", "user", "background", "output_format", "output_compression":
		return true
	default:
		return false
	}
}

func setImageEditField(request *openai.ImageEditRequest, name, value string) bool {
	switch name {
	case "provider":
		request.Provider = value
	case "model":
		request.Model = value
	case "prompt":
		request.Prompt = value
	case "quality":
		request.Quality = value
	case "response_format":
		request.ResponseFormat = value
	case "size":
		request.Size = value
	case "user":
		request.User = value
	case "background":
		request.Background = value
	case "output_format":
		request.OutputFormat = value
	case "n", "output_compression":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return false
		}
		if name == "n" {
			request.N = &parsed
		} else {
			request.OutputCompression = &parsed
		}
	default:
		return false
	}
	return true
}

func imageAttachmentBytes(attachment openai.ImageAttachment) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(attachment.Data)
	if err != nil {
		return nil, fmt.Errorf("decode image attachment: %w", err)
	}
	return data, nil
}
