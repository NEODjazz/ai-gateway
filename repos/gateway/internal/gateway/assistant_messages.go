package gateway

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/assistantstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type assistantMessageSnapshot struct {
	Role        string                       `json:"role"`
	Content     []assistantMessageContent    `json:"content"`
	Attachments []assistantMessageAttachment `json:"attachments"`
	Metadata    map[string]string            `json:"metadata"`
	AssistantID string                       `json:"assistant_id,omitempty"`
	RunID       string                       `json:"run_id,omitempty"`
}

type assistantMessageContent struct {
	Type      string                     `json:"type"`
	Text      *assistantMessageText      `json:"text,omitempty"`
	ImageFile *assistantMessageImageFile `json:"image_file,omitempty"`
	ImageURL  *assistantMessageImageURL  `json:"image_url,omitempty"`
}

type assistantMessageText struct {
	Value       string `json:"value"`
	Annotations []any  `json:"annotations"`
}

type assistantMessageImageFile struct {
	FileID string `json:"file_id"`
	Detail string `json:"detail,omitempty"`
}

type assistantMessageImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type assistantMessageAttachment struct {
	FileID string                 `json:"file_id"`
	Tools  []assistantMessageTool `json:"tools"`
}

type assistantMessageTool struct {
	Type string `json:"type"`
}

type assistantMessageCreateRequest struct {
	Role        string                       `json:"role"`
	Content     json.RawMessage              `json:"content"`
	Attachments []assistantMessageAttachment `json:"attachments,omitempty"`
	Metadata    map[string]string            `json:"metadata,omitempty"`
}

type assistantMessageUpdateRequest struct {
	Metadata *map[string]string `json:"metadata"`
}

type assistantMessageInputPart struct {
	Type      string                     `json:"type"`
	Text      *string                    `json:"text,omitempty"`
	ImageFile *assistantMessageImageFile `json:"image_file,omitempty"`
	ImageURL  *assistantMessageImageURL  `json:"image_url,omitempty"`
}

func (h Handler) CreateAssistantMessage(w http.ResponseWriter, r *http.Request) {
	identity, threadID, ok := h.assistantThreadResource(w, r)
	if !ok {
		return
	}
	if !h.assistantMessageStorageAvailable(w) {
		return
	}
	var input assistantMessageCreateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	content, valid := h.normalizeAssistantMessageContent(w, r, identity, input.Content)
	if !valid {
		return
	}
	attachments := input.Attachments
	if attachments == nil {
		attachments = []assistantMessageAttachment{}
	}
	snapshot := assistantMessageSnapshot{Role: input.Role, Content: content, Attachments: attachments, Metadata: normalizedMetadata(input.Metadata)}
	if !h.validateAssistantMessage(w, r, identity, snapshot) {
		return
	}
	payload, err := json.Marshal(snapshot)
	if err != nil || len(payload) > assistantstate.MaxMessageSnapshotBytes {
		writeError(w, http.StatusBadRequest, "invalid_request", "message exceeds its size limit")
		return
	}
	id, ok := newAssistantResourceID("msg_")
	if !ok {
		writeError(w, http.StatusInternalServerError, "message_id_failed", "message ID generation failed")
		return
	}
	record, err := h.assistantThreads.CreateThreadMessage(r.Context(), assistantstate.MessageRecord{ID: id, ThreadID: threadID, OwnerKey: fileOwnerKey(identity), Snapshot: payload}, h.assistantConfig.MessageThreadQuota)
	if err != nil {
		writeAssistantMessageError(w, err)
		return
	}
	h.writeAssistantMessage(w, record)
}

func (h Handler) ListAssistantMessages(w http.ResponseWriter, r *http.Request) {
	identity, threadID, ok := h.assistantThreadListResource(w, r)
	if !ok {
		return
	}
	options, ok := assistantMessagePageOptions(w, r)
	if !ok {
		return
	}
	records, next, err := h.assistantThreads.ListThreadMessages(r.Context(), fileOwnerKey(identity), threadID, options)
	if err != nil {
		writeAssistantMessageError(w, err)
		return
	}
	data := make([]map[string]any, 0, len(records))
	for _, record := range records {
		value, decodeErr := publicAssistantMessage(record)
		if decodeErr != nil {
			writeAssistantMessageError(w, decodeErr)
			return
		}
		data = append(data, value)
	}
	response := map[string]any{"object": "list", "data": data, "has_more": next != ""}
	if len(records) > 0 {
		response["first_id"], response["last_id"] = records[0].ID, records[len(records)-1].ID
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) GetAssistantMessage(w http.ResponseWriter, r *http.Request) {
	identity, threadID, messageID, ok := h.assistantMessageResource(w, r)
	if !ok {
		return
	}
	record, err := h.assistantThreads.GetThreadMessage(r.Context(), fileOwnerKey(identity), threadID, messageID)
	if err != nil {
		writeAssistantMessageError(w, err)
		return
	}
	h.writeAssistantMessage(w, record)
}

func (h Handler) UpdateAssistantMessage(w http.ResponseWriter, r *http.Request) {
	identity, threadID, messageID, ok := h.assistantMessageResource(w, r)
	if !ok {
		return
	}
	var input assistantMessageUpdateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if input.Metadata == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "metadata is required")
		return
	}
	if message := openai.ValidateMetadata(*input.Metadata); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	owner := fileOwnerKey(identity)
	record, err := h.assistantThreads.GetThreadMessage(r.Context(), owner, threadID, messageID)
	if err != nil {
		writeAssistantMessageError(w, err)
		return
	}
	var snapshot assistantMessageSnapshot
	if json.Unmarshal(record.Snapshot, &snapshot) != nil {
		writeAssistantMessageError(w, assistantstate.ErrUnavailable)
		return
	}
	snapshot.Metadata = normalizedMetadata(*input.Metadata)
	payload, err := json.Marshal(snapshot)
	if err != nil || len(payload) > assistantstate.MaxMessageSnapshotBytes {
		writeError(w, http.StatusBadRequest, "invalid_request", "message exceeds its size limit")
		return
	}
	updated, err := h.assistantThreads.UpdateThreadMessage(r.Context(), owner, threadID, messageID, payload, record.Revision)
	if err != nil {
		writeAssistantMessageError(w, err)
		return
	}
	h.writeAssistantMessage(w, updated)
}

func (h Handler) DeleteAssistantMessage(w http.ResponseWriter, r *http.Request) {
	identity, threadID, messageID, ok := h.assistantMessageResource(w, r)
	if !ok {
		return
	}
	if err := h.assistantThreads.DeleteThreadMessage(r.Context(), fileOwnerKey(identity), threadID, messageID); err != nil {
		writeAssistantMessageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": messageID, "object": "thread.message.deleted", "deleted": true})
}

func (h Handler) assistantThreadListResource(w http.ResponseWriter, r *http.Request) (modules.RequestContext, string, bool) {
	identity, ok := h.assistantThreadIdentity(w, r)
	if !ok || !h.assistantMessageStorageAvailable(w) {
		return modules.RequestContext{}, "", false
	}
	id := r.PathValue("thread_id")
	if !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "thread ID is invalid")
		return modules.RequestContext{}, "", false
	}
	return identity, id, true
}

func (h Handler) assistantMessageResource(w http.ResponseWriter, r *http.Request) (modules.RequestContext, string, string, bool) {
	identity, threadID, ok := h.assistantThreadListResource(w, r)
	if !ok {
		return modules.RequestContext{}, "", "", false
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return modules.RequestContext{}, "", "", false
	}
	id := r.PathValue("message_id")
	if !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "message ID is invalid")
		return modules.RequestContext{}, "", "", false
	}
	return identity, threadID, id, true
}

func (h Handler) assistantMessageStorageAvailable(w http.ResponseWriter) bool {
	if h.assistantThreads == nil || h.assistantConfig.MessageThreadQuota < 1 {
		writeError(w, http.StatusServiceUnavailable, "assistant_message_storage_unavailable", "assistant message storage is unavailable")
		return false
	}
	return true
}

func assistantMessagePageOptions(w http.ResponseWriter, r *http.Request) (assistantstate.MessagePageOptions, bool) {
	query := r.URL.Query()
	for key, values := range query {
		if key != "limit" && key != "after" && key != "before" && key != "order" || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return assistantstate.MessagePageOptions{}, false
		}
	}
	options := assistantstate.MessagePageOptions{Limit: 20, After: query.Get("after"), Before: query.Get("before"), Order: query.Get("order")}
	if options.Order == "" {
		options.Order = "desc"
	}
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return assistantstate.MessagePageOptions{}, false
		}
		options.Limit = parsed
	}
	if options.Order != "asc" && options.Order != "desc" || options.After != "" && options.Before != "" || options.After != "" && !validFileToken(options.After, 128) || options.Before != "" && !validFileToken(options.Before, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "message pagination parameters are invalid")
		return assistantstate.MessagePageOptions{}, false
	}
	return options, true
}

func (h Handler) normalizeAssistantMessageContent(w http.ResponseWriter, r *http.Request, identity modules.RequestContext, raw json.RawMessage) ([]assistantMessageContent, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if !validAssistantMessageText(text) {
			writeError(w, http.StatusBadRequest, "invalid_request", "message text is invalid")
			return nil, false
		}
		return []assistantMessageContent{{Type: "text", Text: &assistantMessageText{Value: text, Annotations: []any{}}}}, true
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) < 1 || len(parts) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_request", "message content must contain between 1 and 64 parts")
		return nil, false
	}
	result := make([]assistantMessageContent, 0, len(parts))
	for _, rawPart := range parts {
		var part assistantMessageInputPart
		if decodeStrictJSON(rawPart, &part) != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "message content part is invalid")
			return nil, false
		}
		switch part.Type {
		case "text":
			if part.Text == nil || part.ImageFile != nil || part.ImageURL != nil || !validAssistantMessageText(*part.Text) {
				writeError(w, http.StatusBadRequest, "invalid_request", "message text part is invalid")
				return nil, false
			}
			result = append(result, assistantMessageContent{Type: "text", Text: &assistantMessageText{Value: *part.Text, Annotations: []any{}}})
		case "image_file":
			if part.Text != nil || part.ImageFile == nil || part.ImageURL != nil || !validAssistantImageDetail(part.ImageFile.Detail) {
				writeError(w, http.StatusBadRequest, "invalid_request", "message image file part is invalid")
				return nil, false
			}
			if !h.validateAssistantMessageFile(w, r, identity, part.ImageFile.FileID) {
				return nil, false
			}
			result = append(result, assistantMessageContent{Type: "image_file", ImageFile: part.ImageFile})
		case "image_url":
			if part.Text != nil || part.ImageFile != nil || part.ImageURL == nil || !validAssistantImageURL(part.ImageURL.URL) || !validAssistantImageDetail(part.ImageURL.Detail) {
				writeError(w, http.StatusBadRequest, "invalid_request", "message image URL part is invalid")
				return nil, false
			}
			result = append(result, assistantMessageContent{Type: "image_url", ImageURL: part.ImageURL})
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "message content part type is unsupported")
			return nil, false
		}
	}
	return result, true
}

func (h Handler) validateAssistantMessage(w http.ResponseWriter, r *http.Request, identity modules.RequestContext, snapshot assistantMessageSnapshot) bool {
	if snapshot.Role != "user" && snapshot.Role != "assistant" {
		writeError(w, http.StatusBadRequest, "invalid_request", "message role must be user or assistant")
		return false
	}
	if message := openai.ValidateMetadata(snapshot.Metadata); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return false
	}
	if len(snapshot.Attachments) > 20 {
		writeError(w, http.StatusBadRequest, "invalid_request", "message attachments exceed the supported limit")
		return false
	}
	seen := map[string]bool{}
	for _, attachment := range snapshot.Attachments {
		if seen[attachment.FileID] || len(attachment.Tools) < 1 || len(attachment.Tools) > 2 {
			writeError(w, http.StatusBadRequest, "invalid_request", "message attachments are invalid")
			return false
		}
		if !h.validateAssistantMessageFile(w, r, identity, attachment.FileID) {
			return false
		}
		seen[attachment.FileID] = true
		toolNames := make([]string, 0, len(attachment.Tools))
		toolSeen := map[string]bool{}
		for _, tool := range attachment.Tools {
			if tool.Type != "code_interpreter" && tool.Type != "file_search" || toolSeen[tool.Type] {
				writeError(w, http.StatusBadRequest, "invalid_request", "message attachment tools are invalid")
				return false
			}
			toolSeen[tool.Type] = true
			toolNames = append(toolNames, tool.Type)
		}
		if !h.authorizeTools(w, identity, toolNames, true) {
			return false
		}
	}
	return true
}

func (h Handler) validateAssistantMessageFile(w http.ResponseWriter, r *http.Request, identity modules.RequestContext, id string) bool {
	if !validFileToken(id, 128) || h.files == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "assistant message file is invalid or unavailable")
		return false
	}
	file, err := h.files.Get(r.Context(), fileOwnerKey(identity), id, false)
	if err != nil || file.Purpose != "assistants" {
		writeError(w, http.StatusBadRequest, "invalid_request", "assistant message file is unavailable")
		return false
	}
	return true
}

func validAssistantMessageText(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 1<<20
}

func validAssistantImageDetail(value string) bool {
	return value == "" || value == "auto" || value == "low" || value == "high"
}

func validAssistantImageURL(value string) bool {
	if len(value) < 1 || len(value) > 2<<20 {
		return false
	}
	if strings.HasPrefix(value, "data:image/") {
		header, encoded, found := strings.Cut(value, ",")
		if !found || encoded == "" || !strings.HasSuffix(header, ";base64") {
			return false
		}
		mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		if mediaType != "image/jpeg" && mediaType != "image/png" && mediaType != "image/gif" && mediaType != "image/webp" {
			return false
		}
		_, err := base64.StdEncoding.DecodeString(encoded)
		return err == nil
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func publicAssistantMessage(record assistantstate.MessageRecord) (map[string]any, error) {
	var snapshot assistantMessageSnapshot
	if json.Unmarshal(record.Snapshot, &snapshot) != nil {
		return nil, assistantstate.ErrUnavailable
	}
	payload, _ := json.Marshal(snapshot)
	var result map[string]any
	if json.Unmarshal(payload, &result) != nil {
		return nil, assistantstate.ErrUnavailable
	}
	result["id"], result["object"], result["created_at"], result["thread_id"] = record.ID, "thread.message", record.CreatedAt.Unix(), record.ThreadID
	result["assistant_id"], result["run_id"] = nil, nil
	if snapshot.AssistantID != "" {
		result["assistant_id"] = snapshot.AssistantID
	}
	if snapshot.RunID != "" {
		result["run_id"] = snapshot.RunID
	}
	return result, nil
}

func (h Handler) writeAssistantMessage(w http.ResponseWriter, record assistantstate.MessageRecord) {
	value, err := publicAssistantMessage(record)
	if err != nil {
		writeAssistantMessageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func writeAssistantMessageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, assistantstate.ErrNotFound):
		writeError(w, http.StatusNotFound, "message_not_found", "thread or message not found")
	case errors.Is(err, assistantstate.ErrQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "message_quota_exceeded", "message quota exceeded")
	case errors.Is(err, assistantstate.ErrConflict):
		writeError(w, http.StatusConflict, "message_conflict", "message was modified concurrently")
	case errors.Is(err, assistantstate.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid message request")
	default:
		writeError(w, http.StatusServiceUnavailable, "assistant_message_storage_unavailable", "assistant message storage is unavailable")
	}
}
