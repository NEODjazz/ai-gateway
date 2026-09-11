package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"ai-gateway-gateway/internal/vectorstate"
)

type createVectorStoreFileBatchRequest struct {
	FileIDs          []string                      `json:"file_ids,omitempty"`
	Files            []vectorStoreFileBatchRequest `json:"files,omitempty"`
	Attributes       map[string]any                `json:"attributes,omitempty"`
	ChunkingStrategy *vectorStoreChunkingStrategy  `json:"chunking_strategy,omitempty"`
}

type vectorStoreFileBatchRequest struct {
	FileID           string                       `json:"file_id"`
	Attributes       map[string]any               `json:"attributes,omitempty"`
	ChunkingStrategy *vectorStoreChunkingStrategy `json:"chunking_strategy,omitempty"`
}

func (h Handler) CreateVectorStoreFileBatch(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_store_file_batches")
	if !ok {
		return
	}
	store, available := h.vectorStoreFileBatchStorage(w)
	if !available || !validateVectorStoreBatchPath(w, r, false, false) {
		return
	}
	var input createVectorStoreFileBatchRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	entries, valid := validateVectorStoreFileBatchRequest(w, input)
	if !valid {
		return
	}
	id, generated := newVectorStoreFileBatchID()
	if !generated {
		writeError(w, http.StatusInternalServerError, "vector_store_file_batch_id_failed", "vector store file batch ID generation failed")
		return
	}
	batch, err := store.CreateVectorStoreFileBatch(r.Context(), vectorstate.FileBatch{ID: id, VectorStoreID: r.PathValue("id"), OwnerKey: fileOwnerKey(req)}, entries, h.vectorStoreConfig.FileQuota, h.vectorStoreConfig.ByteQuota)
	if err != nil {
		writeVectorStoreFileBatchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicVectorStoreFileBatch(batch))
}

func (h Handler) GetVectorStoreFileBatch(w http.ResponseWriter, r *http.Request) {
	h.getOrCancelVectorStoreFileBatch(w, r)
}

func (h Handler) CancelVectorStoreFileBatch(w http.ResponseWriter, r *http.Request) {
	h.getOrCancelVectorStoreFileBatch(w, r)
}

func (h Handler) getOrCancelVectorStoreFileBatch(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_store_file_batches")
	if !ok {
		return
	}
	store, available := h.vectorStoreFileBatchStorage(w)
	if !available || !validateVectorStoreBatchPath(w, r, true, false) {
		return
	}
	batch, err := store.GetVectorStoreFileBatch(r.Context(), fileOwnerKey(req), r.PathValue("id"), r.PathValue("batch_id"))
	if err != nil {
		writeVectorStoreFileBatchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicVectorStoreFileBatch(batch))
}

func (h Handler) ListVectorStoreFileBatchFiles(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_store_file_batches")
	if !ok {
		return
	}
	store, available := h.vectorStoreFileBatchStorage(w)
	if !available || !validateVectorStoreBatchPath(w, r, true, true) {
		return
	}
	options, valid := parseVectorStoreFileBatchListOptions(w, r)
	if !valid {
		return
	}
	files, next, err := store.ListVectorStoreFileBatchFiles(r.Context(), fileOwnerKey(req), r.PathValue("id"), r.PathValue("batch_id"), options)
	if err != nil {
		writeVectorStoreFileBatchError(w, err)
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

func parseVectorStoreFileBatchListOptions(w http.ResponseWriter, r *http.Request) (vectorstate.FileListOptions, bool) {
	query := r.URL.Query()
	for key, values := range query {
		if (key != "after" && key != "before" && key != "filter" && key != "limit" && key != "order") || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return vectorstate.FileListOptions{}, false
		}
	}
	limit := 20
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return vectorstate.FileListOptions{}, false
		}
		limit = parsed
	}
	after, before := query.Get("after"), query.Get("before")
	if after != "" && !validFileToken(after, 128) || before != "" && !validFileToken(before, 128) || after != "" && before != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "exactly one valid cursor may be supplied")
		return vectorstate.FileListOptions{}, false
	}
	order := query.Get("order")
	if order == "" {
		order = "desc"
	}
	status := query.Get("filter")
	options := vectorstate.FileListOptions{Limit: limit, After: after, Before: before, Order: order, Status: status}
	if !options.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_request", "order or filter is invalid")
		return vectorstate.FileListOptions{}, false
	}
	return options, true
}

func validateVectorStoreFileBatchRequest(w http.ResponseWriter, input createVectorStoreFileBatchRequest) ([]vectorstate.FileBatchEntry, bool) {
	if (len(input.FileIDs) == 0) == (len(input.Files) == 0) || len(input.FileIDs) > 2000 || len(input.Files) > 2000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "exactly one of file_ids or files with 1 to 2000 entries is required")
		return nil, false
	}
	entries := make([]vectorstate.FileBatchEntry, 0, max(len(input.FileIDs), len(input.Files)))
	if len(input.FileIDs) > 0 {
		if message := vectorstate.ValidateAttributes(input.Attributes); message != "" {
			writeError(w, http.StatusBadRequest, "invalid_request", message)
			return nil, false
		}
		if !validateVectorStoreChunkingStrategy(w, input.ChunkingStrategy) {
			return nil, false
		}
		for _, id := range input.FileIDs {
			entries = append(entries, vectorstate.FileBatchEntry{FileID: id, Attributes: normalizedVectorStoreAttributes(input.Attributes)})
		}
	} else {
		if input.Attributes != nil || input.ChunkingStrategy != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "global attributes and chunking_strategy cannot be combined with files")
			return nil, false
		}
		for _, file := range input.Files {
			if message := vectorstate.ValidateAttributes(file.Attributes); message != "" {
				writeError(w, http.StatusBadRequest, "invalid_request", message)
				return nil, false
			}
			if !validateVectorStoreChunkingStrategy(w, file.ChunkingStrategy) {
				return nil, false
			}
			entries = append(entries, vectorstate.FileBatchEntry{FileID: file.FileID, Attributes: normalizedVectorStoreAttributes(file.Attributes)})
		}
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if !validFileToken(entry.FileID, 128) {
			writeError(w, http.StatusBadRequest, "invalid_request", "file ID is invalid")
			return nil, false
		}
		if _, found := seen[entry.FileID]; found {
			writeError(w, http.StatusBadRequest, "invalid_request", "file IDs must be unique")
			return nil, false
		}
		seen[entry.FileID] = struct{}{}
	}
	return entries, true
}

func validateVectorStoreBatchPath(w http.ResponseWriter, r *http.Request, requireBatch, allowQuery bool) bool {
	if r.URL.RawQuery != "" && !allowQuery {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return false
	}
	if !validFileToken(r.PathValue("id"), 128) || requireBatch && !validFileToken(r.PathValue("batch_id"), 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "vector store or file batch ID is invalid")
		return false
	}
	return true
}

func (h Handler) vectorStoreFileBatchStorage(w http.ResponseWriter) (vectorstate.FileBatchStore, bool) {
	if !h.vectorStoreFileStorageAvailable(w) {
		return nil, false
	}
	store, ok := h.vectorStores.(vectorstate.FileBatchStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "vector_store_unavailable", "vector store file batch storage is unavailable")
	}
	return store, ok
}

func newVectorStoreFileBatchID() (string, bool) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", false
	}
	return "vsfb_" + hex.EncodeToString(value[:]), true
}

func publicVectorStoreFileBatch(batch vectorstate.FileBatch) map[string]any {
	return map[string]any{"id": batch.ID, "object": "vector_store.file_batch", "created_at": batch.CreatedAt.Unix(), "vector_store_id": batch.VectorStoreID, "status": batch.Status, "file_counts": map[string]int{"in_progress": 0, "completed": batch.Completed, "failed": batch.Failed, "cancelled": batch.Cancelled, "total": batch.Total}}
}

func writeVectorStoreFileBatchError(w http.ResponseWriter, err error) {
	if errors.Is(err, vectorstate.ErrFileBatchNotFound) {
		writeError(w, http.StatusNotFound, "vector_store_file_batch_not_found", "vector store file batch not found")
		return
	}
	writeVectorStoreFileError(w, err)
}
