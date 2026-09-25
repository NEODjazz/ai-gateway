package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/conversationstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type ConversationRuntimeConfig struct {
	OwnerQuota int
	ItemQuota  int
}

type conversationCreateRequest struct {
	Metadata map[string]string `json:"metadata,omitempty"`
	Items    []json.RawMessage `json:"items,omitempty"`
}

type conversationUpdateRequest struct {
	Metadata *map[string]string `json:"metadata"`
}

type conversationItemsCreateRequest struct {
	Items []json.RawMessage `json:"items"`
}

func (h Handler) WithConversationStore(store conversationstate.Store, config ConversationRuntimeConfig) Handler {
	h.conversations = store
	h.conversationConfig = config
	return h
}

func (h Handler) CreateConversation(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.conversationIdentity(w, r)
	if !ok {
		return
	}
	var input conversationCreateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if message := openai.ValidateMetadata(input.Metadata); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	if len(input.Items) > 20 {
		writeError(w, http.StatusBadRequest, "invalid_request", "initial items must contain at most 20 entries")
		return
	}
	id, generated := newAssistantResourceID("conv_")
	if !generated {
		writeError(w, http.StatusInternalServerError, "conversation_id_failed", "conversation ID generation failed")
		return
	}
	owner := conversationstate.OwnerKey(identity.CredentialID, identity.UserID)
	items, valid := prepareConversationItems(w, owner, id, input.Items)
	if !valid {
		return
	}
	metadata, _ := json.Marshal(normalizedMetadata(input.Metadata))
	record, _, err := h.conversations.CreateConversation(r.Context(), conversationstate.Conversation{ID: id, OwnerKey: owner, Metadata: metadata}, items, h.conversationConfig.OwnerQuota, h.conversationConfig.ItemQuota)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeConversation(w, record)
}

func (h Handler) GetConversation(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := h.conversationResource(w, r)
	if !ok {
		return
	}
	record, err := h.conversations.GetConversation(r.Context(), conversationstate.OwnerKey(identity.CredentialID, identity.UserID), id)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeConversation(w, record)
}

func (h Handler) UpdateConversation(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := h.conversationResource(w, r)
	if !ok {
		return
	}
	var input conversationUpdateRequest
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
	owner := conversationstate.OwnerKey(identity.CredentialID, identity.UserID)
	record, err := h.conversations.GetConversation(r.Context(), owner, id)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	metadata, _ := json.Marshal(normalizedMetadata(*input.Metadata))
	record, err = h.conversations.UpdateConversation(r.Context(), owner, id, metadata, record.Revision)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeConversation(w, record)
}

func (h Handler) DeleteConversation(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := h.conversationResource(w, r)
	if !ok {
		return
	}
	if err := h.conversations.DeleteConversation(r.Context(), conversationstate.OwnerKey(identity.CredentialID, identity.UserID), id); err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "conversation.deleted", "deleted": true})
}

func (h Handler) CreateConversationItems(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := h.conversationResource(w, r)
	if !ok {
		return
	}
	var input conversationItemsCreateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if len(input.Items) < 1 || len(input.Items) > 20 {
		writeError(w, http.StatusBadRequest, "invalid_request", "items must contain between 1 and 20 entries")
		return
	}
	owner := conversationstate.OwnerKey(identity.CredentialID, identity.UserID)
	items, valid := prepareConversationItems(w, owner, id, input.Items)
	if !valid {
		return
	}
	created, err := h.conversations.CreateItems(r.Context(), owner, id, items, h.conversationConfig.ItemQuota)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeConversationItemList(w, created, false)
}

func (h Handler) ListConversationItems(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := h.conversationResource(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	if len(query) > 3 {
		writeError(w, http.StatusBadRequest, "invalid_request", "unsupported query parameter")
		return
	}
	limit := 20
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	order := query.Get("order")
	if order == "" {
		order = "desc"
	}
	if order != "asc" && order != "desc" {
		writeError(w, http.StatusBadRequest, "invalid_request", "order must be asc or desc")
		return
	}
	for key := range query {
		if key != "after" && key != "limit" && key != "order" {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported query parameter")
			return
		}
	}
	owner := conversationstate.OwnerKey(identity.CredentialID, identity.UserID)
	items, more, err := h.conversations.ListItems(r.Context(), owner, id, conversationstate.PageOptions{Limit: limit, After: query.Get("after"), Order: order})
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeConversationItemList(w, items, more)
}

func (h Handler) GetConversationItem(w http.ResponseWriter, r *http.Request) {
	identity, conversationID, itemID, ok := h.conversationItemResource(w, r)
	if !ok {
		return
	}
	item, err := h.conversations.GetItem(r.Context(), conversationstate.OwnerKey(identity.CredentialID, identity.UserID), conversationID, itemID)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(item.Payload))
}

func (h Handler) DeleteConversationItem(w http.ResponseWriter, r *http.Request) {
	identity, conversationID, itemID, ok := h.conversationItemResource(w, r)
	if !ok {
		return
	}
	if err := h.conversations.DeleteItem(r.Context(), conversationstate.OwnerKey(identity.CredentialID, identity.UserID), conversationID, itemID); err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": itemID, "object": "conversation.item.deleted", "deleted": true})
}

func (h Handler) conversationIdentity(w http.ResponseWriter, r *http.Request) (modules.RequestContext, bool) {
	identity, ok := h.authorizeOwnedStorageOperation(w, r, "conversations")
	if !ok {
		return modules.RequestContext{}, false
	}
	if h.conversations == nil || h.conversationConfig.OwnerQuota < 1 || h.conversationConfig.ItemQuota < 1 {
		writeError(w, http.StatusServiceUnavailable, "conversation_storage_unavailable", "conversation storage is unavailable")
		return modules.RequestContext{}, false
	}
	return identity, true
}

func (h Handler) conversationResource(w http.ResponseWriter, r *http.Request) (modules.RequestContext, string, bool) {
	identity, ok := h.conversationIdentity(w, r)
	if !ok {
		return modules.RequestContext{}, "", false
	}
	id := r.PathValue("conversation_id")
	if !validFileToken(id, 128) || !strings.HasPrefix(id, "conv_") {
		writeError(w, http.StatusBadRequest, "invalid_request", "conversation ID is invalid")
		return modules.RequestContext{}, "", false
	}
	return identity, id, true
}

func (h Handler) conversationItemResource(w http.ResponseWriter, r *http.Request) (modules.RequestContext, string, string, bool) {
	identity, conversationID, ok := h.conversationResource(w, r)
	if !ok {
		return modules.RequestContext{}, "", "", false
	}
	itemID := r.PathValue("item_id")
	if !validFileToken(itemID, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "conversation item ID is invalid")
		return modules.RequestContext{}, "", "", false
	}
	return identity, conversationID, itemID, true
}

func prepareConversationItems(w http.ResponseWriter, owner, conversationID string, values []json.RawMessage) ([]conversationstate.Item, bool) {
	items := make([]conversationstate.Item, 0, len(values))
	for _, value := range values {
		var object map[string]json.RawMessage
		if json.Unmarshal(value, &object) != nil || object == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "conversation items must be objects")
			return nil, false
		}
		var itemType string
		if json.Unmarshal(object["type"], &itemType) != nil || strings.TrimSpace(itemType) == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "conversation item type is required")
			return nil, false
		}
		var id string
		_ = json.Unmarshal(object["id"], &id)
		if id == "" {
			var generated bool
			id, generated = newAssistantResourceID("item_")
			if !generated {
				writeError(w, http.StatusInternalServerError, "conversation_item_id_failed", "conversation item ID generation failed")
				return nil, false
			}
		}
		if !validFileToken(id, 128) {
			writeError(w, http.StatusBadRequest, "invalid_request", "conversation item ID is invalid")
			return nil, false
		}
		object["id"], _ = json.Marshal(id)
		payload, err := json.Marshal(object)
		if err != nil || len(payload) > conversationstate.MaxItemBytes {
			writeError(w, http.StatusBadRequest, "invalid_request", "conversation item exceeds its size limit")
			return nil, false
		}
		items = append(items, conversationstate.Item{ID: id, ConversationID: conversationID, OwnerKey: owner, Payload: payload})
	}
	return items, true
}

func writeConversation(w http.ResponseWriter, record conversationstate.Conversation) {
	var metadata map[string]string
	if json.Unmarshal(record.Metadata, &metadata) != nil {
		writeConversationError(w, conversationstate.ErrUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": record.ID, "object": "conversation", "created_at": record.CreatedAt.Unix(), "metadata": metadata})
}

func writeConversationItemList(w http.ResponseWriter, items []conversationstate.Item, hasMore bool) {
	data := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		data = append(data, append(json.RawMessage(nil), item.Payload...))
	}
	result := map[string]any{"object": "list", "data": data, "has_more": hasMore}
	if len(items) > 0 {
		result["first_id"], result["last_id"] = items[0].ID, items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, result)
}

func writeConversationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conversationstate.ErrNotFound):
		writeError(w, http.StatusNotFound, "conversation_not_found", "conversation or item not found")
	case errors.Is(err, conversationstate.ErrQuotaExceeded):
		writeError(w, http.StatusConflict, "conversation_quota_exceeded", "conversation quota exceeded")
	case errors.Is(err, conversationstate.ErrConflict):
		writeError(w, http.StatusConflict, "conversation_conflict", "conversation has another active request or revision")
	case errors.Is(err, conversationstate.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid conversation request")
	default:
		writeError(w, http.StatusServiceUnavailable, "conversation_storage_unavailable", "conversation storage is unavailable")
	}
}
