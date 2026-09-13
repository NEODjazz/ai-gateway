package gateway

import (
	"context"
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/vectorstate"
)

func (h Handler) authorizeResponseToolResources(w http.ResponseWriter, ctx context.Context, identity modules.RequestContext, tools []openai.ResponseTool) bool {
	owner := fileOwnerKey(identity)
	for _, tool := range tools {
		switch tool.Type {
		case "code_interpreter":
			fileIDs, message := openai.ResponseCodeInterpreterContainerFileIDs(tool.Container)
			if message != "" {
				writeError(w, http.StatusBadRequest, "invalid_request", message)
				return false
			}
			if len(fileIDs) == 0 {
				continue
			}
			if h.files == nil {
				writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", "file storage is unavailable")
				return false
			}
			for _, id := range fileIDs {
				if _, err := h.files.Get(ctx, owner, id, false); err != nil {
					if errors.Is(err, filestate.ErrUnavailable) {
						writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", "file storage is unavailable")
					} else {
						writeError(w, http.StatusBadRequest, "invalid_request", "code interpreter file is unavailable")
					}
					return false
				}
			}
		case "file_search":
			if h.vectorStores == nil {
				writeError(w, http.StatusServiceUnavailable, "vector_store_unavailable", "vector store storage is unavailable")
				return false
			}
			for _, id := range tool.VectorStoreIDs {
				if _, err := h.vectorStores.GetVectorStore(ctx, owner, id); err != nil {
					if errors.Is(err, vectorstate.ErrUnavailable) {
						writeError(w, http.StatusServiceUnavailable, "vector_store_unavailable", "vector store storage is unavailable")
					} else {
						writeError(w, http.StatusBadRequest, "invalid_request", "file search vector store is unavailable")
					}
					return false
				}
			}
		}
	}
	return true
}
