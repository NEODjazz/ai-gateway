package controlstore

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"ai-gateway-gateway/internal/assistantstate"
)

func TestPostgresAssistantOwnershipQuotaPaginationAndRevisionIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err = store.pool.Exec(t.Context(), `CREATE TABLE gateway_assistants (
		id TEXT NOT NULL,owner_key TEXT NOT NULL,snapshot JSONB NOT NULL,revision BIGINT NOT NULL DEFAULT 1,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(owner_key,id))`); err != nil {
		t.Fatal(err)
	}

	first, err := store.CreateAssistant(t.Context(), assistantstate.Record{ID: "asst_first", OwnerKey: "owner-a", Snapshot: []byte(`{"model":"model-a"}`)}, 2)
	if err != nil || first.Revision != 1 || first.CreatedAt.IsZero() {
		t.Fatalf("created=%+v err=%v", first, err)
	}
	if _, err = store.GetAssistant(t.Context(), "owner-b", first.ID); !errors.Is(err, assistantstate.ErrNotFound) {
		t.Fatalf("cross-owner get err=%v", err)
	}
	second, err := store.CreateAssistant(t.Context(), assistantstate.Record{ID: "asst_second", OwnerKey: "owner-a", Snapshot: []byte(`{"model":"model-b"}`)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateAssistant(t.Context(), assistantstate.Record{ID: "asst_third", OwnerKey: "owner-a", Snapshot: []byte(`{"model":"model-c"}`)}, 2); !errors.Is(err, assistantstate.ErrQuotaExceeded) {
		t.Fatalf("quota err=%v", err)
	}
	page, next, err := store.ListAssistants(t.Context(), "owner-a", 1, "")
	if err != nil || len(page) != 1 || next == "" {
		t.Fatalf("first page=%+v next=%q err=%v", page, next, err)
	}
	remaining, finalNext, err := store.ListAssistants(t.Context(), "owner-a", 1, next)
	if err != nil || len(remaining) != 1 || finalNext != "" || remaining[0].ID == page[0].ID {
		t.Fatalf("remaining=%+v next=%q err=%v", remaining, finalNext, err)
	}
	updated, err := store.UpdateAssistant(t.Context(), "owner-a", first.ID, []byte(`{"model":"model-updated"}`), first.Revision)
	if err != nil || updated.Revision != 2 || string(updated.Snapshot) != `{"model": "model-updated"}` && string(updated.Snapshot) != `{"model":"model-updated"}` {
		t.Fatalf("updated=%+v snapshot=%s err=%v", updated, updated.Snapshot, err)
	}
	if _, err = store.UpdateAssistant(t.Context(), "owner-a", first.ID, []byte(`{"model":"stale"}`), first.Revision); !errors.Is(err, assistantstate.ErrConflict) {
		t.Fatalf("stale update err=%v", err)
	}
	if err = store.DeleteAssistant(t.Context(), "owner-b", second.ID); !errors.Is(err, assistantstate.ErrNotFound) {
		t.Fatalf("cross-owner delete err=%v", err)
	}
	if err = store.DeleteAssistant(t.Context(), "owner-a", second.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAssistantQuotaIsAtomicIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err = store.pool.Exec(t.Context(), `CREATE TABLE gateway_assistants (
		id TEXT NOT NULL,owner_key TEXT NOT NULL,snapshot JSONB NOT NULL,revision BIGINT NOT NULL DEFAULT 1,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(owner_key,id))`); err != nil {
		t.Fatal(err)
	}

	const attempts = 12
	errorsSeen := make(chan error, attempts)
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(id int) {
			defer wait.Done()
			_, createErr := store.CreateAssistant(t.Context(), assistantstate.Record{ID: fmt.Sprintf("asst_%d", id), OwnerKey: "atomic-owner", Snapshot: []byte(`{"model":"model"}`)}, 1)
			errorsSeen <- createErr
		}(index)
	}
	wait.Wait()
	close(errorsSeen)
	succeeded, rejected := 0, 0
	for createErr := range errorsSeen {
		switch {
		case createErr == nil:
			succeeded++
		case errors.Is(createErr, assistantstate.ErrQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected create error: %v", createErr)
		}
	}
	if succeeded != 1 || rejected != attempts-1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestPostgresAssistantStoreRejectsInvalidRecords(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.CreateAssistant(t.Context(), assistantstate.Record{ID: "asst", OwnerKey: "owner", Snapshot: []byte(`[]`)}, 1); !errors.Is(err, assistantstate.ErrInvalid) {
		t.Fatalf("invalid snapshot err=%v", err)
	}
	if _, err := store.UpdateAssistant(t.Context(), "owner", "asst", []byte(`{"ok":true}`), 0); !errors.Is(err, assistantstate.ErrInvalid) {
		t.Fatalf("invalid revision err=%v", err)
	}
}
