package controlstore

import (
	"errors"
	"testing"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/asyncstate"
)

func TestPostgresA2ATaskAndOutboxAreAtomicIntegration(t *testing.T) {
	store := prepareA2ATaskStore(t)
	prepareAsyncJobTable(t, store)
	task := a2astate.Task{
		ID: "task_outbox", OwnerKey: "owner-outbox", AgentID: "agent", Model: "model", ContextID: "context",
		State: "TASK_STATE_SUBMITTED", Payload: []byte(`{"state":"submitted"}`),
	}
	job := asyncstate.Job{Kind: "a2a-push", ResourceID: task.ID, OwnerKey: task.OwnerKey, EndpointID: task.AgentID, ExecutionID: "exec_outbox", Payload: []byte(`{"url":"encrypted"}`)}
	created, err := store.CreateA2ATaskWithJob(t.Context(), task, 10, time.Hour, job)
	if err != nil || created.ID != task.ID {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if exists, err := store.HasAsyncJob(t.Context(), job.Kind, job.ResourceID, job.OwnerKey); err != nil || !exists {
		t.Fatalf("outbox exists=%t err=%v", exists, err)
	}

	conflictingJob := asyncstate.Job{Kind: "other-kind", ResourceID: "other-task", OwnerKey: "other-owner", EndpointID: "other-agent", ExecutionID: "exec_conflict", Payload: []byte(`{}`)}
	if _, err := store.EnqueueAsyncJob(t.Context(), conflictingJob); err != nil {
		t.Fatal(err)
	}
	rollbackTask := task
	rollbackTask.ID, rollbackTask.ContextID = "task_rollback", "context-rollback"
	rollbackJob := asyncstate.Job{Kind: job.Kind, ResourceID: rollbackTask.ID, OwnerKey: rollbackTask.OwnerKey, EndpointID: rollbackTask.AgentID, ExecutionID: conflictingJob.ExecutionID, Payload: []byte(`{}`)}
	if _, err := store.CreateA2ATaskWithJob(t.Context(), rollbackTask, 10, time.Hour, rollbackJob); !errors.Is(err, asyncstate.ErrConflict) {
		t.Fatalf("job conflict error=%v", err)
	}
	if _, err := store.GetA2ATask(t.Context(), rollbackTask.OwnerKey, rollbackTask.AgentID, rollbackTask.ID); !errors.Is(err, a2astate.ErrNotFound) {
		t.Fatalf("task survived rolled-back outbox insert: %v", err)
	}
}

func TestPostgresA2ATaskUpdateAndOutboxAreAtomicIntegration(t *testing.T) {
	store := prepareA2ATaskStore(t)
	prepareAsyncJobTable(t, store)
	task := a2astate.Task{
		ID: "task_update_outbox", OwnerKey: "owner-update-outbox", AgentID: "agent", Model: "model", ContextID: "context",
		State: "TASK_STATE_WORKING", Payload: []byte(`{"state":"working"}`),
	}
	created, err := store.CreateA2ATask(t.Context(), task, 10, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	task.State, task.Payload = "TASK_STATE_COMPLETED", []byte(`{"state":"completed"}`)
	job := asyncstate.Job{Kind: "a2a-push", ResourceID: task.ID, OwnerKey: task.OwnerKey, EndpointID: task.AgentID, ExecutionID: "exec_update_outbox", Payload: []byte(`{"event":"terminal"}`)}
	updated, err := store.UpdateA2ATaskWithJob(t.Context(), task, created.UpdatedAt, time.Hour, job)
	if err != nil || updated.State != task.State {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if exists, err := store.HasAsyncJob(t.Context(), job.Kind, job.ResourceID, job.OwnerKey); err != nil || !exists {
		t.Fatalf("outbox exists=%t err=%v", exists, err)
	}

	rollbackTask := task
	rollbackTask.Payload = []byte(`{"state":"must-rollback"}`)
	conflictJob := job
	conflictJob.Kind = "a2a-push-conflict"
	if _, err := store.UpdateA2ATaskWithJob(t.Context(), rollbackTask, updated.UpdatedAt, time.Hour, conflictJob); !errors.Is(err, asyncstate.ErrConflict) {
		t.Fatalf("job conflict error=%v", err)
	}
	loaded, err := store.GetA2ATask(t.Context(), task.OwnerKey, task.AgentID, task.ID)
	if err != nil || string(loaded.Payload) != string(task.Payload) || !loaded.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("task update was not rolled back: loaded=%+v err=%v", loaded, err)
	}
}

func TestA2AAtomicOutboxRejectsCrossScopeJob(t *testing.T) {
	store := (*PostgresStore)(nil)
	task := a2astate.Task{ID: "task", OwnerKey: "owner", AgentID: "agent", Model: "model", ContextID: "context", State: "state", Payload: []byte(`{}`)}
	job := asyncstate.Job{Kind: "a2a-push", ResourceID: task.ID, OwnerKey: "other-owner", EndpointID: task.AgentID, ExecutionID: "exec", Payload: []byte(`{}`)}
	if validA2AOutboxJob(task, job) {
		t.Fatal("cross-owner outbox job accepted")
	}
	if _, err := store.CreateA2ATaskWithJob(t.Context(), task, 1, time.Hour, job); !errors.Is(err, a2astate.ErrUnavailable) {
		t.Fatalf("nil store error=%v", err)
	}
}
