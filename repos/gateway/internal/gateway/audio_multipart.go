package gateway

import (
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

func decodeAudioTranscriptionRequest(w http.ResponseWriter, r *http.Request) (openai.AudioTranscriptionRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, openai.MaxInferenceBodyBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "multipart/form-data is required")
		return openai.AudioTranscriptionRequest{}, false
	}
	var request openai.AudioTranscriptionRequest
	seen := map[string]bool{}
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid or oversized multipart body")
			return openai.AudioTranscriptionRequest{}, false
		}
		name := part.FormName()
		if name == "timestamp_granularities[]" || name == "include[]" || name == "languages[]" || name == "keywords[]" || name == "known_speaker_names[]" || name == "known_speaker_references[]" {
			var value string
			var ok bool
			if name == "known_speaker_references[]" {
				value, ok = readAudioReference(part)
			} else {
				value, ok = readAudioScalar(part)
			}
			if !ok || !appendAudioArrayField(&request, name, value) {
				writeError(w, http.StatusBadRequest, "invalid_request", "invalid multipart field "+strconv.Quote(name))
				return openai.AudioTranscriptionRequest{}, false
			}
			continue
		}
		if seen[name] {
			_ = part.Close()
			writeError(w, http.StatusBadRequest, "invalid_request", "repeated multipart field")
			return openai.AudioTranscriptionRequest{}, false
		}
		seen[name] = true
		if name == "file" {
			data, readErr := io.ReadAll(io.LimitReader(part, openai.MaxAudioBytes+1))
			filename := filepath.Base(part.FileName())
			contentType := part.Header.Get("Content-Type")
			_ = part.Close()
			if readErr != nil || len(data) == 0 || len(data) > openai.MaxAudioBytes || filename == "." || filename == "" {
				writeError(w, http.StatusBadRequest, "invalid_audio", "audio file is empty, unnamed or exceeds the limit")
				return openai.AudioTranscriptionRequest{}, false
			}
			mediaType, mediaErr := audioMediaType(filename, contentType, data)
			if mediaErr != nil {
				writeError(w, http.StatusBadRequest, "invalid_audio", mediaErr.Error())
				return openai.AudioTranscriptionRequest{}, false
			}
			request.File = openai.AudioAttachment{Filename: filename, MediaType: mediaType, Data: base64.StdEncoding.EncodeToString(data)}
			continue
		}
		value, ok := readAudioScalar(part)
		if !ok || !setAudioScalarField(&request, name, value) {
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown or malformed multipart field")
			return openai.AudioTranscriptionRequest{}, false
		}
	}
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return openai.AudioTranscriptionRequest{}, false
	}
	return request, true
}

func readAudioReference(part interface {
	Read([]byte) (int, error)
	Close() error
	FileName() string
}) (string, bool) {
	if part.FileName() != "" {
		_ = part.Close()
		return "", false
	}
	limit := int64(base64.StdEncoding.EncodedLen(openai.MaxKnownSpeakerReferenceBytes) + 64)
	value, err := io.ReadAll(io.LimitReader(part, limit+1))
	_ = part.Close()
	return string(value), err == nil && int64(len(value)) <= limit
}

func readAudioScalar(part interface {
	Read([]byte) (int, error)
	Close() error
	FileName() string
}) (string, bool) {
	if part.FileName() != "" {
		_ = part.Close()
		return "", false
	}
	value, err := io.ReadAll(io.LimitReader(part, maxImageFormFieldBytes+1))
	_ = part.Close()
	return string(value), err == nil && len(value) <= maxImageFormFieldBytes
}

func setAudioScalarField(request *openai.AudioTranscriptionRequest, name, value string) bool {
	switch name {
	case "provider":
		request.Provider = value
	case "model":
		request.Model = value
	case "language":
		request.Language = value
	case "prompt":
		request.Prompt = value
	case "response_format":
		request.ResponseFormat = value
	case "mode":
		request.Mode = value
	case "temperature":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return false
		}
		request.Temperature = &parsed
	case "chunking_strategy":
		parsed, err := openai.ParseAudioChunkingStrategy(value)
		if err != nil {
			return false
		}
		request.ChunkingStrategy = parsed
	case "stream":
		if value != "true" && value != "false" {
			return false
		}
		request.Stream = value == "true"
	default:
		return false
	}
	return true
}

func appendAudioArrayField(request *openai.AudioTranscriptionRequest, name, value string) bool {
	switch name {
	case "timestamp_granularities[]":
		request.TimestampGranularities = append(request.TimestampGranularities, value)
		return len(request.TimestampGranularities) <= 2
	case "include[]":
		request.Include = append(request.Include, value)
		return len(request.Include) <= 1
	case "languages[]":
		request.Languages = append(request.Languages, value)
		return len(request.Languages) <= openai.MaxAudioLanguages
	case "keywords[]":
		request.Keywords = append(request.Keywords, value)
		return len(request.Keywords) <= openai.MaxAudioKeywords
	case "known_speaker_names[]":
		request.KnownSpeakerNames = append(request.KnownSpeakerNames, value)
		return len(request.KnownSpeakerNames) <= openai.MaxKnownSpeakerReferences
	case "known_speaker_references[]":
		reference, err := openai.ParseDataAudioURL(value)
		if err != nil {
			return false
		}
		request.KnownSpeakerReferences = append(request.KnownSpeakerReferences, reference)
		return len(request.KnownSpeakerReferences) <= openai.MaxKnownSpeakerReferences
	default:
		return false
	}
}

func audioMediaType(filename, contentType string, data []byte) (string, error) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	unspecified := err != nil || mediaType == "" || mediaType == "application/octet-stream"
	if unspecified {
		mediaType = http.DetectContentType(data)
		unspecified = mediaType == "application/octet-stream"
	}
	extensionTypes := map[string][]string{
		".wav": {"audio/wav", "audio/wave", "audio/x-wav"}, ".flac": {"audio/flac"}, ".ogg": {"audio/ogg"},
		".webm": {"audio/webm", "video/webm"}, ".mp3": {"audio/mpeg", "audio/mp3"}, ".mpeg": {"audio/mpeg"},
		".mpga": {"audio/mpeg"}, ".mp4": {"audio/mp4", "video/mp4"}, ".m4a": {"audio/mp4", "audio/x-m4a"},
	}
	allowed := extensionTypes[strings.ToLower(filepath.Ext(filename))]
	for _, candidate := range allowed {
		attachment := openai.AudioAttachment{Filename: filename, MediaType: candidate, Data: base64.StdEncoding.EncodeToString(data)}
		if (unspecified || candidate == strings.ToLower(mediaType)) && openai.ValidateAudioAttachment(attachment) == nil {
			return candidate, nil
		}
	}
	return "", openai.ErrInvalidAudio
}
