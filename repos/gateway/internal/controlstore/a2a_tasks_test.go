package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/a2astate"
)

func TestPostgresA2ATaskLifecycleAndIsolationIntegration(t *testing.T) {
	store := prepareA2ATaskStore(t)
	ctx := t.Context()
	task := a2astate.Task{
		ID: "task_a", OwnerKey: "owner-a", AgentID: "agent-a", Model: "model-a", ContextID: "context-a",
		State: "TASK_STATE_COMPLETED", Payload: []byte(`{"id":"task_a"}`),
	}
	created, err := store.CreateA2ATask(ctx, task, 10, time.Hour)
	if err != nil || created.CreatedAt.IsZero() || created.ExpiresAt.Before(created.CreatedAt) {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	loaded, err := store.GetA2ATask(ctx, task.OwnerKey, task.AgentID, task.ID)
	var payload map[string]string
	if err != nil || json.Unmarshal(loaded.Payload, &payload) != nil || payload["id"] != task.ID {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	task.Payload = []byte(`{"id":"task_a","updated":"true"}`)
	updated, err := store.UpdateA2ATask(ctx, task, created.UpdatedAt, 2*time.Hour)
	if err != nil || !updated.UpdatedAt.After(created.UpdatedAt) || updated.ExpiresAt.Before(created.ExpiresAt) {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err = store.UpdateA2ATask(ctx, task, created.UpdatedAt, time.Hour); !errors.Is(err, a2astate.ErrConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	for _, key := range [][2]string{{"owner-b", task.AgentID}, {task.OwnerKey, "agent-b"}} {
		if _, err := store.GetA2ATask(ctx, key[0], key[1], task.ID); !errors.Is(err, a2astate.ErrNotFound) {
			t.Fatalf("cross-scope get error=%v", err)
		}
	}
	if _, err := store.CreateA2ATask(ctx, task, 10, time.Hour); !errors.Is(err, a2astate.ErrConflict) {
		t.Fatalf("duplicate create error=%v", err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE gateway_a2a_tasks SET expires_at=now()-interval '1 second' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetA2ATask(ctx, task.OwnerKey, task.AgentID, task.ID); !errors.Is(err, a2astate.ErrNotFound) {
		t.Fatalf("expired task error=%v", err)
	}
	task.ID = "task_after_expiry"
	if _, err := store.CreateA2ATask(ctx, task, 1, time.Hour); err != nil {
		t.Fatalf("expired task did not release quota: %v", err)
	}
}

func TestPostgresA2ATaskUpdateIsAtomicIntegration(t *testing.T) {
	store := prepareA2ATaskStore(t)
	task := a2astate.Task{ID: "task_update", OwnerKey: "owner-update", AgentID: "agent", Model: "model", ContextID: "context", State: "TASK_STATE_COMPLETED", Payload: []byte(`{"version":0}`)}
	created, err := store.CreateA2ATask(t.Context(), task, 10, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for version := 1; version <= 2; version++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			candidate := task
			candidate.Payload = []byte(`{"version":` + string(rune('0'+version)) + `}`)
			_, updateErr := store.UpdateA2ATask(context.Background(), candidate, created.UpdatedAt, time.Hour)
			results <- updateErr
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	var succeeded, conflicted int
	for updateErr := range results {
		switch {
		case updateErr == nil:
			succeeded++
		case errors.Is(updateErr, a2astate.ErrConflict):
			conflicted++
		default:
			t.Fatalf("update error=%v", updateErr)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestPostgresA2ATaskQuotaIsAtomicIntegration(t *testing.T) {
	store := prepareA2ATaskStore(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, id := range []string{"task_quota_a", "task_quota_b"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := store.CreateA2ATask(context.Background(), a2astate.Task{
				ID: id, OwnerKey: "owner-quota", AgentID: "agent", Model: "model", ContextID: "context",
				State: "TASK_STATE_COMPLETED", Payload: []byte(`{"state":"completed"}`),
			}, 1, time.Hour)
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	var succeeded, rejected int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, a2astate.ErrQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestPostgresA2ATaskListPaginationAndFiltersIntegration(t *testing.T) {
	store := prepareA2ATaskStore(t)
	past := time.Now().Add(-time.Minute)
	for _, task := range []a2astate.Task{
		{ID: "task_list_a", ContextID: "context-a", State: "TASK_STATE_COMPLETED"},
		{ID: "task_list_b", ContextID: "context-a", State: "TASK_STATE_FAILED"},
		{ID: "task_list_c", ContextID: "context-b", State: "TASK_STATE_COMPLETED"},
	} {
		task.OwnerKey, task.AgentID, task.Model, task.Payload = "owner-list", "agent", "model", []byte(`{"valid":true}`)
		if _, err := store.CreateA2ATask(t.Context(), task, 10, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	first, next, total, err := store.ListA2ATasks(t.Context(), "owner-list", "agent", a2astate.ListOptions{Model: "model", Limit: 2})
	if err != nil || len(first) != 2 || next == "" || total != 3 {
		t.Fatalf("first=%+v next=%q total=%d err=%v", first, next, total, err)
	}
	second, final, total, err := store.ListA2ATasks(t.Context(), "owner-list", "agent", a2astate.ListOptions{Model: "model", Limit: 2, After: next})
	if err != nil || len(second) != 1 || final != "" || total != 3 {
		t.Fatalf("second=%+v next=%q total=%d err=%v", second, final, total, err)
	}
	filtered, _, total, err := store.ListA2ATasks(t.Context(), "owner-list", "agent", a2astate.ListOptions{Model: "model", Limit: 10, ContextID: "context-a", State: "TASK_STATE_COMPLETED"})
	if err != nil || len(filtered) != 1 || filtered[0].ID != "task_list_a" || total != 1 {
		t.Fatalf("filtered=%+v total=%d err=%v", filtered, total, err)
	}
	recent, _, total, err := store.ListA2ATasks(t.Context(), "owner-list", "agent", a2astate.ListOptions{Model: "model", Limit: 10, UpdatedAfter: &past})
	if err != nil || len(recent) != 3 || total != 3 {
		t.Fatalf("recent=%+v total=%d err=%v", recent, total, err)
	}
	future := time.Now().Add(time.Minute)
	recent, _, total, err = store.ListA2ATasks(t.Context(), "owner-list", "agent", a2astate.ListOptions{Model: "model", Limit: 10, UpdatedAfter: &future})
	if err != nil || len(recent) != 0 || total != 0 {
		t.Fatalf("future-filtered=%+v total=%d err=%v", recent, total, err)
	}
}

func prepareA2ATaskStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	_, err = store.pool.Exec(t.Context(), `CREATE TABLE gateway_a2a_tasks (
		id TEXT PRIMARY KEY, owner_key TEXT NOT NULL, agent_id TEXT NOT NULL, model TEXT NOT NULL, context_id TEXT NOT NULL,
		state TEXT NOT NULL, payload BYTEA NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), expires_at TIMESTAMPTZ NOT NULL,
		CHECK (octet_length(payload) BETWEEN 1 AND 2097152))`)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
