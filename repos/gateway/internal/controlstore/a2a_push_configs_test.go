package controlstore

import (
	"errors"
	"testing"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/asyncstate"
)

func TestPostgresA2APushConfigLifecycleIsolationAndAtomicityIntegration(t *testing.T) {
	store := prepareA2ATaskStore(t)
	prepareAsyncJobTable(t, store)
	prepareA2APushConfigTable(t, store)
	task := a2astate.Task{ID: "task_push", OwnerKey: "owner-push", AgentID: "agent", Model: "model", ContextID: "context", State: "TASK_STATE_WORKING", Payload: []byte(`{"state":"working"}`)}
	config := a2astate.PushConfig{ID: "config_a", TaskID: task.ID, OwnerKey: task.OwnerKey, AgentID: task.AgentID, Payload: []byte(`{"encrypted":"a"}`)}
	job := asyncstate.Job{Kind: "a2a-push", ResourceID: task.ID + ":" + config.ID, OwnerKey: task.OwnerKey, EndpointID: task.AgentID, ExecutionID: "push_exec_a", Payload: []byte(`{"encrypted":"job-a"}`)}
	created, err := store.CreateA2ATaskWithPushConfig(t.Context(), task, config, 10, 3, time.Hour, job)
	if err != nil || created.ID != task.ID {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	loaded, err := store.GetA2APushConfig(t.Context(), task.OwnerKey, task.AgentID, task.ID, config.ID)
	if err != nil || string(loaded.Payload) != string(config.Payload) || loaded.CreatedAt.IsZero() {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if _, err := store.GetA2APushConfig(t.Context(), "other-owner", task.AgentID, task.ID, config.ID); !errors.Is(err, a2astate.ErrNotFound) {
		t.Fatalf("cross-owner read error=%v", err)
	}
	crossOwner := config
	crossOwner.ID, crossOwner.OwnerKey = "config_cross_owner", "other-owner"
	crossOwnerJob := job
	crossOwnerJob.ResourceID, crossOwnerJob.OwnerKey, crossOwnerJob.ExecutionID = task.ID+":"+crossOwner.ID, crossOwner.OwnerKey, "push_exec_cross_owner"
	if _, err := store.CreateA2APushConfig(t.Context(), crossOwner, 3, crossOwnerJob); !errors.Is(err, a2astate.ErrNotFound) {
		t.Fatalf("cross-owner create error=%v", err)
	}

	for _, id := range []string{"config_b", "config_c"} {
		candidate := config
		candidate.ID, candidate.Payload = id, []byte(`{"encrypted":"`+id+`"}`)
		candidateJob := job
		candidateJob.ResourceID, candidateJob.ExecutionID = task.ID+":"+id, "push_exec_"+id
		if _, err := store.CreateA2APushConfig(t.Context(), candidate, 3, candidateJob); err != nil {
			t.Fatal(err)
		}
	}
	first, next, total, err := store.ListA2APushConfigs(t.Context(), task.OwnerKey, task.AgentID, task.ID, 2, "")
	if err != nil || len(first) != 2 || next == "" || total != 3 {
		t.Fatalf("first=%+v next=%q total=%d err=%v", first, next, total, err)
	}
	second, final, total, err := store.ListA2APushConfigs(t.Context(), task.OwnerKey, task.AgentID, task.ID, 2, next)
	if err != nil || len(second) != 1 || final != "" || total != 3 {
		t.Fatalf("second=%+v next=%q total=%d err=%v", second, final, total, err)
	}
	overQuota := config
	overQuota.ID = "config_d"
	overQuotaJob := job
	overQuotaJob.ResourceID, overQuotaJob.ExecutionID = task.ID+":"+overQuota.ID, "push_exec_d"
	if _, err := store.CreateA2APushConfig(t.Context(), overQuota, 3, overQuotaJob); !errors.Is(err, a2astate.ErrQuotaExceeded) {
		t.Fatalf("quota error=%v", err)
	}
	if err := store.DeleteA2APushConfig(t.Context(), task.OwnerKey, task.AgentID, task.ID, "config_b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetA2APushConfig(t.Context(), task.OwnerKey, task.AgentID, task.ID, "config_b"); !errors.Is(err, a2astate.ErrNotFound) {
		t.Fatalf("deleted config error=%v", err)
	}

	conflicting := asyncstate.Job{Kind: "other", ResourceID: "resource", OwnerKey: "owner", EndpointID: "agent", ExecutionID: "push_exec_conflict", Payload: []byte(`{}`)}
	if _, err := store.EnqueueAsyncJob(t.Context(), conflicting); err != nil {
		t.Fatal(err)
	}
	rollbackTask := task
	rollbackTask.ID, rollbackTask.ContextID = "task_push_rollback", "context_rollback"
	rollbackConfig := config
	rollbackConfig.ID, rollbackConfig.TaskID = "config_rollback", rollbackTask.ID
	rollbackJob := job
	rollbackJob.ResourceID, rollbackJob.ExecutionID = rollbackTask.ID+":"+rollbackConfig.ID, conflicting.ExecutionID
	if _, err := store.CreateA2ATaskWithPushConfig(t.Context(), rollbackTask, rollbackConfig, 10, 3, time.Hour, rollbackJob); !errors.Is(err, asyncstate.ErrConflict) {
		t.Fatalf("outbox conflict error=%v", err)
	}
	if _, err := store.GetA2ATask(t.Context(), rollbackTask.OwnerKey, rollbackTask.AgentID, rollbackTask.ID); !errors.Is(err, a2astate.ErrNotFound) {
		t.Fatalf("task survived rollback: %v", err)
	}
	if _, err := store.GetA2APushConfig(t.Context(), rollbackConfig.OwnerKey, rollbackConfig.AgentID, rollbackConfig.TaskID, rollbackConfig.ID); !errors.Is(err, a2astate.ErrNotFound) {
		t.Fatalf("config survived rollback: %v", err)
	}
	updateTask := task
	updateTask.State, updateTask.Payload = "TASK_STATE_COMPLETED", []byte(`{"state":"must-rollback"}`)
	updateConfig := config
	updateConfig.ID = "config_update_rollback"
	updateJob := job
	updateJob.ResourceID, updateJob.ExecutionID = task.ID+":"+updateConfig.ID, conflicting.ExecutionID
	if _, err := store.UpdateA2ATaskWithPushConfig(t.Context(), updateTask, updateConfig, created.UpdatedAt, 3, time.Hour, updateJob); !errors.Is(err, asyncstate.ErrConflict) {
		t.Fatalf("update outbox conflict error=%v", err)
	}
	if _, err := store.GetA2APushConfig(t.Context(), task.OwnerKey, task.AgentID, task.ID, updateConfig.ID); !errors.Is(err, a2astate.ErrNotFound) {
		t.Fatalf("updated config survived rollback: %v", err)
	}
	unchanged, err := store.GetA2ATask(t.Context(), task.OwnerKey, task.AgentID, task.ID)
	if err != nil || unchanged.State != task.State || string(unchanged.Payload) != string(task.Payload) {
		t.Fatalf("task update survived rollback: task=%+v err=%v", unchanged, err)
	}
}

func prepareA2APushConfigTable(t *testing.T, store *PostgresStore) {
	t.Helper()
	_, err := store.pool.Exec(t.Context(), `CREATE TABLE gateway_a2a_push_configs (
		id TEXT NOT NULL, task_id TEXT NOT NULL REFERENCES gateway_a2a_tasks(id) ON DELETE CASCADE,
		owner_key TEXT NOT NULL, agent_id TEXT NOT NULL, payload BYTEA NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (task_id,id), CHECK (octet_length(payload) BETWEEN 1 AND 16384))`)
	if err != nil {
		t.Fatal(err)
	}
}
