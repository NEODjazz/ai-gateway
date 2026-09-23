package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/conversationstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type gatewayConversationStore struct {
	conversation conversationstate.Conversation
	items        []conversationstate.Item
}

type conversationTPMProvider struct {
	prepared bool
	called   bool
}

func (*conversationTPMProvider) ChatCompletions(context.Context, modules.RequestContext) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}
func (*conversationTPMProvider) StreamChatCompletions(context.Context, modules.RequestContext, provider.ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	return openai.ChatCompletionResponse{}, false, nil
}
func (p *conversationTPMProvider) Responses(context.Context, modules.RequestContext) (openai.ResponseResponse, error) {
	p.called = true
	return openai.ResponseResponse{}, nil
}
func (*conversationTPMProvider) StreamResponses(context.Context, modules.RequestContext, provider.ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	return openai.ResponseResponse{}, false, nil
}
func (*conversationTPMProvider) Models() []openai.Model { return nil }
func (p *conversationTPMProvider) PrepareConversation(_ context.Context, req modules.RequestContext) (modules.RequestContext, error) {
	p.prepared = true
	request := *req.ResponseRequest
	request.Conversation = nil
	request.Input = []any{
		map[string]any{"type": "message", "role": "user", "content": strings.Repeat("history ", 100)},
		map[string]any{"type": "message", "role": "user", "content": "current"},
	}
	req.ResponseRequest = &request
	return req, nil
}
func (*conversationTPMProvider) ReleaseConversation(context.Context, *modules.RequestContext) {}

type conversationTPMAuth struct{}

func (*conversationTPMAuth) Name() string   { return "auth" }
func (*conversationTPMAuth) Required() bool { return true }
func (*conversationTPMAuth) Handle(_ context.Context, req *modules.RequestContext) error {
	req.CredentialID = "credential"
	req.RateLimitTPM = 10
	return nil
}

func (s *gatewayConversationStore) CreateConversation(_ context.Context, value conversationstate.Conversation, items []conversationstate.Item, _, _ int) (conversationstate.Conversation, []conversationstate.Item, error) {
	value.Revision = 1
	value.CreatedAt = time.Unix(100, 0)
	s.conversation = value
	s.items = append([]conversationstate.Item(nil), items...)
	return value, items, nil
}
func (s *gatewayConversationStore) GetConversation(_ context.Context, owner, id string) (conversationstate.Conversation, error) {
	if s.conversation.OwnerKey != owner || s.conversation.ID != id {
		return conversationstate.Conversation{}, conversationstate.ErrNotFound
	}
	return s.conversation, nil
}
func (s *gatewayConversationStore) UpdateConversation(_ context.Context, owner, id string, metadata []byte, revision int64) (conversationstate.Conversation, error) {
	if s.conversation.OwnerKey != owner || s.conversation.ID != id {
		return conversationstate.Conversation{}, conversationstate.ErrNotFound
	}
	if revision != s.conversation.Revision {
		return conversationstate.Conversation{}, conversationstate.ErrConflict
	}
	s.conversation.Metadata = append([]byte(nil), metadata...)
	s.conversation.Revision++
	return s.conversation, nil
}
func (s *gatewayConversationStore) DeleteConversation(_ context.Context, owner, id string) error {
	if s.conversation.OwnerKey != owner || s.conversation.ID != id {
		return conversationstate.ErrNotFound
	}
	s.conversation = conversationstate.Conversation{}
	s.items = nil
	return nil
}
func (s *gatewayConversationStore) CreateItems(_ context.Context, owner, id string, items []conversationstate.Item, _ int) ([]conversationstate.Item, error) {
	if s.conversation.OwnerKey != owner || s.conversation.ID != id {
		return nil, conversationstate.ErrNotFound
	}
	s.items = append(s.items, items...)
	return items, nil
}
func (s *gatewayConversationStore) ListItems(_ context.Context, owner, id string, options conversationstate.PageOptions) ([]conversationstate.Item, bool, error) {
	if s.conversation.OwnerKey != owner || s.conversation.ID != id {
		return nil, false, conversationstate.ErrNotFound
	}
	items := append([]conversationstate.Item(nil), s.items...)
	if options.Order == "desc" {
		for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
			items[left], items[right] = items[right], items[left]
		}
	}
	return items, false, nil
}
func (s *gatewayConversationStore) GetItem(_ context.Context, owner, id, itemID string) (conversationstate.Item, error) {
	for _, item := range s.items {
		if item.OwnerKey == owner && item.ConversationID == id && item.ID == itemID {
			return item, nil
		}
	}
	return conversationstate.Item{}, conversationstate.ErrNotFound
}
func (s *gatewayConversationStore) DeleteItem(_ context.Context, owner, id, itemID string) error {
	for index, item := range s.items {
		if item.OwnerKey == owner && item.ConversationID == id && item.ID == itemID {
			s.items = append(s.items[:index], s.items[index+1:]...)
			return nil
		}
	}
	return conversationstate.ErrNotFound
}
func (*gatewayConversationStore) BeginTurn(context.Context, string, string, string, time.Duration) (conversationstate.Turn, error) {
	panic("not used")
}
func (*gatewayConversationStore) CompleteTurn(context.Context, conversationstate.Turn, []conversationstate.Item, int) error {
	panic("not used")
}
func (*gatewayConversationStore) ReleaseTurn(context.Context, conversationstate.Turn) error {
	panic("not used")
}

func TestConversationCRUDRoutesAreOwnerScoped(t *testing.T) {
	store := &gatewayConversationStore{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential-a", user: "user-a"}}), nil).
		WithConversationStore(store, ConversationRuntimeConfig{OwnerQuota: 10, ItemQuota: 10}))
	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/conversations", strings.NewReader(`{"metadata":{"topic":"support"},"items":[{"type":"message","role":"user","content":"hello"}]}`)))
	if create.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var created map[string]any
	if json.Unmarshal(create.Body.Bytes(), &created) != nil || !strings.HasPrefix(created["id"].(string), "conv_") || len(store.items) != 1 {
		t.Fatalf("created=%v items=%+v", created, store.items)
	}
	id := created["id"].(string)
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/conversations/"+id+"/items?order=asc&limit=10", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"has_more":false`) || !strings.Contains(list.Body.String(), `"type":"message"`) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}

	other := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential-b", user: "user-a"}}), nil).
		WithConversationStore(store, ConversationRuntimeConfig{OwnerQuota: 10, ItemQuota: 10}))
	get := httptest.NewRecorder()
	other.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/conversations/"+id, nil))
	if get.Code != http.StatusNotFound {
		t.Fatalf("cross-owner status=%d body=%s", get.Code, get.Body.String())
	}
}

func TestConversationHistoryIsIncludedInGatewayTPMAdmission(t *testing.T) {
	upstream := &conversationTPMProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{&conversationTPMAuth{}}), upstream)
	response := httptest.NewRecorder()
	handler.Responses(response, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","input":"x","conversation":"conv_test","max_output_tokens":1}`)))
	if response.Code != http.StatusTooManyRequests || !upstream.prepared || upstream.called {
		t.Fatalf("status=%d prepared=%v called=%v body=%s", response.Code, upstream.prepared, upstream.called, response.Body.String())
	}
}
