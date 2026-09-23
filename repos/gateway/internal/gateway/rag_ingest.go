package gateway

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/ragstate"
	"ai-gateway-gateway/internal/vectorstate"
)

type ragIngestRequest struct {
	FileID           string                       `json:"file_id,omitempty"`
	File             *ragInlineFile               `json:"file,omitempty"`
	VectorStoreID    string                       `json:"vector_store_id,omitempty"`
	VectorStore      *vectorStoreRequest          `json:"vector_store,omitempty"`
	Attributes       map[string]any               `json:"attributes,omitempty"`
	ChunkingStrategy *vectorStoreChunkingStrategy `json:"chunking_strategy,omitempty"`
}

type ragInlineFile struct {
	Filename           string `json:"filename"`
	Content            []byte `json:"content"`
	ContentType        string `json:"content_type,omitempty"`
	ExpiresAfterSecond int64  `json:"expires_after_seconds,omitempty"`
}

func (h Handler) WithRAGIngestStore(store ragstate.Store) Handler {
	h.ragIngest = store
	return h
}

func (h Handler) RAGIngest(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := h.authorizeOwnedStorageOperation(w, r, "rag_ingest")
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	if !h.fileStorageAvailable(w) || !h.vectorStoreStorageAvailable(w) || !h.vectorStoreFileStorageAvailable(w) {
		return
	}
	if h.ragIngest == nil {
		writeError(w, http.StatusServiceUnavailable, "rag_ingest_unavailable", "atomic RAG ingestion is unavailable")
		return
	}
	var input ragIngestRequest
	if !decodeInferenceRequest(w, r, &input) || !validateRAGIngestInput(w, input) {
		return
	}
	if input.File != nil && int64(len(input.File.Content)) > h.fileConfig.MaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "inline file exceeds the configured file limit")
		return
	}
	owner := fileOwnerKey(reqCtx)
	request := ragstate.IngestRequest{
		OwnerKey: owner, FileID: input.FileID, VectorStoreID: input.VectorStoreID,
		Attributes: normalizedVectorStoreAttributes(input.Attributes), FileOwnerQuota: h.fileConfig.OwnerQuotaBytes,
		ChunkingStrategy: normalizedVectorStoreChunkingStrategy(input.ChunkingStrategy),
		VectorStoreQuota: h.vectorStoreConfig.OwnerQuota, VectorStoreFiles: h.vectorStoreConfig.FileQuota, VectorStoreBytes: h.vectorStoreConfig.ByteQuota,
	}
	if input.File != nil {
		id, generated := newFileID()
		if !generated {
			writeError(w, http.StatusInternalServerError, "file_id_failed", "file ID generation failed")
			return
		}
		request.File = &filestate.File{
			ID: id, OwnerKey: owner, Filename: input.File.Filename, Purpose: "assistants", ContentType: normalizedRAGContentType(*input.File),
			Bytes: int64(len(input.File.Content)), Content: input.File.Content, ExpiresAfterSeconds: input.File.ExpiresAfterSecond,
		}
	}
	if input.VectorStore != nil {
		id, generated := newVectorStoreID()
		if !generated {
			writeError(w, http.StatusInternalServerError, "vector_store_id_failed", "vector store ID generation failed")
			return
		}
		days, valid := validateVectorStoreExpiry(w, input.VectorStore.ExpiresAfter)
		if !valid {
			return
		}
		request.VectorStore = &vectorstate.VectorStore{ID: id, OwnerKey: owner, Name: input.VectorStore.Name, Metadata: normalizedMetadata(input.VectorStore.Metadata), ExpiresAfter: days}
	}
	if input.FileID != "" {
		file, err := h.files.Get(r.Context(), owner, input.FileID, true)
		if err != nil {
			writeFileStoreError(w, err)
			return
		}
		if message := validateRAGFile(file); message != "" {
			writeError(w, http.StatusUnprocessableEntity, "unsupported_rag_file", message)
			return
		}
	}
	result, err := h.ragIngest.IngestRAG(r.Context(), request)
	if err != nil {
		writeRAGIngestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "rag.ingest", "file": publicFile(result.File), "vector_store": publicVectorStore(result.VectorStore), "attachment": publicVectorStoreFile(result.Attachment),
	})
}

func validateRAGIngestInput(w http.ResponseWriter, input ragIngestRequest) bool {
	if (input.File == nil) == (input.FileID == "") || (input.VectorStore == nil) == (input.VectorStoreID == "") {
		writeError(w, http.StatusBadRequest, "invalid_request", "provide exactly one of file or file_id and exactly one of vector_store or vector_store_id")
		return false
	}
	if input.FileID != "" && !validFileToken(input.FileID, 128) || input.VectorStoreID != "" && !validFileToken(input.VectorStoreID, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "file_id or vector_store_id is invalid")
		return false
	}
	if input.File != nil {
		file := filestate.File{Filename: input.File.Filename, Purpose: "assistants", ContentType: normalizedRAGContentType(*input.File), Bytes: int64(len(input.File.Content)), Content: input.File.Content, ExpiresAfterSeconds: input.File.ExpiresAfterSecond}
		if !validFileName(file.Filename) || file.ExpiresAfterSeconds != 0 && (file.ExpiresAfterSeconds < filestate.MinimumExpirySeconds || file.ExpiresAfterSeconds > filestate.MaximumExpirySeconds) {
			writeError(w, http.StatusBadRequest, "invalid_file", "inline file name or expiry is invalid")
			return false
		}
		if message := validateRAGFile(file); message != "" {
			writeError(w, http.StatusUnprocessableEntity, "unsupported_rag_file", message)
			return false
		}
	}
	if input.VectorStore != nil && (!validateVectorStoreName(w, input.VectorStore.Name) || !validateVectorStoreMetadata(w, input.VectorStore.Metadata)) {
		return false
	}
	if message := vectorstate.ValidateAttributes(input.Attributes); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return false
	}
	return validateVectorStoreChunkingStrategy(w, input.ChunkingStrategy)
}

func normalizedRAGContentType(file ragInlineFile) string {
	value := strings.TrimSpace(file.ContentType)
	if value != "" {
		mediaType, _, err := mime.ParseMediaType(value)
		if err != nil || mediaType == "" {
			return ""
		}
		return strings.ToLower(mediaType)
	}
	return strings.ToLower(strings.TrimSpace(http.DetectContentType(file.Content)))
}

func validateRAGFile(file filestate.File) string {
	if file.Purpose != "assistants" {
		return "RAG files must use the assistants purpose"
	}
	if file.Bytes < 1 || file.Bytes > maxVectorSearchBytes || int64(len(file.Content)) != file.Bytes {
		return "RAG files must contain at most 1 MiB"
	}
	if !supportedVectorSearchContentType(file.ContentType) {
		return "RAG files must be UTF-8 text, Markdown, CSV, or JSON"
	}
	if !utf8.Valid(file.Content) || strings.IndexByte(string(file.Content), 0) >= 0 {
		return "RAG files must contain valid UTF-8 text without NUL bytes"
	}
	if strings.EqualFold(strings.TrimSpace(strings.SplitN(file.ContentType, ";", 2)[0]), "application/json") && !json.Valid(file.Content) {
		return "application/json RAG files must contain valid JSON"
	}
	return ""
}

func writeRAGIngestError(w http.ResponseWriter, err error) {
	if errors.Is(err, filestate.ErrNotFound) || errors.Is(err, filestate.ErrQuotaExceeded) || errors.Is(err, filestate.ErrConflict) || errors.Is(err, filestate.ErrInvalid) {
		writeFileStoreError(w, err)
		return
	}
	writeVectorStoreFileError(w, err)
}
