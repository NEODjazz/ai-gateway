package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"ai-gateway-gateway/internal/a2astate"
)

func (h Handler) subscribeToA2ATask(w http.ResponseWriter, r *http.Request, request a2aRequest, profile AgentProfile) {
	if !validFileToken(request.Params.ID, 128) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	reqCtx, ok := h.authorizeA2ATaskOperation(w, r, request.ID)
	if !ok {
		return
	}
	stored, err := h.a2aTasks.GetA2ATask(r.Context(), fileOwnerKey(reqCtx), profile.ID, request.Params.ID)
	if err != nil {
		h.writeA2ATaskStoreError(w, request.ID, err)
		return
	}
	if !h.authorizeA2ATaskModel(w, request.ID, reqCtx, stored.Model) {
		return
	}
	task, _, err := decodeA2AStoredTask(stored.Payload)
	if err != nil || task.ID != stored.ID || task.ContextID != stored.ContextID || task.Status.State != stored.State {
		h.writeA2ATaskStoreError(w, request.ID, a2astate.ErrInvalid)
		return
	}
	if a2aTaskTerminal(task.Status.State) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32004, "Terminal tasks cannot be subscribed")
		return
	}
	select {
	case h.a2aSubscriptions <- struct{}{}:
		defer func() { <-h.a2aSubscriptions }()
	default:
		h.writeA2AError(w, request.ID, http.StatusTooManyRequests, -32603, "Task subscription capacity exceeded")
		return
	}
	writeStreamHeaders(w)
	w.WriteHeader(http.StatusOK)
	if err := writeA2AStreamResult(w, request.ID, map[string]any{"task": task}); err != nil {
		return
	}
	if a2aTaskStreamEnded(task.Status.State) {
		return
	}
	ticker := time.NewTicker(h.a2aTaskConfig.SubscriptionPoll)
	defer ticker.Stop()
	deadline := time.NewTimer(h.a2aTaskConfig.SubscriptionDuration)
	defer deadline.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
		}
		latest, loadErr := h.a2aTasks.GetA2ATask(r.Context(), stored.OwnerKey, profile.ID, stored.ID)
		if loadErr != nil {
			_ = writeA2AStreamError(w, request.ID, -32603, "Task status is temporarily unavailable")
			return
		}
		updated, latestResponseID, decodeErr := decodeA2AStoredTask(latest.Payload)
		if decodeErr != nil || updated.ID != latest.ID || updated.ContextID != latest.ContextID || updated.Status.State != latest.State {
			_ = writeA2AStreamError(w, request.ID, -32603, "Stored task is invalid")
			return
		}
		if latestResponseID != "" && a2aTaskPending(updated.Status.State) {
			latest, updated, loadErr = h.reconcileA2ABackgroundTask(r.Context(), reqCtx, latest, updated, latestResponseID)
			if errors.Is(loadErr, a2astate.ErrConflict) {
				continue
			}
			if loadErr != nil {
				_ = writeA2AStreamError(w, request.ID, -32603, "Task status is temporarily unavailable")
				return
			}
		}
		changed := !latest.UpdatedAt.Equal(stored.UpdatedAt) || updated.Status.State != task.Status.State
		if !changed {
			continue
		}
		for index := len(task.Artifacts); index < len(updated.Artifacts); index++ {
			artifact := updated.Artifacts[index]
			if err := writeA2AStreamResult(w, request.ID, map[string]any{"artifactUpdate": map[string]any{
				"taskId": updated.ID, "contextId": updated.ContextID, "artifact": artifact, "append": false, "lastChunk": true,
			}}); err != nil {
				return
			}
		}
		if err := writeA2AStreamResult(w, request.ID, map[string]any{"statusUpdate": map[string]any{
			"taskId": updated.ID, "contextId": updated.ContextID, "status": updated.Status,
		}}); err != nil {
			return
		}
		stored, task = latest, updated
		if a2aTaskStreamEnded(task.Status.State) {
			return
		}
	}
}

func writeA2AStreamResult(w http.ResponseWriter, id json.RawMessage, result any) error {
	payload, err := json.Marshal(a2aRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		return err
	}
	return writeSSEResponseEvent(w, "", string(payload))
}

func writeA2AStreamError(w http.ResponseWriter, id json.RawMessage, code int, message string) error {
	payload, err := json.Marshal(a2aRPCResponse{JSONRPC: "2.0", ID: id, Error: &a2aRPCError{Code: code, Message: message}})
	if err != nil {
		return err
	}
	return writeSSEResponseEvent(w, "", string(payload))
}
