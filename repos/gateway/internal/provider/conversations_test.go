package provider

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/conversationstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type memoryConversationStore struct {
	mu        sync.Mutex
	turn      conversationstate.Turn
	completed []conversationstate.Item
	released  bool
}

func (s *memoryConversationStore) CreateConversation(context.Context, conversationstate.Conversation, []conversationstate.Item, int, int) (conversationstate.Conversation, []conversationstate.Item, error) {
	panic("not used")
}
func (s *memoryConversationStore) GetConversation(context.Context, string, string) (conversationstate.Conversation, error) {
	panic("not used")
}
func (s *memoryConversationStore) UpdateConversation(context.Context, string, string, []byte, int64) (conversationstate.Conversation, error) {
	panic("not used")
}
func (s *memoryConversationStore) DeleteConversation(context.Context, string, string) error {
	panic("not used")
}
func (s *memoryConversationStore) CreateItems(context.Context, string, string, []conversationstate.Item, int) ([]conversationstate.Item, error) {
	panic("not used")
}
func (s *memoryConversationStore) ListItems(context.Context, string, string, conversationstate.PageOptions) ([]conversationstate.Item, bool, error) {
	panic("not used")
}
func (s *memoryConversationStore) GetItem(context.Context, string, string, string) (conversationstate.Item, error) {
	panic("not used")
}
func (s *memoryConversationStore) DeleteItem(context.Context, string, string, string) error {
	panic("not used")
}
func (s *memoryConversationStore) BeginTurn(_ context.Context, owner, id, execution string, _ time.Duration) (conversationstate.Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn.Conversation.ID != id || s.turn.Conversation.OwnerKey != owner {
		return conversationstate.Turn{}, conversationstate.ErrNotFound
	}
	if s.turn.ExecutionID != "" {
		return conversationstate.Turn{}, conversationstate.ErrConflict
	}
	s.turn.ExecutionID = execution
	s.released = false
	return s.turn, nil
}
func (s *memoryConversationStore) CompleteTurn(_ context.Context, turn conversationstate.Turn, items []conversationstate.Item, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn.ExecutionID != turn.ExecutionID {
		return conversationstate.ErrConflict
	}
	s.completed = append([]conversationstate.Item(nil), items...)
	s.turn.ExecutionID = ""
	return nil
}
func (s *memoryConversationStore) ReleaseTurn(_ context.Context, turn conversationstate.Turn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn.ExecutionID != turn.ExecutionID {
		return conversationstate.ErrConflict
	}
	s.turn.ExecutionID = ""
	s.released = true
	return nil
}

type conversationCaptureClient struct {
	input any
	err   error
}

func (*conversationCaptureClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}
func (c *conversationCaptureClient) Responses(_ context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	c.input = request.Input
	if c.err != nil {
		return openai.ResponseResponse{}, c.err
	}
	return openai.ResponseResponse{ID: "resp_1", Object: "response", Model: request.Model, Status: "completed", Output: []openai.ResponseOutputItem{{ID: "msg_assistant", Type: "message", Role: "assistant", Content: []openai.ResponseOutputContent{{Type: "output_text", Text: "answer"}}}}}, nil
}

type conversationPostModule struct {
	store       *memoryConversationStore
	seen        bool
	inputTokens int
}

func (*conversationPostModule) Name() string                                          { return "conversation-order" }
func (*conversationPostModule) Required() bool                                        { return true }
func (*conversationPostModule) PostResponseEnabled() bool                             { return true }
func (m *conversationPostModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.inputTokens = openai.ResponseInputTokens(*req.ResponseRequest)
	return nil
}
func (m *conversationPostModule) HandlePostResponse(context.Context, *modules.RequestContext) error {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	if len(m.store.completed) == 0 {
		return errors.New("conversation was not committed before post-response")
	}
	m.seen = true
	return nil
}

func TestResponsesConversationExpandsAndCommitsBeforePostResponse(t *testing.T) {
	owner := conversationstate.OwnerKey("credential", "user")
	store := &memoryConversationStore{turn: conversationstate.Turn{Conversation: conversationstate.Conversation{ID: "conv_test", OwnerKey: owner}, Items: []conversationstate.Item{{ID: "msg_old", ConversationID: "conv_test", OwnerKey: owner, Payload: []byte(`{"id":"msg_old","type":"message","role":"user","content":[{"type":"input_text","text":"history"}]}`)}}}}
	client := &conversationCaptureClient{}
	post := &conversationPostModule{store: store}
	router := Router{endpoints: []Endpoint{{Name: "endpoint", Type: "demo", Provider: client}}, modules: modules.NewPipeline([]modules.Module{post}), health: newEndpointHealthTracker(), cache: newExactCache(time.Hour), conversations: store}
	request := openai.ResponseRequest{Model: "model", Input: "current", Conversation: &openai.ResponseConversation{ID: "conv_test"}}
	response, err := router.Responses(t.Context(), modules.RequestContext{RequestID: "execution", CredentialID: "credential", UserID: "user", Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(client.input)
	var expanded []json.RawMessage
	if json.Unmarshal(encoded, &expanded) != nil || len(expanded) != 2 || !post.seen {
		t.Fatalf("expanded=%s post=%v", encoded, post.seen)
	}
	if response.Conversation == nil || response.Conversation.ID != "conv_test" || len(store.completed) != 2 || post.inputTokens <= openai.ResponseInputTokens(request) {
		t.Fatalf("response=%+v committed=%d input_tokens=%d", response, len(store.completed), post.inputTokens)
	}
	if responseReplaySafe(request) {
		t.Fatal("conversation request must bypass replay cache and shadowing")
	}
}

func TestResponsesConversationReleasesTurnOnProviderFailure(t *testing.T) {
	owner := conversationstate.OwnerKey("credential", "user")
	store := &memoryConversationStore{turn: conversationstate.Turn{Conversation: conversationstate.Conversation{ID: "conv_test", OwnerKey: owner}}}
	client := &conversationCaptureClient{err: errors.New("upstream failed")}
	router := Router{endpoints: []Endpoint{{Name: "endpoint", Type: "demo", Provider: client}}, modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), cache: newExactCache(time.Hour), conversations: store}
	request := openai.ResponseRequest{Model: "model", Input: "current", Conversation: &openai.ResponseConversation{ID: "conv_test"}}
	if _, err := router.Responses(t.Context(), modules.RequestContext{RequestID: "execution", CredentialID: "credential", UserID: "user", Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request}); err == nil {
		t.Fatal("expected provider failure")
	}
	if !store.released {
		t.Fatal("conversation turn was not released")
	}
}
