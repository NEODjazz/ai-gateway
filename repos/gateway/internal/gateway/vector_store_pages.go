package gateway

import (
	"net/http"
	"strconv"

	"ai-gateway-gateway/internal/vectorstate"
)

func (h Handler) ListVectorStorePage(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "vector_stores")
	if !ok || !h.vectorStoreStorageAvailable(w) {
		return
	}
	input := r.URL.Query()
	for key, values := range input {
		if len(values) != 1 || (key != "offset" && key != "limit" && key != "search" && key != "status" && key != "sort_by" && key != "sort_order") {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated vector store page parameter")
			return
		}
	}
	q := vectorstate.PageQuery{Limit: 25, Search: input.Get("search"), Status: input.Get("status"), SortBy: input.Get("sort_by"), SortOrder: input.Get("sort_order")}
	if q.SortBy == "" {
		q.SortBy = "created_at"
	}
	if q.SortOrder == "" {
		q.SortOrder = "desc"
	}
	for key, destination := range map[string]*int{"offset": &q.Offset, "limit": &q.Limit} {
		if input.Has(key) {
			value, err := strconv.Atoi(input.Get(key))
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "offset and limit must be integers")
				return
			}
			*destination = value
		}
	}
	if !q.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid vector store page query")
		return
	}
	store, supported := h.vectorStores.(vectorstate.PageStore)
	if !supported {
		writeError(w, http.StatusServiceUnavailable, "vector_store_page_unavailable", "vector store directory is unavailable")
		return
	}
	stores, total, err := store.ListVectorStorePage(r.Context(), fileOwnerKey(req), q)
	if err != nil {
		writeVectorStoreError(w, err)
		return
	}
	data := make([]map[string]any, len(stores))
	for index := range stores {
		data[index] = publicVectorStore(stores[index])
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data, "total": total, "offset": q.Offset, "limit": q.Limit})
}
