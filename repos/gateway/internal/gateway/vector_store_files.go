package gateway

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/vectorstate"
)

type attachVectorStoreFileRequest struct {
	FileID     string         `json:"file_id"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type updateVectorStoreFileRequest struct {
	Attributes *map[string]any `json:"attributes"`
}

type vectorStoreFileContent struct {
	FileID     string             `json:"file_id"`
	Filename   string             `json:"filename"`
	Attributes map[string]any     `json:"attributes"`
	Content    []vectorSearchText `json:"content"`
}

func (h Handler) AttachVectorStoreFile(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_store_files")
	if !ok || !h.vectorStoreFileStorageAvailable(w) || !validateVectorStoreFilePath(w, r, false) {
		return
	}
	var input attachVectorStoreFileRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if !validFileToken(input.FileID, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "file_id is invalid")
		return
	}
	if message := vectorstate.ValidateAttributes(input.Attributes); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	file, err := h.vectorStores.AttachVectorStoreFile(r.Context(), fileOwnerKey(req), r.PathValue("id"), input.FileID, normalizedVectorStoreAttributes(input.Attributes), h.vectorStoreConfig.FileQuota, h.vectorStoreConfig.ByteQuota)
	if err != nil {
		writeVectorStoreFileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicVectorStoreFile(file))
}

func (h Handler) ListVectorStoreFiles(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_store_files")
	if !ok || !h.vectorStoreFileStorageAvailable(w) || !validateVectorStoreFilePath(w, r, true) {
		return
	}
	query := r.URL.Query()
	for key, values := range query {
		if (key != "after" && key != "limit") || len(values) != 1 {
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
	if after != "" && !validFileToken(after, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "after is invalid")
		return
	}
	files, next, err := h.vectorStores.ListVectorStoreFiles(r.Context(), fileOwnerKey(req), r.PathValue("id"), limit, after)
	if err != nil {
		writeVectorStoreFileError(w, err)
		return
	}
	data := make([]map[string]any, len(files))
	for index := range files {
		data[index] = publicVectorStoreFile(files[index])
	}
	response := map[string]any{"object": "list", "data": data, "has_more": next != ""}
	if len(files) > 0 {
		response["first_id"] = files[0].FileID
		response["last_id"] = files[len(files)-1].FileID
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) GetVectorStoreFile(w http.ResponseWriter, r *http.Request) {
	h.getVectorStoreFile(w, r, false)
}

func (h Handler) UpdateVectorStoreFile(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_store_files")
	if !ok || !h.vectorStoreFileStorageAvailable(w) || !validateVectorStoreFilePath(w, r, false) {
		return
	}
	fileID := r.PathValue("file_id")
	if !validFileToken(fileID, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "file ID is invalid")
		return
	}
	var input updateVectorStoreFileRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if input.Attributes == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "attributes are required")
		return
	}
	if message := vectorstate.ValidateAttributes(*input.Attributes); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	file, err := h.vectorStores.UpdateVectorStoreFile(r.Context(), fileOwnerKey(req), r.PathValue("id"), fileID, normalizedVectorStoreAttributes(*input.Attributes))
	if err != nil {
		writeVectorStoreFileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicVectorStoreFile(file))
}

func (h Handler) GetVectorStoreFileContent(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_store_files")
	if !ok || !h.vectorStoreFileStorageAvailable(w) || !validateVectorStoreFilePath(w, r, false) {
		return
	}
	fileID := r.PathValue("file_id")
	if !validFileToken(fileID, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "file ID is invalid")
		return
	}
	owner := fileOwnerKey(req)
	attached, err := h.vectorStores.GetVectorStoreFile(r.Context(), owner, r.PathValue("id"), fileID)
	if err != nil {
		writeVectorStoreFileError(w, err)
		return
	}
	if h.files == nil {
		writeError(w, http.StatusServiceUnavailable, "vector_store_unavailable", "vector store file content is unavailable")
		return
	}
	file, err := h.files.Get(r.Context(), owner, fileID, false)
	if err != nil {
		writeVectorSearchError(w, err)
		return
	}
	if file.Purpose != "assistants" || !supportedVectorSearchContentType(file.ContentType) || file.Bytes > maxVectorSearchBytes {
		writeVectorSearchError(w, errVectorSearchUnsupportedFile)
		return
	}
	file, err = h.files.Get(r.Context(), owner, fileID, true)
	if err != nil {
		writeVectorSearchError(w, err)
		return
	}
	if !utf8.Valid(file.Content) || strings.IndexByte(string(file.Content), 0) >= 0 || len(file.Content) > maxVectorSearchBytes {
		writeVectorSearchError(w, errVectorSearchUnsupportedFile)
		return
	}
	texts := splitVectorSearchText(string(file.Content))
	if len(texts) == 0 {
		writeVectorSearchError(w, errVectorSearchEmpty)
		return
	}
	if len(texts) > maxVectorSearchChunks {
		writeVectorSearchError(w, errVectorSearchTooLarge)
		return
	}
	content := make([]vectorSearchText, len(texts))
	for index, text := range texts {
		content[index] = vectorSearchText{Type: "text", Text: text}
	}
	writeJSON(w, http.StatusOK, vectorStoreFileContent{FileID: fileID, Filename: file.Filename, Attributes: normalizedVectorStoreAttributes(attached.Attributes), Content: content})
}

func (h Handler) DeleteVectorStoreFile(w http.ResponseWriter, r *http.Request) {
	h.getVectorStoreFile(w, r, true)
}

func (h Handler) getVectorStoreFile(w http.ResponseWriter, r *http.Request, deleteFile bool) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_store_files")
	if !ok || !h.vectorStoreFileStorageAvailable(w) || !validateVectorStoreFilePath(w, r, false) {
		return
	}
	fileID := r.PathValue("file_id")
	if !validFileToken(fileID, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "file ID is invalid")
		return
	}
	owner := fileOwnerKey(req)
	storeID := r.PathValue("id")
	if deleteFile {
		if err := h.vectorStores.DeleteVectorStoreFile(r.Context(), owner, storeID, fileID); err != nil {
			writeVectorStoreFileError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": fileID, "object": "vector_store.file.deleted", "deleted": true})
		return
	}
	file, err := h.vectorStores.GetVectorStoreFile(r.Context(), owner, storeID, fileID)
	if err != nil {
		writeVectorStoreFileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicVectorStoreFile(file))
}

func (h Handler) vectorStoreFileStorageAvailable(w http.ResponseWriter) bool {
	if h.vectorStores == nil || h.vectorStoreConfig.FileQuota < 1 || h.vectorStoreConfig.ByteQuota < 1 {
		writeError(w, http.StatusServiceUnavailable, "vector_store_unavailable", "vector store file storage is unavailable")
		return false
	}
	return true
}

func validateVectorStoreFilePath(w http.ResponseWriter, r *http.Request, allowListQuery bool) bool {
	if !allowListQuery && r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return false
	}
	if !validFileToken(r.PathValue("id"), 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "vector store ID is invalid")
		return false
	}
	return true
}

func publicVectorStoreFile(file vectorstate.File) map[string]any {
	return map[string]any{
		"id": file.FileID, "object": "vector_store.file", "usage_bytes": file.Bytes,
		"created_at": file.CreatedAt.Unix(), "vector_store_id": file.VectorStoreID,
		"status": file.Status, "last_error": nil, "attributes": normalizedVectorStoreAttributes(file.Attributes),
	}
}

func writeVectorStoreFileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vectorstate.ErrNotFound):
		writeError(w, http.StatusNotFound, "vector_store_not_found", "vector store not found")
	case errors.Is(err, vectorstate.ErrFileNotFound):
		writeError(w, http.StatusNotFound, "vector_store_file_not_found", "vector store file not found")
	case errors.Is(err, vectorstate.ErrFileQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "vector_store_file_quota_exceeded", "vector store file quota exceeded")
	case errors.Is(err, vectorstate.ErrByteQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "vector_store_byte_quota_exceeded", "vector store byte quota exceeded")
	case errors.Is(err, vectorstate.ErrConflict):
		writeError(w, http.StatusConflict, "vector_store_file_conflict", "file is already attached to the vector store")
	case errors.Is(err, vectorstate.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid vector store file request")
	default:
		writeError(w, http.StatusServiceUnavailable, "vector_store_unavailable", "vector store file storage is unavailable")
	}
}
