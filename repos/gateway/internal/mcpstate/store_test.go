package mcpstate

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreClaimCompleteReplayAndConflict(t *testing.T) {
	store := NewMemoryStore(2, time.Hour)
	claim := Record{ScopeKey: "credential/server/tool", IdempotencyKey: "key", RequestHash: "hash", ExecutionID: "execution"}
	created, owner, err := store.Claim(context.Background(), claim)
	if err != nil || !owner || created.State != StatePending {
		t.Fatalf("claim=%+v owner=%v err=%v", created, owner, err)
	}
	if _, _, err := store.Claim(context.Background(), Record{ScopeKey: claim.ScopeKey, IdempotencyKey: claim.IdempotencyKey, RequestHash: "other", ExecutionID: "other"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting payload error=%v", err)
	}
	payload := json.RawMessage(`{"content":[{"type":"text","text":"sunny"}]}`)
	if err := store.Complete(context.Background(), claim.ScopeKey, claim.IdempotencyKey, claim.ExecutionID, 200, payload); err != nil {
		t.Fatal(err)
	}
	replayed, owner, err := store.Claim(context.Background(), claim)
	if err != nil || owner || replayed.State != StateCompleted || replayed.HTTPStatus != 200 || string(replayed.Response) != string(payload) {
		t.Fatalf("replay=%+v owner=%v err=%v", replayed, owner, err)
	}
}

func TestMemoryStoreBoundsAndExpiresEntries(t *testing.T) {
	now := time.Unix(100, 0)
	store := NewMemoryStore(1, time.Minute)
	store.now = func() time.Time { return now }
	first := Record{ScopeKey: "scope", IdempotencyKey: "one", RequestHash: "hash", ExecutionID: "one"}
	if _, _, err := store.Claim(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Claim(context.Background(), Record{ScopeKey: "scope", IdempotencyKey: "two", RequestHash: "hash", ExecutionID: "two"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("capacity error=%v", err)
	}
	now = now.Add(time.Minute)
	if _, owner, err := store.Claim(context.Background(), Record{ScopeKey: "scope", IdempotencyKey: "two", RequestHash: "hash", ExecutionID: "two"}); err != nil || !owner {
		t.Fatalf("expired claim owner=%v err=%v", owner, err)
	}
}

func TestMemoryStoreReleaseOnlyPendingOwner(t *testing.T) {
	store := NewMemoryStore(1, time.Hour)
	claim := Record{ScopeKey: "scope", IdempotencyKey: "key", RequestHash: "hash", ExecutionID: "execution"}
	_, _, _ = store.Claim(context.Background(), claim)
	if err := store.Release(context.Background(), claim.ScopeKey, claim.IdempotencyKey, "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-owner release error=%v", err)
	}
	if err := store.Release(context.Background(), claim.ScopeKey, claim.IdempotencyKey, claim.ExecutionID); err != nil {
		t.Fatal(err)
	}
	if _, owner, err := store.Claim(context.Background(), claim); err != nil || !owner {
		t.Fatalf("released claim owner=%v err=%v", owner, err)
	}
}
