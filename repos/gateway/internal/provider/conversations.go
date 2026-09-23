package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ai-gateway-gateway/internal/conversationstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

const conversationTurnLease = 15 * time.Minute
const defaultConversationItemQuota = 4096

var ErrConversationNotFound = errors.New("conversation not found")
var ErrConversationConflict = errors.New("conversation has another active request")
var ErrConversationStorageUnavailable = errors.New("conversation storage is unavailable")

func (r Router) prepareConversation(ctx context.Context, req modules.RequestContext) (modules.RequestContext, error) {
	if req.ResponseRequest == nil || req.ResponseRequest.Conversation == nil {
		return req, nil
	}
	if req.ConversationTurn != nil {
		return req, nil
	}
	if interfaceIsNil(r.conversations) || req.CredentialID == "" {
		return req, ErrConversationStorageUnavailable
	}
	turn, err := r.conversations.BeginTurn(ctx, conversationstate.OwnerKey(req.CredentialID, req.UserID), req.ResponseRequest.Conversation.ID, req.RequestID, conversationTurnLease)
	if err != nil {
		return req, conversationError(err)
	}
	current, err := conversationInputItems(req.ResponseRequest.Input, turn.Conversation.OwnerKey, turn.Conversation.ID)
	if err != nil {
		_ = r.conversations.ReleaseTurn(ctx, turn)
		return req, err
	}
	request := *req.ResponseRequest
	request.Input = combinedConversationInput(turn.Items, current)
	request.Conversation = nil
	req.ResponseRequest = &request
	req.ConversationTurn = &turn
	req.ConversationInputItems = current
	return req, nil
}

func (r Router) PrepareConversation(ctx context.Context, req modules.RequestContext) (modules.RequestContext, error) {
	return r.prepareConversation(ctx, req)
}

func (r Router) ReleaseConversation(ctx context.Context, req *modules.RequestContext) {
	r.releaseConversation(ctx, req)
}

func (r Router) completeConversation(ctx context.Context, req *modules.RequestContext, response *openai.ResponseResponse) error {
	if req == nil || req.ConversationTurn == nil {
		return nil
	}
	items := append([]conversationstate.Item(nil), req.ConversationInputItems...)
	for _, output := range response.Output {
		id, payload, err := conversationOutputPayload(output)
		if err != nil {
			return err
		}
		items = append(items, conversationstate.Item{ID: id, ConversationID: req.ConversationTurn.Conversation.ID, OwnerKey: req.ConversationTurn.Conversation.OwnerKey, Payload: payload})
	}
	if len(response.Output) == 0 && response.OutputText != "" {
		item := openai.ResponseOutputItem{ID: responseItemID(""), Type: "message", Role: "assistant", Content: []openai.ResponseOutputContent{{Type: "output_text", Text: response.OutputText}}}
		payload, _ := json.Marshal(item)
		items = append(items, conversationstate.Item{ID: item.ID, ConversationID: req.ConversationTurn.Conversation.ID, OwnerKey: req.ConversationTurn.Conversation.OwnerKey, Payload: payload})
	}
	if len(items) == len(req.ConversationInputItems) {
		return errors.New("provider returned no conversation output items")
	}
	if err := r.conversations.CompleteTurn(ctx, *req.ConversationTurn, items, r.conversationItemLimit()); err != nil {
		return conversationError(err)
	}
	response.Conversation = &openai.ResponseConversation{ID: req.ConversationTurn.Conversation.ID}
	req.ConversationTurn = nil
	return nil
}

func (r Router) conversationItemLimit() int {
	if r.conversationItemQuota > 0 {
		return r.conversationItemQuota
	}
	return defaultConversationItemQuota
}

func (r Router) releaseConversation(ctx context.Context, req *modules.RequestContext) {
	if req == nil || req.ConversationTurn == nil || interfaceIsNil(r.conversations) {
		return
	}
	_ = r.conversations.ReleaseTurn(ctx, *req.ConversationTurn)
	req.ConversationTurn = nil
}

func conversationError(err error) error {
	switch {
	case errors.Is(err, conversationstate.ErrNotFound):
		return ErrConversationNotFound
	case errors.Is(err, conversationstate.ErrConflict), errors.Is(err, conversationstate.ErrQuotaExceeded):
		return errors.Join(ErrConversationConflict, err)
	case errors.Is(err, conversationstate.ErrUnavailable):
		return ErrConversationStorageUnavailable
	default:
		return fmt.Errorf("conversation storage failed: %w", err)
	}
}

func conversationInputItems(input any, owner, conversationID string) ([]conversationstate.Item, error) {
	var payloads []json.RawMessage
	if text, ok := input.(string); ok {
		payload, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}})
		payloads = []json.RawMessage{payload}
	} else {
		encoded, err := json.Marshal(input)
		if err != nil || json.Unmarshal(encoded, &payloads) != nil || len(payloads) == 0 {
			return nil, errors.New("conversation input must be a string or non-empty array of objects")
		}
	}
	items := make([]conversationstate.Item, 0, len(payloads))
	for _, payload := range payloads {
		var object map[string]json.RawMessage
		if json.Unmarshal(payload, &object) != nil || object == nil {
			return nil, errors.New("conversation input entries must be objects")
		}
		id := ""
		_ = json.Unmarshal(object["id"], &id)
		id = responseItemID(id)
		object["id"], _ = json.Marshal(id)
		normalized, err := json.Marshal(object)
		if err != nil || len(normalized) > conversationstate.MaxItemBytes {
			return nil, errors.New("conversation input item exceeds its size limit")
		}
		items = append(items, conversationstate.Item{ID: id, ConversationID: conversationID, OwnerKey: owner, Payload: normalized})
	}
	return items, nil
}

func combinedConversationInput(history, current []conversationstate.Item) []json.RawMessage {
	result := make([]json.RawMessage, 0, len(history)+len(current))
	for _, item := range history {
		result = append(result, append(json.RawMessage(nil), item.Payload...))
	}
	for _, item := range current {
		result = append(result, append(json.RawMessage(nil), item.Payload...))
	}
	return result
}

func conversationOutputPayload(output openai.ResponseOutputItem) (string, []byte, error) {
	output.ID = responseItemID(output.ID)
	payload, err := json.Marshal(output)
	if err != nil || len(payload) > conversationstate.MaxItemBytes {
		return "", nil, errors.New("conversation output item exceeds its size limit")
	}
	return output.ID, payload, nil
}

func responseItemID(value string) string {
	if value != "" && len(value) <= 128 {
		return value
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("item_%d", time.Now().UnixNano())
	}
	return "item_" + hex.EncodeToString(random[:])
}
