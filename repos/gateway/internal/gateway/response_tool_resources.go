package gateway

import (
	"context"
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/containerstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/vectorstate"
)

func (h Handler) authorizeResponseToolResources(w http.ResponseWriter, ctx context.Context, identity *modules.RequestContext, request *openai.ResponseRequest) bool {
	owner := fileOwnerKey(*identity)
	for _, tool := range request.Tools {
		switch tool.Type {
		case "code_interpreter":
			containerID, fileIDs, message := openai.InspectResponseCodeInterpreterContainer(tool.Container)
			if message != "" {
				writeError(w, http.StatusBadRequest, "invalid_request", message)
				return false
			}
			if containerID != "" {
				if h.containers == nil {
					writeError(w, http.StatusServiceUnavailable, "container_unavailable", "container storage is unavailable")
					return false
				}
				record, err := h.containers.GetContainerRecord(ctx, owner, containerID)
				if err != nil {
					if errors.Is(err, containerstate.ErrUnavailable) {
						writeError(w, http.StatusServiceUnavailable, "container_unavailable", "container storage is unavailable")
					} else {
						writeError(w, http.StatusBadRequest, "invalid_request", "code interpreter container is unavailable")
					}
					return false
				}
				if record.Container.ID != containerID || record.Binding.Endpoint == "" || record.Binding.Deployment == "" || record.Binding.Model != request.Model {
					writeError(w, http.StatusBadRequest, "invalid_request", "code interpreter container is incompatible with the requested model")
					return false
				}
				if identity.Metadata == nil {
					identity.Metadata = map[string]string{}
				}
				identity.Metadata[modules.MetadataResponseContainerEndpoint] = record.Binding.Endpoint
				identity.Metadata[modules.MetadataResponseContainerDeployment] = record.Binding.Deployment
				identity.Metadata[modules.MetadataResponseContainerModel] = record.Binding.Model
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
