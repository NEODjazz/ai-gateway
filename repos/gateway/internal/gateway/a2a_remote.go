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
	count := 0
	for _, part := range parts {
		if part.URL == nil {
			continue
		}
		count++
		if count > openai.MaxImageAttachments || part.Text != nil || part.Raw != nil || len(part.Data) != 0 || !validA2AFilename(part.Filename) || !supportedA2AImageType(part.MediaType) {
			return 0, errors.New("invalid remote image part")
		}
		parsed, err := url.Parse(*part.URL)
		if err != nil || len(*part.URL) > 2048 || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return 0, errors.New("invalid remote image URL")
		}
	}
	return count, nil
}

func (h Handler) resolveA2ARemoteParts(ctx context.Context, parts []a2aPart) error {
	if h.a2aHTTPClient == nil {
		return errA2ARemoteUnavailable
	}
	total := 0
	for index := range parts {
		part := &parts[index]
		if part.URL == nil {
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, *part.URL, nil)
		if err != nil {
			return errors.New("invalid remote image URL")
		}
		request.Header.Set("Accept", "image/jpeg, image/png, image/gif, image/webp")
		response, err := h.a2aHTTPClient.Do(request)
		if err != nil {
			return fmt.Errorf("%w: %v", errA2ARemoteUnavailable, err)
		}
		data, mediaType, readErr := readA2ARemoteImage(response, part.MediaType, openai.MaxTotalImageBytes-total)
		if readErr != nil {
			return readErr
		}
		total += len(data)
		encoded := base64.StdEncoding.EncodeToString(data)
		if _, err := openai.ParseDataImageURL("data:" + mediaType + ";base64," + encoded); err != nil {
			return errors.New("remote image bytes do not match its media type")
		}
		part.Raw = &encoded
		part.URL = nil
		part.MediaType = mediaType
	}
	return nil
}

func readA2ARemoteImage(response *http.Response, requestedType string, remaining int) ([]byte, string, error) {
	if response == nil || response.Body == nil {
		return nil, "", errA2ARemoteUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%w: HTTP status %d", errA2ARemoteUnavailable, response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !supportedA2AImageType(mediaType) || requestedType != "" && requestedType != mediaType {
		return nil, "", errors.New("remote image content type is invalid")
	}
	limit := openai.MaxImageBytes
	if remaining < limit {
		limit = remaining
	}
	if limit <= 0 || response.ContentLength > int64(limit) {
		return nil, "", errors.New("remote images exceed their size limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: read response: %v", errA2ARemoteUnavailable, err)
	}
	if len(data) == 0 || len(data) > limit {
		return nil, "", errors.New("remote image exceeds its size limit")
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

func validA2AFilename(filename string) bool {
	return filename == "" || len(filename) <= 256 && filepath.Base(filename) == filename && filename != "." && filename != ".." && !strings.ContainsAny(filename, "\x00/\\")
}
