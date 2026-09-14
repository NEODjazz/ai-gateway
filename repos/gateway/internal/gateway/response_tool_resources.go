package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/containerstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/vectorstate"
)

var errResponseComputerFileStorageUnavailable = errors.New("computer screenshot file storage is unavailable")

func responseComputerOutputsHaveFiles(outputs []openai.ResponseComputerCallOutput) bool {
	for _, output := range outputs {
		if output.FileID != "" {
			return true
		}
	}
	return false
}

func (h Handler) resolveResponseComputerScreenshots(ctx context.Context, identity modules.RequestContext, request *openai.ResponseRequest) error {
	if request == nil {
		return errors.New("computer screenshot request is unavailable")
	}
	outputs, message := openai.InspectResponseComputerCallOutputs(request.Input)
	if message != "" {
		return errors.New(message)
	}
	if !responseComputerOutputsHaveFiles(outputs) {
		return nil
	}
	if h.files == nil {
		return errResponseComputerFileStorageUnavailable
	}
	encoded, err := json.Marshal(request.Input)
	if err != nil {
		return errors.New("computer screenshot input is invalid")
	}
	var items []any
	if json.Unmarshal(encoded, &items) != nil {
		return errors.New("computer screenshot input is invalid")
	}
	owner := fileOwnerKey(identity)
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok || item["type"] != "computer_call_output" {
			continue
		}
		screenshot, ok := item["output"].(map[string]any)
		if !ok || screenshot["type"] != "computer_screenshot" {
			return errors.New("computer_call_output.output must be a computer_screenshot object")
		}
		fileID, _ := screenshot["file_id"].(string)
		if fileID == "" {
			continue
		}
		file, err := h.files.Get(ctx, owner, fileID, true)
		if err != nil {
			if errors.Is(err, filestate.ErrUnavailable) {
				return errResponseComputerFileStorageUnavailable
			}
			return errors.New("computer screenshot file is unavailable")
		}
		mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(file.ContentType, ";", 2)[0]))
		if mediaType == "" {
			mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(http.DetectContentType(file.Content), ";", 2)[0]))
		}
		imageURL := "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(file.Content)
		if _, err := openai.ParseDataImageURL(imageURL); err != nil {
			return fmt.Errorf("computer screenshot file is not a supported image: %w", err)
		}
		delete(screenshot, "file_id")
		screenshot["image_url"] = imageURL
	}
	request.Input = items
	return nil
}

func (h Handler) authorizeResponseToolResources(w http.ResponseWriter, ctx context.Context, identity *modules.RequestContext, request *openai.ResponseRequest) bool {
	owner := fileOwnerKey(*identity)
	computerOutputs, message := openai.InspectResponseComputerCallOutputs(request.Input)
	if message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return false
	}
	for _, output := range computerOutputs {
		if output.FileID == "" {
			continue
		}
		if h.files == nil {
			writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", "file storage is unavailable")
			return false
		}
		if _, err := h.files.Get(ctx, owner, output.FileID, false); err != nil {
			if errors.Is(err, filestate.ErrUnavailable) {
				writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", "file storage is unavailable")
			} else {
				writeError(w, http.StatusBadRequest, "invalid_request", "computer screenshot file is unavailable")
			}
			return false
		}
	}
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
				if !bindResponseContainer(identity, record.Binding.Endpoint, record.Binding.Deployment, record.Binding.Model) {
					writeError(w, http.StatusBadRequest, "invalid_request", "response tools reference incompatible containers")
					return false
				}
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
		case "shell":
			_, containerID, fileIDs, message := openai.InspectResponseShellEnvironment(tool.Environment)
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
						writeError(w, http.StatusBadRequest, "invalid_request", "shell container is unavailable")
					}
					return false
				}
				if record.Container.ID != containerID || record.Binding.Endpoint == "" || record.Binding.Deployment == "" || record.Binding.Model != request.Model {
					writeError(w, http.StatusBadRequest, "invalid_request", "shell container is incompatible with the requested model")
					return false
				}
				if !bindResponseContainer(identity, record.Binding.Endpoint, record.Binding.Deployment, record.Binding.Model) {
					writeError(w, http.StatusBadRequest, "invalid_request", "response tools reference incompatible containers")
					return false
				}
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
						writeError(w, http.StatusBadRequest, "invalid_request", "shell file is unavailable")
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
		case "image_generation":
			if tool.InputImageMask == nil || tool.InputImageMask.FileID == "" {
				continue
			}
			if h.files == nil {
				writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", "file storage is unavailable")
				return false
			}
			if _, err := h.files.Get(ctx, owner, tool.InputImageMask.FileID, false); err != nil {
				if errors.Is(err, filestate.ErrUnavailable) {
					writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", "file storage is unavailable")
				} else {
					writeError(w, http.StatusBadRequest, "invalid_request", "image generation mask file is unavailable")
				}
				return false
			}
		case "computer":
			// computer_call_output file references are resolved before policy
			// modules run; the tool itself has no additional server resource.
		}
	}
	return true
}

func bindResponseContainer(identity *modules.RequestContext, endpoint, deployment, model string) bool {
	if identity.Metadata == nil {
		identity.Metadata = map[string]string{}
	}
	for key, value := range map[string]string{
		modules.MetadataResponseContainerEndpoint:   endpoint,
		modules.MetadataResponseContainerDeployment: deployment,
		modules.MetadataResponseContainerModel:      model,
	} {
		if existing := identity.Metadata[key]; existing != "" && existing != value {
			return false
		}
	}
	identity.Metadata[modules.MetadataResponseContainerEndpoint] = endpoint
	identity.Metadata[modules.MetadataResponseContainerDeployment] = deployment
	identity.Metadata[modules.MetadataResponseContainerModel] = model
	return true
}
