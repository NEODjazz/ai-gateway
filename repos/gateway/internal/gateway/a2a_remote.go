package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

var errA2ARemoteUnavailable = errors.New("A2A remote content unavailable")

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func countA2ARemoteParts(parts []a2aPart) (int, error) {
	count, files, videos := 0, 0, 0
	for _, part := range parts {
		if (part.Raw != nil || part.URL != nil) && (part.MediaType == "application/pdf" || supportedA2ATextDocumentType(part.MediaType)) {
			files++
			if files > openai.MaxResponseFileAttachments {
				return 0, errors.New("invalid remote media part")
			}
		}
		if part.URL == nil {
			continue
		}
		if supportedA2AVideoType(part.MediaType) {
			videos++
			if videos > openai.MaxChatVideoAttachments {
				return 0, errors.New("invalid remote media part")
			}
		}
		count++
		if count > openai.MaxImageAttachments || part.Text != nil || part.Raw != nil || len(part.Data) != 0 || !validA2AFilename(part.Filename) || !supportedA2ARemoteType(part.MediaType) {
			return 0, errors.New("invalid remote media part")
		}
		parsed, err := url.Parse(*part.URL)
		if err != nil || len(*part.URL) > 2048 || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return 0, errors.New("invalid remote media URL")
		}
	}
	return count, nil
}

func (h Handler) resolveA2ARemoteParts(ctx context.Context, parts []a2aPart) error {
	if h.a2aHTTPClient == nil {
		return errA2ARemoteUnavailable
	}
	imageTotal, audioTotal, videoTotal, fileTotal, remoteTotal := 0, 0, 0, 0, 0
	for index := range parts {
		part := &parts[index]
		if part.URL == nil {
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, *part.URL, nil)
		if err != nil {
			return errors.New("invalid remote media URL")
		}
		request.Header.Set("Accept", "image/jpeg, image/png, image/gif, image/webp, audio/wav, audio/mpeg, video/mp4, video/webm, application/pdf, text/plain, text/markdown, text/csv")
		response, err := h.a2aHTTPClient.Do(request)
		if err != nil {
			return fmt.Errorf("%w: %v", errA2ARemoteUnavailable, err)
		}
		data, mediaType, readErr := readA2ARemoteContent(response, part.MediaType, openai.MaxTotalImageBytes-imageTotal, openai.MaxAudioBytes-audioTotal, openai.MaxChatVideoBytes-videoTotal, openai.MaxResponseFileBytes-fileTotal, openai.MaxInferenceBodyBytes-remoteTotal)
		if readErr != nil {
			return readErr
		}
		switch {
		case strings.HasPrefix(mediaType, "image/"):
			imageTotal += len(data)
		case strings.HasPrefix(mediaType, "audio/"):
			audioTotal += len(data)
		case strings.HasPrefix(mediaType, "video/"):
			videoTotal += len(data)
		case mediaType == "application/pdf" || supportedA2ATextDocumentType(mediaType):
			fileTotal += len(data)
		}
		remoteTotal += len(data)
		encoded := base64.StdEncoding.EncodeToString(data)
		part.Raw = &encoded
		part.URL = nil
		part.MediaType = mediaType
		if _, ok := a2aInputPart(*part); !ok {
			return errors.New("remote media bytes do not match its media type")
		}
	}
	return nil
}

func readA2ARemoteImage(response *http.Response, requestedType string, remaining int) ([]byte, string, error) {
	return readA2ARemoteContent(response, requestedType, remaining, 0, 0, 0, remaining)
}

func readA2ARemoteContent(response *http.Response, requestedType string, imageRemaining, audioRemaining, videoRemaining, fileRemaining, totalRemaining int) ([]byte, string, error) {
	if response == nil || response.Body == nil {
		return nil, "", errA2ARemoteUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%w: HTTP status %d", errA2ARemoteUnavailable, response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !supportedA2ARemoteType(mediaType) || mediaType == "" || requestedType != "" && requestedType != mediaType {
		return nil, "", errors.New("remote media content type is invalid")
	}
	limit, remaining := openai.MaxImageBytes, imageRemaining
	if strings.HasPrefix(mediaType, "audio/") {
		limit, remaining = openai.MaxAudioBytes, audioRemaining
	} else if strings.HasPrefix(mediaType, "video/") {
		limit, remaining = openai.MaxChatVideoBytes, videoRemaining
	} else if mediaType == "application/pdf" || supportedA2ATextDocumentType(mediaType) {
		limit, remaining = openai.MaxResponseFileBytes, fileRemaining
	}
	if remaining < limit {
		limit = remaining
	}
	if totalRemaining < limit {
		limit = totalRemaining
	}
	if limit <= 0 || response.ContentLength > int64(limit) {
		return nil, "", errors.New("remote media exceed their size limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: read response: %v", errA2ARemoteUnavailable, err)
	}
	if len(data) == 0 || len(data) > limit {
		return nil, "", errors.New("remote media exceeds its size limit")
	}
	return data, mediaType, nil
}

func supportedA2AImageType(mediaType string) bool {
	switch mediaType {
	case "", "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func supportedA2ARemoteType(mediaType string) bool {
	return supportedA2AImageType(mediaType) || mediaType == "audio/wav" || mediaType == "audio/mpeg" || supportedA2AVideoType(mediaType) || mediaType == "application/pdf" || supportedA2ATextDocumentType(mediaType)
}

func supportedA2AVideoType(mediaType string) bool {
	return mediaType == "video/mp4" || mediaType == "video/webm"
}

func supportedA2ATextDocumentType(mediaType string) bool {
	return mediaType == "text/plain" || mediaType == "text/markdown" || mediaType == "text/csv"
}

func validA2AFilename(filename string) bool {
	return filename == "" || len(filename) <= 256 && filepath.Base(filename) == filename && filename != "." && filename != ".." && !strings.ContainsAny(filename, "\x00/\\")
}
