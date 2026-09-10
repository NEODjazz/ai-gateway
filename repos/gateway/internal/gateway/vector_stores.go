package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/vectorstate"
)

type VectorStoreRuntimeConfig struct{ OwnerQuota int }

type vectorStoreRequest struct {
	Name         string                    `json:"name"`
	ExpiresAfter *vectorStoreExpiryRequest `json:"expires_after,omitempty"`
	Metadata     map[string]string         `json:"metadata,omitempty"`
}

type vectorStoreUpdateRequest struct {
	Name         *string                   `json:"name,omitempty"`
	ExpiresAfter *vectorStoreExpiryRequest `json:"expires_after,omitempty"`
	Metadata     *map[string]string        `json:"metadata,omitempty"`
}

type vectorStoreExpiryRequest struct {
	Anchor string `json:"anchor"`
	Days   int    `json:"days"`
}

func (h Handler) WithVectorStore(store vectorstate.Store, config VectorStoreRuntimeConfig) Handler {
	h.vectorStores = store
	h.vectorStoreConfig = config
	return h
}

func (h Handler) CreateVectorStore(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_stores")
	if !ok || !h.vectorStoreStorageAvailable(w) {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	var input vectorStoreRequest
	if !decodeInferenceRequest(w, r, &input) || !validateVectorStoreName(w, input.Name) || !validateVectorStoreMetadata(w, input.Metadata) {
		return
	}
	days, ok := validateVectorStoreExpiry(w, input.ExpiresAfter)
	if !ok {
		return
	}
	id, ok := newVectorStoreID()
	if !ok {
		writeError(w, http.StatusInternalServerError, "vector_store_id_failed", "vector store ID generation failed")
		return
	}
	created, err := h.vectorStores.CreateVectorStore(r.Context(), vectorstate.VectorStore{
		ID: id, OwnerKey: fileOwnerKey(req), Name: input.Name, Metadata: normalizedMetadata(input.Metadata), ExpiresAfter: days,
	}, h.vectorStoreConfig.OwnerQuota)
	if err != nil {
		writeVectorStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicVectorStore(created))
}

func (h Handler) ListVectorStores(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_stores")
	if !ok || !h.vectorStoreStorageAvailable(w) {
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
	stores, next, err := h.vectorStores.ListVectorStores(r.Context(), fileOwnerKey(req), limit, after)
	if err != nil {
		writeVectorStoreError(w, err)
		return
	}
	data := make([]map[string]any, len(stores))
	for index := range stores {
		data[index] = publicVectorStore(stores[index])
	}
	response := map[string]any{"object": "list", "data": data, "has_more": next != ""}
	if len(data) > 0 {
		response["first_id"] = stores[0].ID
		response["last_id"] = stores[len(stores)-1].ID
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) GetVectorStore(w http.ResponseWriter, r *http.Request) {
	h.getVectorStore(w, r, false)
}

func (h Handler) UpdateVectorStore(w http.ResponseWriter, r *http.Request) {
	h.getVectorStore(w, r, true)
}

func (h Handler) getVectorStore(w http.ResponseWriter, r *http.Request, update bool) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_stores")
	if !ok || !h.vectorStoreStorageAvailable(w) {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	id := r.PathValue("id")
	if !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "vector store ID is invalid")
		return
	}
	owner := fileOwnerKey(req)
	if !update {
		store, err := h.vectorStores.GetVectorStore(r.Context(), owner, id)
		if err != nil {
			writeVectorStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, publicVectorStore(store))
		return
	}
	var input vectorStoreUpdateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if input.Name == nil && input.Metadata == nil && input.ExpiresAfter == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "at least one update field is required")
		return
	}
	if input.Name != nil && !validateVectorStoreName(w, *input.Name) || input.Metadata != nil && !validateVectorStoreMetadata(w, *input.Metadata) {
		return
	}
	var days *int
	if input.ExpiresAfter != nil {
		value, valid := validateVectorStoreExpiry(w, input.ExpiresAfter)
		if !valid {
			return
		}
		days = &value
	}
	store, err := h.vectorStores.UpdateVectorStore(r.Context(), owner, id, vectorstate.Update{Name: input.Name, Metadata: input.Metadata, ExpiresAfter: days})
	if err != nil {
		writeVectorStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicVectorStore(store))
}

func (h Handler) DeleteVectorStore(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_stores")
	if !ok || !h.vectorStoreStorageAvailable(w) {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	id := r.PathValue("id")
	if !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "vector store ID is invalid")
		return
	}
	if err := h.vectorStores.DeleteVectorStore(r.Context(), fileOwnerKey(req), id); err != nil {
		writeVectorStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "vector_store.deleted", "deleted": true})
}

func (h Handler) vectorStoreStorageAvailable(w http.ResponseWriter) bool {
	if h.vectorStores == nil || h.vectorStoreConfig.OwnerQuota < 1 {
		writeError(w, http.StatusServiceUnavailable, "vector_store_unavailable", "vector store storage is unavailable")
		return false
	}
	return true
}

func validateVectorStoreName(w http.ResponseWriter, name string) bool {
	if name == "" || utf8.RuneCountInString(name) > 256 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		writeError(w, http.StatusBadRequest, "invalid_request", "name must contain 1 to 256 valid UTF-8 characters without surrounding whitespace")
		return false
	}
	return true
}

func validateVectorStoreMetadata(w http.ResponseWriter, metadata map[string]string) bool {
	if message := openai.ValidateMetadata(metadata); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return false
	}
	return true
}

func validateVectorStoreExpiry(w http.ResponseWriter, expiry *vectorStoreExpiryRequest) (int, bool) {
	if expiry == nil {
		return 0, true
	}
	if expiry.Anchor != "last_active_at" || expiry.Days < 1 || expiry.Days > 365 {
		writeError(w, http.StatusBadRequest, "invalid_request", "expires_after requires last_active_at and 1 to 365 days")
		return 0, false
	}
	return expiry.Days, true
}

func normalizedMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return map[string]string{}
	}
	return metadata
}

func newVectorStoreID() (string, bool) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", false
	}
	return "vs_" + hex.EncodeToString(value[:]), true
}

func publicVectorStore(store vectorstate.VectorStore) map[string]any {
	result := map[string]any{
		"id": store.ID, "object": "vector_store", "created_at": store.CreatedAt.Unix(), "name": store.Name,
		"usage_bytes": 0, "status": store.Status, "file_counts": map[string]int{"in_progress": 0, "completed": 0, "failed": 0, "cancelled": 0, "total": 0},
		"metadata": normalizedMetadata(store.Metadata), "last_active_at": store.LastActiveAt.Unix(),
	}
	if store.ExpiresAfter > 0 {
		result["expires_after"] = map[string]any{"anchor": "last_active_at", "days": store.ExpiresAfter}
	}
	if store.ExpiresAt != nil {
		result["expires_at"] = store.ExpiresAt.Unix()
	}
	return result
}

func writeVectorStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vectorstate.ErrNotFound):
		writeError(w, http.StatusNotFound, "vector_store_not_found", "vector store not found")
	case errors.Is(err, vectorstate.ErrQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "vector_store_quota_exceeded", "vector store quota exceeded")
	case errors.Is(err, vectorstate.ErrConflict):
		writeError(w, http.StatusConflict, "vector_store_conflict", "vector store already exists")
	case errors.Is(err, vectorstate.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid vector store request")
	default:
		writeError(w, http.StatusServiceUnavailable, "vector_store_unavailable", "vector store storage is unavailable")
	}
}
