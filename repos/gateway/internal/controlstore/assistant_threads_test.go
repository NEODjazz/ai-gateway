package controlstore

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"ai-gateway-gateway/internal/assistantstate"
)

func TestPostgresAssistantThreadAndMessageLifecycleIntegration(t *testing.T) {
	store := prepareAssistantThreadStore(t)
	thread, err := store.CreateThread(t.Context(), assistantstate.ThreadRecord{ID: "thread_a", OwnerKey: "owner-a", Snapshot: []byte(`{"metadata":{}}`)}, 2)
	if err != nil || thread.Revision != 1 {
		t.Fatalf("thread=%+v err=%v", thread, err)
	}
	if _, err := store.GetThread(t.Context(), "owner-b", thread.ID); !errors.Is(err, assistantstate.ErrNotFound) {
		t.Fatalf("cross-owner thread err=%v", err)
	}
	message, err := store.CreateThreadMessage(t.Context(), assistantstate.MessageRecord{ID: "msg_a", ThreadID: thread.ID, OwnerKey: "owner-a", Snapshot: []byte(`{"role":"user","content":"hello"}`)}, 2)
	if err != nil || message.Revision != 1 {
		t.Fatalf("message=%+v err=%v", message, err)
	}
	second, err := store.CreateThreadMessage(t.Context(), assistantstate.MessageRecord{ID: "msg_b", ThreadID: thread.ID, OwnerKey: "owner-a", Snapshot: []byte(`{"role":"assistant","content":"hi"}`)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateThreadMessage(t.Context(), assistantstate.MessageRecord{ID: "msg_c", ThreadID: thread.ID, OwnerKey: "owner-a", Snapshot: []byte(`{"role":"user"}`)}, 2); !errors.Is(err, assistantstate.ErrQuotaExceeded) {
		t.Fatalf("message quota err=%v", err)
	}
	page, next, err := store.ListThreadMessages(t.Context(), "owner-a", thread.ID, assistantstate.MessagePageOptions{Limit: 1, Order: "desc"})
	if err != nil || len(page) != 1 || next == "" {
		t.Fatalf("page=%+v next=%q err=%v", page, next, err)
	}
	rest, finalNext, err := store.ListThreadMessages(t.Context(), "owner-a", thread.ID, assistantstate.MessagePageOptions{Limit: 1, After: next, Order: "desc"})
	if err != nil || len(rest) != 1 || finalNext != "" || rest[0].ID == page[0].ID {
		t.Fatalf("rest=%+v next=%q err=%v", rest, finalNext, err)
	}
	ascending, _, err := store.ListThreadMessages(t.Context(), "owner-a", thread.ID, assistantstate.MessagePageOptions{Limit: 2, Order: "asc"})
	if err != nil || len(ascending) != 2 || ascending[0].ID != rest[0].ID || ascending[1].ID != page[0].ID {
		t.Fatalf("ascending=%+v err=%v", ascending, err)
	}
	before, _, err := store.ListThreadMessages(t.Context(), "owner-a", thread.ID, assistantstate.MessagePageOptions{Limit: 2, Before: page[0].ID, Order: "desc"})
	if err != nil || len(before) != 0 {
		t.Fatalf("before newest=%+v err=%v", before, err)
	}
	beforeOldest, _, err := store.ListThreadMessages(t.Context(), "owner-a", thread.ID, assistantstate.MessagePageOptions{Limit: 2, Before: rest[0].ID, Order: "desc"})
	if err != nil || len(beforeOldest) != 1 || beforeOldest[0].ID != page[0].ID {
		t.Fatalf("before oldest=%+v err=%v", beforeOldest, err)
	}
	updated, err := store.UpdateThreadMessage(t.Context(), "owner-a", thread.ID, message.ID, []byte(`{"role":"user","content":"updated"}`), message.Revision)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err = store.UpdateThreadMessage(t.Context(), "owner-a", thread.ID, message.ID, []byte(`{"role":"user"}`), message.Revision); !errors.Is(err, assistantstate.ErrConflict) {
		t.Fatalf("stale message update err=%v", err)
	}
	if err = store.DeleteThread(t.Context(), "owner-a", thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetThreadMessage(t.Context(), "owner-a", thread.ID, second.ID); !errors.Is(err, assistantstate.ErrNotFound) {
		t.Fatalf("thread deletion did not cascade: %v", err)
	}
}

func TestPostgresAssistantThreadQuotaIsAtomicIntegration(t *testing.T) {
	store := prepareAssistantThreadStore(t)
	const attempts = 12
	results := make(chan error, attempts)
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(value int) {
			defer wait.Done()
			_, err := store.CreateThread(t.Context(), assistantstate.ThreadRecord{ID: fmt.Sprintf("thread_%d", value), OwnerKey: "atomic-owner", Snapshot: []byte(`{"metadata":{}}`)}, 1)
			results <- err
		}(index)
	}
	wait.Wait()
	close(results)
	success, quota := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, assistantstate.ErrQuotaExceeded) {
			quota++
		} else {
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	if success != 1 || quota != attempts-1 {
		t.Fatalf("success=%d quota=%d", success, quota)
	}
}

func TestPostgresAssistantMessageQuotaIsAtomicIntegration(t *testing.T) {
	store := prepareAssistantThreadStore(t)
	thread, err := store.CreateThread(t.Context(), assistantstate.ThreadRecord{ID: "thread", OwnerKey: "message-owner", Snapshot: []byte(`{"metadata":{}}`)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	const attempts = 12
	results := make(chan error, attempts)
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(value int) {
			defer wait.Done()
			_, createErr := store.CreateThreadMessage(t.Context(), assistantstate.MessageRecord{ID: fmt.Sprintf("msg_%d", value), ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Snapshot: []byte(`{"role":"user"}`)}, 1)
			results <- createErr
		}(index)
	}
	wait.Wait()
	close(results)
	success, quota := 0, 0
	for createErr := range results {
		if createErr == nil {
			success++
		} else if errors.Is(createErr, assistantstate.ErrQuotaExceeded) {
			quota++
		} else {
			t.Fatalf("unexpected create error: %v", createErr)
		}
	}
	if success != 1 || quota != attempts-1 {
		t.Fatalf("success=%d quota=%d", success, quota)
	}
}

func TestPostgresAssistantThreadInitialMessagesAreAtomicIntegration(t *testing.T) {
	store := prepareAssistantThreadStore(t)
	thread := assistantstate.ThreadRecord{ID: "thread_initial", OwnerKey: "initial-owner", Snapshot: []byte(`{"metadata":{}}`)}
	messages := []assistantstate.MessageRecord{
		{ID: "msg_initial_a", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Snapshot: []byte(`{"role":"user","content":"one"}`)},
		{ID: "msg_initial_b", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Snapshot: []byte(`{"role":"assistant","content":"two"}`)},
	}
	createdThread, createdMessages, err := store.CreateThreadWithMessages(t.Context(), thread, messages, 2, 2)
	if err != nil || createdThread.Revision != 1 || len(createdMessages) != 2 {
		t.Fatalf("thread=%+v messages=%+v err=%v", createdThread, createdMessages, err)
	}
	listed, _, err := store.ListThreadMessages(t.Context(), thread.OwnerKey, thread.ID, assistantstate.MessagePageOptions{Limit: 10, Order: "asc"})
	if err != nil || len(listed) != 2 || listed[0].ID != messages[0].ID || listed[1].ID != messages[1].ID {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	rejectedThread := assistantstate.ThreadRecord{ID: "thread_rejected", OwnerKey: thread.OwnerKey, Snapshot: []byte(`{"metadata":{}}`)}
	rejectedMessages := []assistantstate.MessageRecord{
		{ID: "msg_rejected_a", ThreadID: rejectedThread.ID, OwnerKey: rejectedThread.OwnerKey, Snapshot: []byte(`{"role":"user"}`)},
		{ID: "msg_rejected_b", ThreadID: rejectedThread.ID, OwnerKey: rejectedThread.OwnerKey, Snapshot: []byte(`{"role":"user"}`)},
	}
	if _, _, err = store.CreateThreadWithMessages(t.Context(), rejectedThread, rejectedMessages, 2, 1); !errors.Is(err, assistantstate.ErrQuotaExceeded) {
		t.Fatalf("quota err=%v", err)
	}
	if _, err = store.GetThread(t.Context(), rejectedThread.OwnerKey, rejectedThread.ID); !errors.Is(err, assistantstate.ErrNotFound) {
		t.Fatalf("rejected thread was persisted: %v", err)
	}
}

func prepareAssistantThreadStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	_, err = store.pool.Exec(t.Context(), `CREATE TABLE gateway_assistant_threads (
		id TEXT NOT NULL,owner_key TEXT NOT NULL,snapshot JSONB NOT NULL,revision BIGINT NOT NULL DEFAULT 1,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(owner_key,id));
		CREATE TABLE gateway_assistant_messages (
		id TEXT NOT NULL,thread_id TEXT NOT NULL,owner_key TEXT NOT NULL,snapshot JSONB NOT NULL,revision BIGINT NOT NULL DEFAULT 1,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(owner_key,thread_id,id),
		FOREIGN KEY(owner_key,thread_id) REFERENCES gateway_assistant_threads(owner_key,id) ON DELETE CASCADE)`)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
