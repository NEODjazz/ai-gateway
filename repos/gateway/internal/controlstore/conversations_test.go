package controlstore

import (
	"errors"
	"testing"
	"time"

	"ai-gateway-gateway/internal/conversationstate"
)

func TestPostgresConversationLifecycleAndTurnIsolationIntegration(t *testing.T) {
	store := prepareConversationStore(t)
	owner := "owner-a"
	conversation := conversationstate.Conversation{ID: "conv_a", OwnerKey: owner, Metadata: []byte(`{"topic":"support"}`)}
	initial := []conversationstate.Item{{ID: "item_a", ConversationID: conversation.ID, OwnerKey: owner, Payload: []byte(`{"id":"item_a","type":"message","role":"user"}`)}}
	created, createdItems, err := store.CreateConversation(t.Context(), conversation, initial, 2, 3)
	if err != nil || created.Revision != 1 || len(createdItems) != 1 || createdItems[0].Ordinal < 1 {
		t.Fatalf("conversation=%+v items=%+v err=%v", created, createdItems, err)
	}
	if _, err := store.GetConversation(t.Context(), "owner-b", conversation.ID); !errors.Is(err, conversationstate.ErrNotFound) {
		t.Fatalf("cross-owner read err=%v", err)
	}
	turn, err := store.BeginTurn(t.Context(), owner, conversation.ID, "execution-a", time.Minute)
	if err != nil || len(turn.Items) != 1 {
		t.Fatalf("turn=%+v err=%v", turn, err)
	}
	if _, err := store.BeginTurn(t.Context(), owner, conversation.ID, "execution-b", time.Minute); !errors.Is(err, conversationstate.ErrConflict) {
		t.Fatalf("parallel turn err=%v", err)
	}
	if _, err := store.CreateItems(t.Context(), owner, conversation.ID, []conversationstate.Item{{ID: "blocked", ConversationID: conversation.ID, OwnerKey: owner, Payload: []byte(`{"id":"blocked","type":"message"}`)}}, 3); !errors.Is(err, conversationstate.ErrConflict) {
		t.Fatalf("mutation during turn err=%v", err)
	}
	completion := []conversationstate.Item{
		{ID: "item_b", ConversationID: conversation.ID, OwnerKey: owner, Payload: []byte(`{"id":"item_b","type":"message","role":"user"}`)},
		{ID: "item_c", ConversationID: conversation.ID, OwnerKey: owner, Payload: []byte(`{"id":"item_c","type":"message","role":"assistant"}`)},
	}
	if err := store.CompleteTurn(t.Context(), turn, completion, 3); err != nil {
		t.Fatal(err)
	}
	page, more, err := store.ListItems(t.Context(), owner, conversation.ID, conversationstate.PageOptions{Limit: 2, Order: "asc"})
	if err != nil || len(page) != 2 || !more || page[0].ID != "item_a" || page[1].ID != "item_b" {
		t.Fatalf("first page=%+v more=%v err=%v", page, more, err)
	}
	rest, more, err := store.ListItems(t.Context(), owner, conversation.ID, conversationstate.PageOptions{Limit: 2, After: page[1].ID, Order: "asc"})
	if err != nil || len(rest) != 1 || more || rest[0].ID != "item_c" {
		t.Fatalf("rest=%+v more=%v err=%v", rest, more, err)
	}
	if err := store.ReleaseTurn(t.Context(), turn); !errors.Is(err, conversationstate.ErrConflict) {
		t.Fatalf("completed turn released twice: %v", err)
	}
	if err := store.DeleteConversation(t.Context(), owner, conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetItem(t.Context(), owner, conversation.ID, "item_a"); !errors.Is(err, conversationstate.ErrNotFound) {
		t.Fatalf("delete did not cascade: %v", err)
	}
}

func TestPostgresConversationCompletionIsAtomicAtQuotaIntegration(t *testing.T) {
	store := prepareConversationStore(t)
	conversation := conversationstate.Conversation{ID: "conv_quota", OwnerKey: "owner", Metadata: []byte(`{}`)}
	if _, _, err := store.CreateConversation(t.Context(), conversation, nil, 1, 1); err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurn(t.Context(), conversation.OwnerKey, conversation.ID, "execution", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	items := []conversationstate.Item{
		{ID: "one", ConversationID: conversation.ID, OwnerKey: conversation.OwnerKey, Payload: []byte(`{"id":"one","type":"message"}`)},
		{ID: "two", ConversationID: conversation.ID, OwnerKey: conversation.OwnerKey, Payload: []byte(`{"id":"two","type":"message"}`)},
	}
	if err := store.CompleteTurn(t.Context(), turn, items, 1); !errors.Is(err, conversationstate.ErrQuotaExceeded) {
		t.Fatalf("quota err=%v", err)
	}
	listed, _, err := store.ListItems(t.Context(), conversation.OwnerKey, conversation.ID, conversationstate.PageOptions{Limit: 10, Order: "asc"})
	if err != nil || len(listed) != 0 {
		t.Fatalf("partial completion persisted: items=%+v err=%v", listed, err)
	}
	if err := store.ReleaseTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresBackgroundConversationTurnIsDurableAndIdempotentIntegration(t *testing.T) {
	store := prepareConversationStore(t)
	conversation := conversationstate.Conversation{ID: "conv_background", OwnerKey: "owner", Metadata: []byte(`{}`)}
	if _, _, err := store.CreateConversation(t.Context(), conversation, nil, 1, 4); err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurn(t.Context(), conversation.OwnerKey, conversation.ID, "execution-background", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input := []conversationstate.Item{{ID: "input", ConversationID: conversation.ID, OwnerKey: conversation.OwnerKey, Payload: []byte(`{"id":"input","type":"message","role":"user"}`)}}
	if err := store.StageTurn(t.Context(), turn, input, 4, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE gateway_conversations SET lease_until=now()-interval '1 minute' WHERE owner_key=$1 AND id=$2`, conversation.OwnerKey, conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginTurn(t.Context(), conversation.OwnerKey, conversation.ID, "execution-other", time.Minute); !errors.Is(err, conversationstate.ErrConflict) {
		t.Fatalf("durable turn replaced after lease expiry: %v", err)
	}
	if _, err := store.UpdateConversation(t.Context(), conversation.OwnerKey, conversation.ID, []byte(`{"changed":true}`), 1); !errors.Is(err, conversationstate.ErrConflict) {
		t.Fatalf("durable turn allowed mutation: %v", err)
	}
	output := []conversationstate.Item{{ID: "output", ConversationID: conversation.ID, OwnerKey: conversation.OwnerKey, Payload: []byte(`{"id":"output","type":"message","role":"assistant"}`)}}
	if err := store.CompleteTurn(t.Context(), turn, output, 4); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteTurn(t.Context(), turn, output, 4); err != nil {
		t.Fatalf("retrying committed turn: %v", err)
	}
	next, err := store.BeginTurn(t.Context(), conversation.OwnerKey, conversation.ID, "execution-next", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteTurn(t.Context(), turn, output, 4); err != nil {
		t.Fatalf("retrying committed turn while next turn is active: %v", err)
	}
	if err := store.ReleaseTurn(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	items, more, err := store.ListItems(t.Context(), conversation.OwnerKey, conversation.ID, conversationstate.PageOptions{Limit: 10, Order: "asc"})
	if err != nil || more || len(items) != 2 || items[0].ID != "input" || items[1].ID != "output" {
		t.Fatalf("items=%+v more=%v err=%v", items, more, err)
	}
}

func TestPostgresBackgroundConversationReleaseRemovesPendingItemsIntegration(t *testing.T) {
	store := prepareConversationStore(t)
	conversation := conversationstate.Conversation{ID: "conv_release", OwnerKey: "owner", Metadata: []byte(`{}`)}
	if _, _, err := store.CreateConversation(t.Context(), conversation, nil, 1, 2); err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurn(t.Context(), conversation.OwnerKey, conversation.ID, "execution-release", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input := []conversationstate.Item{{ID: "input", ConversationID: conversation.ID, OwnerKey: conversation.OwnerKey, Payload: []byte(`{"id":"input","type":"message"}`)}}
	if err := store.StageTurn(t.Context(), turn, input, 1, 1); !errors.Is(err, conversationstate.ErrQuotaExceeded) {
		t.Fatalf("missing output reservation quota error: %v", err)
	}
	if err := store.StageTurn(t.Context(), turn, input, 2, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseTurn(t.Context(), turn); err != nil {
		t.Fatalf("retrying released turn: %v", err)
	}
	items, _, err := store.ListItems(t.Context(), conversation.OwnerKey, conversation.ID, conversationstate.PageOptions{Limit: 10, Order: "asc"})
	if err != nil || len(items) != 0 {
		t.Fatalf("released pending items leaked: items=%+v err=%v", items, err)
	}
	if _, err := store.BeginTurn(t.Context(), conversation.OwnerKey, conversation.ID, "execution-next", time.Minute); err != nil {
		t.Fatalf("released conversation remained locked: %v", err)
	}
}

func prepareConversationStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	_, err = store.pool.Exec(t.Context(), `CREATE TABLE gateway_conversations (
		id TEXT NOT NULL,owner_key TEXT NOT NULL,metadata JSONB NOT NULL,revision BIGINT NOT NULL DEFAULT 1,
		active_execution_id TEXT,lease_until TIMESTAMPTZ,durable_active BOOLEAN NOT NULL DEFAULT false,
		last_execution_id TEXT,last_execution_committed BOOLEAN,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(owner_key,id));
		CREATE TABLE gateway_conversation_items (
		id TEXT NOT NULL,conversation_id TEXT NOT NULL,owner_key TEXT NOT NULL,payload JSONB NOT NULL,ordinal BIGSERIAL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY(owner_key,conversation_id,id),UNIQUE(owner_key,conversation_id,ordinal),
		FOREIGN KEY(owner_key,conversation_id) REFERENCES gateway_conversations(owner_key,id) ON DELETE CASCADE);
		CREATE TABLE gateway_conversation_pending_items (
		execution_id TEXT NOT NULL,id TEXT NOT NULL,conversation_id TEXT NOT NULL,owner_key TEXT NOT NULL,payload JSONB NOT NULL,position INTEGER NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY(owner_key,conversation_id,execution_id,id),UNIQUE(owner_key,conversation_id,execution_id,position),
		FOREIGN KEY(owner_key,conversation_id) REFERENCES gateway_conversations(owner_key,id) ON DELETE CASCADE)`)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
