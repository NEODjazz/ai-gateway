package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
)

const fileFormOverheadBytes int64 = 64 << 10

type FileRuntimeConfig struct {
	MaxBytes        int64
	OwnerQuotaBytes int64
}

type fileObject struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose"`
	Status    string `json:"status"`
}

func (h Handler) WithFileStore(store filestate.Store, config FileRuntimeConfig) Handler {
	h.files = store
	h.fileConfig = config
	return h
}

func (h Handler) CreateFile(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeFileOperation(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	if !h.fileStorageAvailable(w) {
		return
	}
	file, ok := decodeFileUpload(w, r, h.fileConfig.MaxBytes)
	if !ok {
		return
	}
	file.ID, ok = newFileID()
	if !ok {
		writeError(w, http.StatusInternalServerError, "file_id_failed", "file ID generation failed")
		return
	}
	file.OwnerKey = fileOwnerKey(req)
	created, err := h.files.Create(r.Context(), file, h.fileConfig.OwnerQuotaBytes)
	if err != nil {
		writeFileStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicFile(created))
}

func (h Handler) ListFiles(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeFileOperation(w, r)
	if !ok {
		return
	}
	if !h.fileStorageAvailable(w) {
		return
	}
	query := r.URL.Query()
	for key, values := range query {
		if (key != "after" && key != "limit" && key != "purpose") || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return
		}
	}
	limit := 20
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	after := query.Get("after")
	purpose := query.Get("purpose")
	if (after != "" && !validFileToken(after, 128)) || (purpose != "" && !validFileToken(purpose, 64)) {
		writeError(w, http.StatusBadRequest, "invalid_request", "after or purpose is invalid")
		return
	}
	files, next, err := h.files.List(r.Context(), fileOwnerKey(req), purpose, limit, after)
	if err != nil {
		writeFileStoreError(w, err)
		return
	}
	data := make([]fileObject, len(files))
	for index := range files {
		data[index] = publicFile(files[index])
	}
	response := map[string]any{"object": "list", "data": data, "has_more": next != ""}
	if len(data) > 0 {
		response["first_id"] = data[0].ID
		response["last_id"] = data[len(data)-1].ID
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) GetFile(w http.ResponseWriter, r *http.Request) {
	h.getFile(w, r, false)
}

func (h Handler) GetFileContent(w http.ResponseWriter, r *http.Request) {
	h.getFile(w, r, true)
}

func (h Handler) getFile(w http.ResponseWriter, r *http.Request, content bool) {
	req, ok := h.authorizeFileOperation(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	if !h.fileStorageAvailable(w) {
		return
	}
	id := r.PathValue("id")
	if !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "file ID is invalid")
		return
	}
	file, err := h.files.Get(r.Context(), fileOwnerKey(req), id, content)
	if err != nil {
		writeFileStoreError(w, err)
		return
	}
	if !content {
		writeJSON(w, http.StatusOK, publicFile(file))
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": file.Filename})
	w.Header().Set("Content-Type", file.ContentType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(file.Bytes, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file.Content)
}

func (h Handler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeFileOperation(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	if !h.fileStorageAvailable(w) {
		return
	}
	id := r.PathValue("id")
	if !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "file ID is invalid")
		return
	}
	if err := h.files.Delete(r.Context(), fileOwnerKey(req), id); err != nil {
		writeFileStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "file", "deleted": true})
}

func (h Handler) authorizeFileOperation(w http.ResponseWriter, r *http.Request) (modules.RequestContext, bool) {
	req := modules.RequestContext{
		APIKey:    bearerToken(r.Header.Get("Authorization")),
		RequestID: executionID(w),
		SessionID: sessionID(r),
		Metadata:  map[string]string{"gateway.api_type": "files"},
	}
	if err := h.pipeline.RunAuthentication(r.Context(), &req); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return modules.RequestContext{}, false
		}
		writeError(w, http.StatusBadGateway, "module_failed", "authentication failed")
		return modules.RequestContext{}, false
	}
	req.APIKey = ""
	if !h.prepareAccessGroups(w, &req) || !h.authorizeRateLimit(w, r.Context(), req, 0) {
		return modules.RequestContext{}, false
	}
	return req, true
}

func (h Handler) fileStorageAvailable(w http.ResponseWriter) bool {
	if h.files == nil || h.fileConfig.MaxBytes < 1 || h.fileConfig.OwnerQuotaBytes < h.fileConfig.MaxBytes {
		writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", "file storage is unavailable")
		return false
	}
	return true
}

func decodeFileUpload(w http.ResponseWriter, r *http.Request, maxBytes int64) (filestate.File, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+fileFormOverheadBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "multipart/form-data is required")
		return filestate.File{}, false
	}
	var file filestate.File
	seen := map[string]bool{}
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid or oversized multipart body")
			return filestate.File{}, false
		}
		name := part.FormName()
		if seen[name] || (name != "file" && name != "purpose") {
			_ = part.Close()
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown or repeated multipart field")
			return filestate.File{}, false
		}
		seen[name] = true
		switch name {
		case "purpose":
			if part.FileName() != "" {
				_ = part.Close()
				writeError(w, http.StatusBadRequest, "invalid_request", "purpose must be a scalar field")
				return filestate.File{}, false
			}
			value, readErr := io.ReadAll(io.LimitReader(part, 65))
			_ = part.Close()
			file.Purpose = strings.TrimSpace(string(value))
			if readErr != nil || !validFileToken(file.Purpose, 64) {
				writeError(w, http.StatusBadRequest, "invalid_request", "purpose is invalid")
				return filestate.File{}, false
			}
		case "file":
			filename := filepath.Base(part.FileName())
			payload, readErr := io.ReadAll(io.LimitReader(part, maxBytes+1))
			contentType := part.Header.Get("Content-Type")
			_ = part.Close()
			if readErr != nil || !validFileName(filename) || len(payload) == 0 || int64(len(payload)) > maxBytes {
				writeError(w, http.StatusBadRequest, "invalid_file", "file is empty, unnamed or exceeds the limit")
				return filestate.File{}, false
			}
			mediaType, _, parseErr := mime.ParseMediaType(contentType)
			if parseErr != nil || mediaType == "" || mediaType == "application/octet-stream" {
				mediaType = http.DetectContentType(payload)
			}
			if len(mediaType) > 255 {
				writeError(w, http.StatusBadRequest, "invalid_file", "file content type is invalid")
				return filestate.File{}, false
			}
			file.Filename = filename
			file.ContentType = mediaType
			file.Bytes = int64(len(payload))
			file.Content = payload
		}
	}
	if !seen["file"] || !seen["purpose"] {
		writeError(w, http.StatusBadRequest, "invalid_request", "file and purpose are required")
		return filestate.File{}, false
	}
	return file, true
}

func fileOwnerKey(req modules.RequestContext) string {
	digest := sha256.Sum256([]byte(req.CredentialID + "\x00" + req.UserID))
	return "v1/" + hex.EncodeToString(digest[:])
}

func newFileID() (string, bool) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", false
	}
	return "file_" + hex.EncodeToString(value[:]), true
}

func validFileToken(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validFileName(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func publicFile(file filestate.File) fileObject {
	return fileObject{ID: file.ID, Object: "file", Bytes: file.Bytes, CreatedAt: file.CreatedAt.Unix(), Filename: file.Filename, Purpose: file.Purpose, Status: "processed"}
}

func writeFileStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, filestate.ErrNotFound):
		writeError(w, http.StatusNotFound, "file_not_found", "file not found")
	case errors.Is(err, filestate.ErrQuotaExceeded):
		writeError(w, http.StatusRequestEntityTooLarge, "file_quota_exceeded", "file storage quota exceeded")
	case errors.Is(err, filestate.ErrConflict):
		writeError(w, http.StatusConflict, "file_conflict", "file already exists")
	case errors.Is(err, filestate.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid file storage request")
	default:
		writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", "file storage is unavailable")
	}
}
