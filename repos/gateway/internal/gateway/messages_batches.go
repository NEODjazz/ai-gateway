package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/batchstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type messagesBatchCreateRequest struct {
	Requests []messagesBatchCreateItem `json:"requests"`
}

type messagesBatchCreateItem struct {
	CustomID string          `json:"custom_id"`
	Params   json.RawMessage `json:"params"`
}

type messagesBatchCounts struct {
	Processing int `json:"processing"`
	Succeeded  int `json:"succeeded"`
	Errored    int `json:"errored"`
	Canceled   int `json:"canceled"`
	Expired    int `json:"expired"`
}

type messagesBatch struct {
	ID                string              `json:"id"`
	Type              string              `json:"type"`
	ProcessingStatus  string              `json:"processing_status"`
	RequestCounts     messagesBatchCounts `json:"request_counts"`
	CreatedAt         string              `json:"created_at"`
	ExpiresAt         string              `json:"expires_at"`
	EndedAt           *string             `json:"ended_at"`
	CancelInitiatedAt *string             `json:"cancel_initiated_at"`
	ArchivedAt        *string             `json:"archived_at"`
	ResultsURL        *string             `json:"results_url"`
}

func (h Handler) CreateMessagesBatch(w http.ResponseWriter, r *http.Request) {
	if !validateMessagesBatchHeaders(w, r) {
		return
	}
	var request messagesBatchCreateRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if len(request.Requests) < 1 || len(request.Requests) > openai.MaxBatchRequests {
		writeError(w, http.StatusBadRequest, "invalid_request", "requests must contain between 1 and 50000 items")
		return
	}
	identity, ok := h.authorizeMessagesBatchOperationValidated(w, r, "")
	if !ok {
		return
	}
	var payload bytes.Buffer
	seen := make(map[string]struct{}, len(request.Requests))
	for _, item := range request.Requests {
		if !validMessagesBatchCustomID(item.CustomID) || len(item.Params) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "custom_id or params is invalid")
			return
		}
		if _, duplicate := seen[item.CustomID]; duplicate {
			writeError(w, http.StatusBadRequest, "invalid_request", "custom_id must be unique")
			return
		}
		seen[item.CustomID] = struct{}{}
		line, err := json.Marshal(openai.BatchRequestLine{CustomID: item.CustomID, Method: http.MethodPost, URL: "/v1/messages", Body: item.Params})
		if err != nil || len(line) > batchMaxLineBytes {
			writeError(w, http.StatusBadRequest, "invalid_request", "batch item exceeds the 4 MiB limit")
			return
		}
		payload.Write(line)
		payload.WriteByte('\n')
	}
	items, err := h.decodeBatchItems(w, r.Context(), identity, "/v1/messages", payload.Bytes())
	if err != nil {
		if !errors.Is(err, errBatchResponseWritten) {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		}
		return
	}
	if len(items) > h.batchResultLimit()/batchResultMinLine {
		writeError(w, http.StatusBadRequest, "invalid_request", "batch contains too many requests for the configured result limit")
		return
	}
	create := openai.BatchCreateRequest{InputFileID: "native_messages", Endpoint: "/v1/messages", CompletionWindow: openai.BatchCompletionWindow, Metadata: map[string]string{"gateway.protocol": "messages"}}
	created, err := h.persistBatch(r.Context(), identity, create, items, "msgbatch_")
	if err != nil {
		if errors.Is(err, errBatchIdentityInvalid) {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		} else {
			writeBatchStoreError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, publicMessagesBatch(created))
}

func (h Handler) GetMessagesBatch(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.authorizeMessagesBatchOperation(w, r, r.PathValue("message_batch_id"))
	if !ok {
		return
	}
	batch, err := h.batches.GetBatch(r.Context(), fileOwnerKey(identity), r.PathValue("message_batch_id"))
	if err != nil || batch.Endpoint != "/v1/messages" {
		writeMessagesBatchStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicMessagesBatch(batch))
}

func (h Handler) ListMessagesBatches(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.authorizeMessagesBatchOperation(w, r, "")
	if !ok {
		return
	}
	query := r.URL.Query()
	for key, values := range query {
		if (key != "after_id" && key != "before_id" && key != "limit") || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return
		}
	}
	if query.Get("before_id") != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "before_id is not supported")
		return
	}
	limit := 20
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return
		}
		limit = value
	}
	owner, cursor := fileOwnerKey(identity), query.Get("after_id")
	if cursor != "" && (!validFileToken(cursor, 128) || !strings.HasPrefix(cursor, "msgbatch_")) {
		writeError(w, http.StatusBadRequest, "invalid_request", "after_id is invalid")
		return
	}
	result := make([]messagesBatch, 0, limit+1)
	for len(result) <= limit {
		page, next, err := h.batches.ListBatches(r.Context(), owner, 100, cursor)
		if err != nil {
			writeMessagesBatchStoreError(w, err)
			return
		}
		for _, batch := range page {
			if batch.Endpoint == "/v1/messages" && strings.HasPrefix(batch.ID, "msgbatch_") {
				result = append(result, publicMessagesBatch(batch))
				if len(result) > limit {
					break
				}
			}
		}
		if next == "" || len(result) > limit {
			break
		}
		cursor = next
	}
	hasMore := len(result) > limit
	if hasMore {
		result = result[:limit]
	}
	response := map[string]any{"data": result, "has_more": hasMore}
	if len(result) > 0 {
		response["first_id"], response["last_id"] = result[0].ID, result[len(result)-1].ID
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) CancelMessagesBatch(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.authorizeMessagesBatchOperation(w, r, r.PathValue("message_batch_id"))
	if !ok {
		return
	}
	owner, id := fileOwnerKey(identity), r.PathValue("message_batch_id")
	batch, err := h.batches.GetBatch(r.Context(), owner, id)
	if err != nil || batch.Endpoint != "/v1/messages" {
		writeMessagesBatchStoreError(w, err)
		return
	}
	batch, err = h.batches.CancelBatch(r.Context(), owner, id)
	if err != nil {
		writeMessagesBatchStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicMessagesBatch(batch))
}

func (h Handler) DeleteMessagesBatch(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.authorizeMessagesBatchOperation(w, r, r.PathValue("message_batch_id"))
	if !ok {
		return
	}
	owner, id := fileOwnerKey(identity), r.PathValue("message_batch_id")
	batch, err := h.batches.GetBatch(r.Context(), owner, id)
	if err != nil || batch.Endpoint != "/v1/messages" {
		writeMessagesBatchStoreError(w, err)
		return
	}
	if _, err = h.batches.DeleteBatch(r.Context(), owner, id); err != nil {
		writeMessagesBatchStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "type": "message_batch_deleted"})
}

func (h Handler) GetMessagesBatchResults(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.authorizeMessagesBatchOperation(w, r, r.PathValue("message_batch_id"))
	if !ok {
		return
	}
	owner, id := fileOwnerKey(identity), r.PathValue("message_batch_id")
	batch, err := h.batches.GetBatch(r.Context(), owner, id)
	if err != nil || batch.Endpoint != "/v1/messages" {
		writeMessagesBatchStoreError(w, err)
		return
	}
	if batch.Status != "completed" && batch.Status != "failed" && batch.Status != "cancelled" && batch.Status != "expired" {
		writeError(w, http.StatusConflict, "batch_not_ended", "message batch results are not ready")
		return
	}
	items, err := h.batches.ListBatchItems(r.Context(), owner, id)
	if err != nil {
		writeMessagesBatchStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	for _, item := range items {
		line := messagesBatchResult(batch, item)
		encoded, err := json.Marshal(line)
		if err != nil {
			return
		}
		_, _ = w.Write(append(encoded, '\n'))
	}
}

func validateMessagesBatchHeaders(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("anthropic-beta") != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "anthropic-version must be 2023-06-01; beta headers are unsupported")
		return false
	}
	if key := r.Header.Get("x-api-key"); key != "" {
		if auth := bearerToken(r.Header.Get("Authorization")); auth != "" && auth != key {
			writeError(w, http.StatusBadRequest, "invalid_request", "conflicting authentication headers")
			return false
		}
		r.Header.Set("Authorization", "Bearer "+key)
	}
	return true
}

func (h Handler) authorizeMessagesBatchOperation(w http.ResponseWriter, r *http.Request, id string) (modules.RequestContext, bool) {
	if !validateMessagesBatchHeaders(w, r) {
		return modules.RequestContext{}, false
	}
	return h.authorizeMessagesBatchOperationValidated(w, r, id)
}

func (h Handler) authorizeMessagesBatchOperationValidated(w http.ResponseWriter, r *http.Request, id string) (modules.RequestContext, bool) {
	identity, ok := h.authorizeOwnedStorageOperation(w, r, "messages_batch")
	if !ok || !h.batchStorageAvailable(w) {
		return modules.RequestContext{}, false
	}
	if id != "" && (!validFileToken(id, 128) || !strings.HasPrefix(id, "msgbatch_")) {
		writeError(w, http.StatusBadRequest, "invalid_request", "message batch ID is invalid")
		return modules.RequestContext{}, false
	}
	return identity, true
}

func validMessagesBatchCustomID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if char != '_' && char != '-' && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func publicMessagesBatch(batch batchstate.Batch) messagesBatch {
	status := "in_progress"
	if batch.Status == "cancelled" || batch.Status == "expired" || batch.Status == "completed" || batch.Status == "failed" {
		status = "ended"
	}
	remaining := max(batch.Total-batch.Completed-batch.Failed, 0)
	counts := messagesBatchCounts{Processing: remaining, Succeeded: batch.Completed, Errored: batch.Failed}
	if batch.Status == "cancelled" {
		counts.Processing, counts.Canceled = 0, remaining
	}
	if batch.Status == "expired" {
		counts.Processing, counts.Expired = 0, remaining
	}
	if batch.Status == "failed" {
		counts.Processing, counts.Errored = 0, counts.Errored+remaining
	}
	result := messagesBatch{ID: batch.ID, Type: "message_batch", ProcessingStatus: status, RequestCounts: counts, CreatedAt: batch.CreatedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: batch.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	format := func(value *time.Time) *string {
		if value == nil {
			return nil
		}
		formatted := value.UTC().Format(time.RFC3339Nano)
		return &formatted
	}
	result.CancelInitiatedAt = format(batch.CancellingAt)
	result.EndedAt = format(batch.CompletedAt)
	if result.EndedAt == nil {
		result.EndedAt = format(batch.CancelledAt)
	}
	if result.EndedAt == nil {
		result.EndedAt = format(batch.ExpiredAt)
	}
	if result.EndedAt == nil {
		result.EndedAt = format(batch.FailedAt)
	}
	if status == "ended" {
		url := "/v1/messages/batches/" + batch.ID + "/results"
		result.ResultsURL = &url
	}
	return result
}

func messagesBatchResult(batch batchstate.Batch, item batchstate.Item) map[string]any {
	result := map[string]any{"type": "canceled"}
	if batch.Status == "expired" && item.State == "pending" {
		result = map[string]any{"type": "expired"}
	} else if item.State != "pending" {
		var line openai.BatchOutputLine
		message := "batch item execution failed"
		result = map[string]any{"type": "errored", "error": map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": message}}}
		if json.Unmarshal(item.Result, &line) == nil && line.Response != nil && len(line.Response.Body) > 0 {
			var response any
			if json.Unmarshal(line.Response.Body, &response) == nil {
				result = map[string]any{"type": "succeeded", "message": response}
			}
		} else if line.Error != nil && line.Error.Message != "" {
			message = line.Error.Message
			result = map[string]any{"type": "errored", "error": map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": message}}}
		}
	}
	return map[string]any{"custom_id": item.CustomID, "result": result}
}

func writeMessagesBatchStoreError(w http.ResponseWriter, err error) {
	if err == nil || errors.Is(err, batchstate.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "message batch not found")
		return
	}
	writeBatchStoreError(w, err)
}
