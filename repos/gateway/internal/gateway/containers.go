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
	if len(input.FileIDs) > 0 {
		writeProviderParameterError(w, http.StatusBadRequest, "unsupported_parameter", "container file references require the container files API", "file_ids")
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
	record := containerstate.Record{OwnerKey: fileOwnerKey(identity), Binding: binding, Container: container}
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
	return input.ExpiresAfter == nil || input.ExpiresAfter.Anchor == "last_active_at" && input.ExpiresAfter.Minutes >= 1 && input.ExpiresAfter.Minutes <= 10080
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
