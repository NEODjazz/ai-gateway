package gateway

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/containerstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

const containerOwnerQuota = 1000
const maxContainerInitialFiles = 20

func (h Handler) WithContainerStore(store containerstate.Store) Handler {
	h.containers = store
	return h
}

func (h Handler) CreateContainer(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	var input openai.ContainerCreateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	identity, ok := h.authorizeOwnedStorageOperation(w, r, "container")
	if !ok {
		return
	}
	if h.containers == nil {
		writeError(w, http.StatusServiceUnavailable, "container_unavailable", "container storage is unavailable")
		return
	}
	if !validContainerCreateInput(input) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid container request")
		return
	}
	owner := fileOwnerKey(identity)
	if !h.validateOwnedContainerFiles(w, r, owner, input.FileIDs) {
		return
	}
	if !h.authorizeBatchModel(w, identity, input.Model) {
		return
	}
	runtime, ok := h.provider.(provider.ContainerProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "containers are not supported")
		return
	}
	fileRuntime, supportsFiles := h.provider.(provider.ContainerFileProvider)
	if len(input.FileIDs) > 0 && !supportsFiles {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "container files are not supported")
		return
	}
	var billingRequest modules.RequestContext
	reserved := false
	var billingErr error
	container, binding, err := runtime.CreateContainer(r.Context(), identity, input, func(ctx context.Context, request *modules.RequestContext) error {
		billingRequest = *request
		billingErr = h.resourceBillingPipeline().RunBillingLifecycle(ctx, &billingRequest, "reserve", nil)
		reserved = billingErr == nil && h.resourceBillingPipeline().HasModule("billing")
		return billingErr
	})
	if err != nil {
		if reserved {
			h.cancelContainerBilling(r.Context(), &billingRequest, err)
		}
		if billingErr != nil {
			writeVideoBillingFailure(w, billingErr)
			return
		}
		writeProviderFailure(w, err)
		return
	}
	for _, fileID := range input.FileIDs {
		file, fileErr := h.files.Get(r.Context(), owner, fileID, true)
		if fileErr != nil {
			h.compensateContainer(r.Context(), runtime, binding, container.ID, owner, false, &billingRequest, reserved, fileErr)
			writeFileStoreError(w, fileErr)
			return
		}
		if file.Bytes > maxGatewayContainerFileBytes || int64(len(file.Content)) != file.Bytes {
			fileErr = errors.New("container source file content is invalid or oversized")
			h.compensateContainer(r.Context(), runtime, binding, container.ID, owner, false, &billingRequest, reserved, fileErr)
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds the container upload limit")
			return
		}
		_, fileErr = fileRuntime.CreateContainerFile(r.Context(), binding, container.ID, provider.ContainerFileUpload{Filename: file.Filename, ContentType: file.ContentType, Content: file.Content})
		if fileErr != nil {
			h.compensateContainer(r.Context(), runtime, binding, container.ID, owner, false, &billingRequest, reserved, fileErr)
			writeProviderFailure(w, fileErr)
			return
		}
	}
	record := containerstate.Record{OwnerKey: owner, Binding: binding, Container: container}
	created, err := h.containers.CreateContainerRecord(r.Context(), record, containerOwnerQuota)
	if err != nil {
		h.compensateContainer(r.Context(), runtime, binding, container.ID, record.OwnerKey, false, &billingRequest, reserved, err)
		writeContainerStoreError(w, err)
		return
	}
	if reserved {
		if err = h.resourceBillingPipeline().RunBillingLifecycle(r.Context(), &billingRequest, "commit", nil); err != nil {
			h.compensateContainer(r.Context(), runtime, binding, container.ID, record.OwnerKey, true, &billingRequest, true, err)
			writeVideoBillingFailure(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, created.Container)
}

func validContainerCreateInput(input openai.ContainerCreateRequest) bool {
	if input.Model == "" || len(input.Model) > 256 || input.Name == "" || len(input.Name) > 256 || input.Name != strings.TrimSpace(input.Name) {
		return false
	}
	if input.Provider != "" && !validFileToken(input.Provider, 128) {
		return false
	}
	if input.MemoryLimit != "" && input.MemoryLimit != "1g" && input.MemoryLimit != "4g" && input.MemoryLimit != "16g" && input.MemoryLimit != "64g" {
		return false
	}
	if validateContainerNetworkPolicyInput(input.NetworkPolicy) != nil {
		return false
	}
	if len(input.FileIDs) > maxContainerInitialFiles {
		return false
	}
	seenFiles := make(map[string]struct{}, len(input.FileIDs))
	for _, fileID := range input.FileIDs {
		if !validFileToken(fileID, 128) {
			return false
		}
		if _, duplicate := seenFiles[fileID]; duplicate {
			return false
		}
		seenFiles[fileID] = struct{}{}
	}
	return input.ExpiresAfter == nil || input.ExpiresAfter.Anchor == "last_active_at" && input.ExpiresAfter.Minutes >= 1 && input.ExpiresAfter.Minutes <= 10080
}

func (h Handler) validateOwnedContainerFiles(w http.ResponseWriter, r *http.Request, owner string, fileIDs []string) bool {
	if len(fileIDs) == 0 {
		return true
	}
	if h.files == nil || h.fileConfig.MaxBytes < 1 {
		writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", "file storage is unavailable")
		return false
	}
	for _, fileID := range fileIDs {
		file, err := h.files.Get(r.Context(), owner, fileID, false)
		if err != nil {
			writeFileStoreError(w, err)
			return false
		}
		if file.Bytes < 0 || file.Bytes > maxGatewayContainerFileBytes || !validContainerFileName(file.Filename) {
			writeError(w, http.StatusBadRequest, "invalid_file", "container source file is invalid or oversized")
			return false
		}
	}
	return true
}

func validateContainerNetworkPolicyInput(policy *openai.ContainerNetworkPolicyRequest) error {
	return provider.ValidateContainerNetworkPolicyRequest(policy)
}

func (h Handler) ListContainers(w http.ResponseWriter, r *http.Request) {
	owner, _, _, ok := h.containerOwner(w, r)
	if !ok {
		return
	}
	limit, after, ok := containerListOptions(w, r)
	if !ok {
		return
	}
	records, next, err := h.containers.ListContainerRecords(r.Context(), owner, limit, after)
	if err != nil {
		writeContainerStoreError(w, err)
		return
	}
	data := make([]openai.Container, len(records))
	for i := range records {
		data[i] = records[i].Container
	}
	result := openai.ContainerList{Object: "list", Data: data, HasMore: next != ""}
	if len(data) > 0 {
		result.FirstID, result.LastID = data[0].ID, data[len(data)-1].ID
	}
	writeJSON(w, http.StatusOK, result)
}

func (h Handler) GetContainer(w http.ResponseWriter, r *http.Request) {
	owner, runtime, _, ok := h.containerOwner(w, r)
	if !ok {
		return
	}
	record, ok := h.containerRecord(w, r, owner)
	if !ok {
		return
	}
	container, err := runtime.RetrieveContainer(r.Context(), record.Binding, record.Container.ID)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	updated, err := h.containers.UpdateContainerRecord(r.Context(), owner, container)
	if err != nil {
		writeContainerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated.Container)
}

func (h Handler) DeleteContainer(w http.ResponseWriter, r *http.Request) {
	owner, runtime, _, ok := h.containerOwner(w, r)
	if !ok {
		return
	}
	record, ok := h.containerRecord(w, r, owner)
	if !ok {
		return
	}
	deleted, err := runtime.DeleteContainer(r.Context(), record.Binding, record.Container.ID)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	if err = h.containers.DeleteContainerRecord(r.Context(), owner, record.Container.ID); err != nil {
		writeContainerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deleted)
}

func (h Handler) compensateContainer(ctx context.Context, runtime provider.ContainerProvider, binding provider.ContainerBinding, id, owner string, stored bool, request *modules.RequestContext, reserved bool, cause error) {
	compensation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, deleteErr := runtime.DeleteContainer(compensation, binding, id)
	if deleteErr != nil {
		log.Printf("container compensation delete failed for %s: %v", id, deleteErr)
	} else if stored {
		if err := h.containers.DeleteContainerRecord(compensation, owner, id); err != nil {
			log.Printf("container compensation ownership cleanup failed for %s: %v", id, err)
		}
	}
	if reserved {
		if err := h.resourceBillingPipeline().RunBillingLifecycle(compensation, request, "cancel", cause); err != nil {
			log.Printf("container billing cancellation failed for %s: %v", id, err)
		}
	}
}

func (h Handler) cancelContainerBilling(ctx context.Context, request *modules.RequestContext, cause error) {
	compensation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := h.resourceBillingPipeline().RunBillingLifecycle(compensation, request, "cancel", cause); err != nil {
		log.Printf("container billing cancellation failed: %v", err)
	}
}

func (h Handler) containerOwner(w http.ResponseWriter, r *http.Request) (string, provider.ContainerProvider, modules.RequestContext, bool) {
	identity, ok := h.authorizeOwnedStorageOperation(w, r, "container")
	if !ok {
		return "", nil, modules.RequestContext{}, false
	}
	if h.containers == nil {
		writeError(w, http.StatusServiceUnavailable, "container_unavailable", "container storage is unavailable")
		return "", nil, modules.RequestContext{}, false
	}
	runtime, ok := h.provider.(provider.ContainerProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "containers are not supported")
		return "", nil, modules.RequestContext{}, false
	}
	return fileOwnerKey(identity), runtime, identity, true
}

func (h Handler) containerRecord(w http.ResponseWriter, r *http.Request, owner string) (containerstate.Record, bool) {
	id := r.PathValue("id")
	if !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "container ID is invalid")
		return containerstate.Record{}, false
	}
	record, err := h.containers.GetContainerRecord(r.Context(), owner, id)
	if err != nil {
		writeContainerStoreError(w, err)
		return containerstate.Record{}, false
	}
	return record, true
}

func containerListOptions(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	q := r.URL.Query()
	for key, values := range q {
		if (key != "after" && key != "limit" && key != "order") || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return 0, "", false
		}
	}
	after, order, limit := q.Get("after"), q.Get("order"), 20
	if after != "" && !validFileToken(after, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "after is invalid")
		return 0, "", false
	}
	if order != "" && order != "desc" {
		writeError(w, http.StatusBadRequest, "invalid_request", "only descending order is supported")
		return 0, "", false
	}
	if raw := q.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return 0, "", false
		}
		limit = value
	}
	return limit, after, true
}

func writeContainerStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, containerstate.ErrNotFound):
		writeError(w, http.StatusNotFound, "container_not_found", "container not found")
	case errors.Is(err, containerstate.ErrQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "container_limit_exceeded", "too many containers")
	case errors.Is(err, containerstate.ErrConflict):
		writeError(w, http.StatusConflict, "container_conflict", "container already exists")
	default:
		writeError(w, http.StatusServiceUnavailable, "container_unavailable", "container storage is unavailable")
	}
}
