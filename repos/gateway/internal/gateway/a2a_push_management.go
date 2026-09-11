package gateway

import (
	"errors"
	"net/http"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/modules"
)

func (h Handler) createA2APushConfig(w http.ResponseWriter, r *http.Request, request a2aRequest, profile AgentProfile) {
	config := a2aPushConfig{ID: request.Params.ID, URL: request.Params.URL, Token: request.Params.Token, Authentication: request.Params.Authentication}
	if !validFileToken(request.Params.TaskID, 128) || config.ID != "" && !validFileToken(config.ID, 128) || config.validate() != nil {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	req, task, ok := h.authorizeA2APushTask(w, r, request.ID, profile, request.Params.TaskID)
	if !ok {
		return
	}
	if config.ID == "" {
		config.ID = newA2AID("push")
	}
	record, job, err := h.newA2APushConfigAndJob(req, task, config)
	if err == nil {
		record, err = h.a2aPushConfigs.CreateA2APushConfig(r.Context(), record, a2aPushConfigQuota, job)
	}
	if err != nil {
		h.writeA2APushStoreError(w, request.ID, err)
		return
	}
	writeJSON(w, http.StatusOK, a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: a2aPushConfigResult(config, task.ID, record)})
}

func (h Handler) getA2APushConfig(w http.ResponseWriter, r *http.Request, request a2aRequest, profile AgentProfile) {
	if !validFileToken(request.Params.TaskID, 128) || !validFileToken(request.Params.ID, 128) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	_, task, ok := h.authorizeA2APushTask(w, r, request.ID, profile, request.Params.TaskID)
	if !ok {
		return
	}
	record, err := h.a2aPushConfigs.GetA2APushConfig(r.Context(), task.OwnerKey, profile.ID, task.ID, request.Params.ID)
	if err != nil {
		h.writeA2APushStoreError(w, request.ID, err)
		return
	}
	config, err := h.decryptA2APushConfig(record)
	if err != nil {
		h.writeA2APushStoreError(w, request.ID, err)
		return
	}
	writeJSON(w, http.StatusOK, a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: a2aPushConfigResult(config, task.ID, record)})
}

func (h Handler) listA2APushConfigs(w http.ResponseWriter, r *http.Request, request a2aRequest, profile AgentProfile) {
	pageSize := request.Params.PageSize
	if pageSize == 0 {
		pageSize = 50
	}
	if !validFileToken(request.Params.TaskID, 128) || pageSize < 1 || pageSize > 100 || request.Params.PageToken != "" && !validFileToken(request.Params.PageToken, 128) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	_, task, ok := h.authorizeA2APushTask(w, r, request.ID, profile, request.Params.TaskID)
	if !ok {
		return
	}
	records, next, total, err := h.a2aPushConfigs.ListA2APushConfigs(r.Context(), task.OwnerKey, profile.ID, task.ID, pageSize, request.Params.PageToken)
	if err != nil {
		h.writeA2APushStoreError(w, request.ID, err)
		return
	}
	configs := make([]map[string]any, 0, len(records))
	for _, record := range records {
		config, decodeErr := h.decryptA2APushConfig(record)
		if decodeErr != nil {
			h.writeA2APushStoreError(w, request.ID, decodeErr)
			return
		}
		configs = append(configs, a2aPushConfigResult(config, task.ID, record))
	}
	writeJSON(w, http.StatusOK, a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{
		"configs": configs, "nextPageToken": next, "pageSize": pageSize, "totalSize": total,
	}})
}

func (h Handler) deleteA2APushConfig(w http.ResponseWriter, r *http.Request, request a2aRequest, profile AgentProfile) {
	if !validFileToken(request.Params.TaskID, 128) || !validFileToken(request.Params.ID, 128) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	_, task, ok := h.authorizeA2APushTask(w, r, request.ID, profile, request.Params.TaskID)
	if !ok {
		return
	}
	if err := h.a2aPushConfigs.DeleteA2APushConfig(r.Context(), task.OwnerKey, profile.ID, task.ID, request.Params.ID); err != nil {
		h.writeA2APushStoreError(w, request.ID, err)
		return
	}
	writeJSON(w, http.StatusOK, a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{}})
}

func (h Handler) authorizeA2APushTask(w http.ResponseWriter, r *http.Request, rpcID []byte, profile AgentProfile, taskID string) (modules.RequestContext, a2astate.Task, bool) {
	if h.a2aPushConfigs == nil || h.a2aPushJobs == nil || h.a2aPushVault == nil {
		h.writeA2AError(w, rpcID, http.StatusNotImplemented, -32003, "Push notifications are not supported")
		return modules.RequestContext{}, a2astate.Task{}, false
	}
	req, ok := h.authorizeA2ATaskOperation(w, r, rpcID)
	if !ok {
		return modules.RequestContext{}, a2astate.Task{}, false
	}
	task, err := h.a2aTasks.GetA2ATask(r.Context(), fileOwnerKey(req), profile.ID, taskID)
	if err != nil {
		h.writeA2ATaskStoreError(w, rpcID, err)
		return modules.RequestContext{}, a2astate.Task{}, false
	}
	if !h.authorizeA2ATaskModel(w, rpcID, req, task.Model) {
		return modules.RequestContext{}, a2astate.Task{}, false
	}
	return req, task, true
}

func a2aPushConfigResult(config a2aPushConfig, taskID string, record a2astate.PushConfig) map[string]any {
	result := map[string]any{"id": config.ID, "taskId": taskID, "url": config.URL, "createdAt": record.CreatedAt.UTC().Format(time.RFC3339Nano)}
	if config.Token != "" {
		result["token"] = config.Token
	}
	if config.Authentication != nil {
		result["authentication"] = config.Authentication
	}
	return result
}

func (h Handler) writeA2APushStoreError(w http.ResponseWriter, id []byte, err error) {
	switch {
	case errors.Is(err, a2astate.ErrNotFound):
		h.writeA2AError(w, id, http.StatusNotFound, -32001, "Task or push notification configuration not found")
	case errors.Is(err, a2astate.ErrConflict):
		h.writeA2AError(w, id, http.StatusConflict, -32602, "Push notification configuration already exists")
	case errors.Is(err, a2astate.ErrQuotaExceeded):
		h.writeA2AError(w, id, http.StatusTooManyRequests, -32603, "Push notification configuration quota exceeded")
	case errors.Is(err, a2astate.ErrInvalid):
		h.writeA2AError(w, id, http.StatusInternalServerError, -32603, "Stored push notification configuration is invalid")
	default:
		h.writeA2AError(w, id, http.StatusServiceUnavailable, -32603, "Push notification storage is unavailable")
	}
}
