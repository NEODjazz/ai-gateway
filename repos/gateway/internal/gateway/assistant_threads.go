package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/assistantstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type assistantThreadSnapshot struct {
	ToolResources json.RawMessage   `json:"tool_resources"`
	Metadata      map[string]string `json:"metadata"`
}

type assistantThreadCreateRequest assistantThreadSnapshot

type assistantThreadUpdateRequest struct {
	ToolResources optionalAssistantRaw `json:"tool_resources,omitempty"`
	Metadata      *map[string]string   `json:"metadata,omitempty"`
}

func (h Handler) CreateAssistantThread(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.assistantThreadIdentity(w, r)
	if !ok || !h.assistantThreadStorageAvailable(w) {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	var input assistantThreadCreateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	snapshot := assistantThreadSnapshot(input)
	normalizeAssistantThreadSnapshot(&snapshot)
	if !validateAssistantThreadSnapshot(w, snapshot) || !h.authorizeAssistantThreadResources(w, r, identity, snapshot) {
		return
	}
	id, ok := newAssistantResourceID("thread_")
	if !ok {
		writeError(w, http.StatusInternalServerError, "thread_id_failed", "thread ID generation failed")
		return
	}
	payload, err := json.Marshal(snapshot)
	if err != nil || len(payload) > assistantstate.MaxSnapshotBytes {
		writeError(w, http.StatusBadRequest, "invalid_request", "thread definition exceeds its size limit")
		return
	}
	record, err := h.assistantThreads.CreateThread(r.Context(), assistantstate.ThreadRecord{ID: id, OwnerKey: fileOwnerKey(identity), Snapshot: payload}, h.assistantConfig.ThreadOwnerQuota)
	if err != nil {
		writeAssistantThreadError(w, err)
		return
	}
	h.writeAssistantThread(w, record)
}

func (h Handler) GetAssistantThread(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := h.assistantThreadResource(w, r)
	if !ok {
		return
	}
	record, err := h.assistantThreads.GetThread(r.Context(), fileOwnerKey(identity), id)
	if err != nil {
		writeAssistantThreadError(w, err)
		return
	}
	h.writeAssistantThread(w, record)
}

func (h Handler) UpdateAssistantThread(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := h.assistantThreadResource(w, r)
	if !ok {
		return
	}
	var input assistantThreadUpdateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if !input.ToolResources.Set && input.Metadata == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "at least one update field is required")
		return
	}
	owner := fileOwnerKey(identity)
	record, err := h.assistantThreads.GetThread(r.Context(), owner, id)
	if err != nil {
		writeAssistantThreadError(w, err)
		return
	}
	var snapshot assistantThreadSnapshot
	if json.Unmarshal(record.Snapshot, &snapshot) != nil {
		writeAssistantThreadError(w, assistantstate.ErrUnavailable)
		return
	}
	if input.ToolResources.Set {
		snapshot.ToolResources = append(json.RawMessage(nil), input.ToolResources.Value...)
	}
	if input.Metadata != nil {
		snapshot.Metadata = normalizedMetadata(*input.Metadata)
	}
	normalizeAssistantThreadSnapshot(&snapshot)
	if !validateAssistantThreadSnapshot(w, snapshot) || !h.authorizeAssistantThreadResources(w, r, identity, snapshot) {
		return
	}
	payload, err := json.Marshal(snapshot)
	if err != nil || len(payload) > assistantstate.MaxSnapshotBytes {
		writeError(w, http.StatusBadRequest, "invalid_request", "thread definition exceeds its size limit")
		return
	}
	updated, err := h.assistantThreads.UpdateThread(r.Context(), owner, id, payload, record.Revision)
	if err != nil {
		writeAssistantThreadError(w, err)
		return
	}
	h.writeAssistantThread(w, updated)
}

func (h Handler) DeleteAssistantThread(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := h.assistantThreadResource(w, r)
	if !ok {
		return
	}
	if err := h.assistantThreads.DeleteThread(r.Context(), fileOwnerKey(identity), id); err != nil {
		writeAssistantThreadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "thread.deleted", "deleted": true})
}

func (h Handler) assistantThreadIdentity(w http.ResponseWriter, r *http.Request) (modules.RequestContext, bool) {
	return h.authorizeOwnedStorageOperation(w, r, "assistant threads")
}

func (h Handler) assistantThreadResource(w http.ResponseWriter, r *http.Request) (modules.RequestContext, string, bool) {
	identity, ok := h.assistantThreadIdentity(w, r)
	if !ok || !h.assistantThreadStorageAvailable(w) {
		return modules.RequestContext{}, "", false
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return modules.RequestContext{}, "", false
	}
	id := r.PathValue("thread_id")
	if !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "thread ID is invalid")
		return modules.RequestContext{}, "", false
	}
	return identity, id, true
}

func (h Handler) assistantThreadStorageAvailable(w http.ResponseWriter) bool {
	if h.assistantThreads == nil || h.assistantConfig.ThreadOwnerQuota < 1 {
		writeError(w, http.StatusServiceUnavailable, "assistant_thread_storage_unavailable", "assistant thread storage is unavailable")
		return false
	}
	return true
}

func normalizeAssistantThreadSnapshot(snapshot *assistantThreadSnapshot) {
	if len(snapshot.ToolResources) == 0 || string(snapshot.ToolResources) == "null" {
		snapshot.ToolResources = json.RawMessage(`{}`)
	}
	snapshot.Metadata = normalizedMetadata(snapshot.Metadata)
}

func validateAssistantThreadSnapshot(w http.ResponseWriter, snapshot assistantThreadSnapshot) bool {
	if !validAssistantRawObject(snapshot.ToolResources, 256<<10) {
		writeError(w, http.StatusBadRequest, "invalid_request", "thread tool_resources are invalid")
		return false
	}
	if message := openai.ValidateMetadata(snapshot.Metadata); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return false
	}
	return true
}

func (h Handler) authorizeAssistantThreadResources(w http.ResponseWriter, r *http.Request, identity modules.RequestContext, snapshot assistantThreadSnapshot) bool {
	return h.authorizeAssistantResources(w, r.Context(), identity, assistantSnapshot{
		Tools:         []assistantTool{{Type: "code_interpreter"}, {Type: "file_search"}},
		ToolResources: snapshot.ToolResources,
	})
}

func newAssistantResourceID(prefix string) (string, bool) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", false
	}
	return prefix + hex.EncodeToString(value[:]), true
}

func publicAssistantThread(record assistantstate.ThreadRecord) (map[string]any, error) {
	var snapshot assistantThreadSnapshot
	if json.Unmarshal(record.Snapshot, &snapshot) != nil {
		return nil, assistantstate.ErrUnavailable
	}
	payload, _ := json.Marshal(snapshot)
	var result map[string]any
	if json.Unmarshal(payload, &result) != nil {
		return nil, assistantstate.ErrUnavailable
	}
	result["id"], result["object"], result["created_at"] = record.ID, "thread", record.CreatedAt.Unix()
	return result, nil
}

func (h Handler) writeAssistantThread(w http.ResponseWriter, record assistantstate.ThreadRecord) {
	value, err := publicAssistantThread(record)
	if err != nil {
		writeAssistantThreadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func writeAssistantThreadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, assistantstate.ErrNotFound):
		writeError(w, http.StatusNotFound, "thread_not_found", "thread not found")
	case errors.Is(err, assistantstate.ErrQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "thread_quota_exceeded", "thread quota exceeded")
	case errors.Is(err, assistantstate.ErrConflict):
		writeError(w, http.StatusConflict, "thread_conflict", "thread was modified concurrently")
	case errors.Is(err, assistantstate.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid thread request")
	default:
		writeError(w, http.StatusServiceUnavailable, "assistant_thread_storage_unavailable", "assistant thread storage is unavailable")
	}
}
