package controlstore

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/assistantstate"
)

func TestPostgresAssistantRunAndStepLifecycleIntegration(t *testing.T) {
	store := prepareAssistantRunStore(t)
	thread, err := store.CreateThread(t.Context(), assistantstate.ThreadRecord{ID: "thread_run", OwnerKey: "owner", Snapshot: []byte(`{"metadata":{}}`)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(t.Context(), assistantstate.RunRecord{ID: "run_a", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "queued", Snapshot: []byte(`{"assistant_id":"asst_a"}`), RetainUntil: time.Now().Add(time.Hour)}, 2)
	if err != nil || run.Revision != 1 {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	if _, err = store.GetRun(t.Context(), "other", thread.ID, run.ID); !errors.Is(err, assistantstate.ErrNotFound) {
		t.Fatalf("cross-owner run err=%v", err)
	}
	if _, err = store.CreateRun(t.Context(), assistantstate.RunRecord{ID: "run_active", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "queued", Snapshot: []byte(`{}`), RetainUntil: time.Now().Add(time.Hour)}, 2); !errors.Is(err, assistantstate.ErrConflict) {
		t.Fatalf("parallel active run err=%v", err)
	}
	run.Status, run.Snapshot = "in_progress", []byte(`{"assistant_id":"asst_a","started":true}`)
	run, err = store.TransitionRun(t.Context(), run, "queued", run.Revision)
	if err != nil || run.Revision != 2 {
		t.Fatalf("started=%+v err=%v", run, err)
	}
	if _, err = store.TransitionRun(t.Context(), run, "queued", 1); !errors.Is(err, assistantstate.ErrConflict) {
		t.Fatalf("stale transition err=%v", err)
	}
	step, err := store.CreateRunStep(t.Context(), assistantstate.RunStepRecord{ID: "step_a", RunID: run.ID, ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "in_progress", Snapshot: []byte(`{"type":"message_creation"}`)}, 1)
	if err != nil || step.Revision != 1 {
		t.Fatalf("step=%+v err=%v", step, err)
	}
	if _, err = store.CreateRunStep(t.Context(), assistantstate.RunStepRecord{ID: "step_b", RunID: run.ID, ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "in_progress", Snapshot: []byte(`{}`)}, 1); !errors.Is(err, assistantstate.ErrQuotaExceeded) {
		t.Fatalf("step quota err=%v", err)
	}
	step.Status, step.Snapshot = "completed", []byte(`{"type":"message_creation","done":true}`)
	step, err = store.UpdateRunStep(t.Context(), step, "in_progress", step.Revision)
	if err != nil || step.Revision != 2 {
		t.Fatalf("completed step=%+v err=%v", step, err)
	}
	if _, err = store.UpdateRunStep(t.Context(), step, "in_progress", 1); !errors.Is(err, assistantstate.ErrConflict) {
		t.Fatalf("terminal step transition err=%v", err)
	}
	steps, _, err := store.ListRunSteps(t.Context(), thread.OwnerKey, thread.ID, run.ID, assistantstate.RunPageOptions{Limit: 10, Order: "desc"})
	if err != nil || len(steps) != 1 || steps[0].ID != step.ID {
		t.Fatalf("steps=%+v err=%v", steps, err)
	}
	run.Status, run.Snapshot = "completed", []byte(`{"assistant_id":"asst_a","done":true}`)
	run, err = store.TransitionRun(t.Context(), run, "in_progress", run.Revision)
	if err != nil || run.Status != "completed" {
		t.Fatalf("completed run=%+v err=%v", run, err)
	}
	second, err := store.CreateRun(t.Context(), assistantstate.RunRecord{ID: "run_b", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "queued", Snapshot: []byte(`{}`), RetainUntil: time.Now().Add(time.Hour)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateRun(t.Context(), assistantstate.RunRecord{ID: "run_over_quota", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "queued", Snapshot: []byte(`{}`), RetainUntil: time.Now().Add(time.Hour)}, 2); !errors.Is(err, assistantstate.ErrQuotaExceeded) {
		t.Fatalf("run owner quota err=%v", err)
	}
	runs, _, err := store.ListRuns(t.Context(), thread.OwnerKey, thread.ID, assistantstate.RunPageOptions{Limit: 2, Order: "asc"})
	if err != nil || len(runs) != 2 || runs[0].ID != run.ID || runs[1].ID != second.ID {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

func TestPostgresAssistantActiveRunIsAtomicIntegration(t *testing.T) {
	store := prepareAssistantRunStore(t)
	thread, err := store.CreateThread(t.Context(), assistantstate.ThreadRecord{ID: "thread_atomic_run", OwnerKey: "owner", Snapshot: []byte(`{}`)}, 1)
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
			_, createErr := store.CreateRun(t.Context(), assistantstate.RunRecord{ID: fmt.Sprintf("run_%d", value), ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "queued", Snapshot: []byte(`{}`), RetainUntil: time.Now().Add(time.Hour)}, 100)
			results <- createErr
		}(index)
	}
	wait.Wait()
	close(results)
	success, conflicts := 0, 0
	for createErr := range results {
		if createErr == nil {
			success++
		} else if errors.Is(createErr, assistantstate.ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected error: %v", createErr)
		}
	}
	if success != 1 || conflicts != attempts-1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestPostgresAssistantRunRetentionBoundsOwnerHistoryIntegration(t *testing.T) {
	store := prepareAssistantRunStore(t)
	thread, err := store.CreateThread(t.Context(), assistantstate.ThreadRecord{ID: "thread_retention", OwnerKey: "owner", Snapshot: []byte(`{}`)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.pool.Exec(t.Context(), `INSERT INTO gateway_assistant_runs (id,thread_id,owner_key,status,snapshot,retain_until,created_at) VALUES ('run_expired',$1,$2,'completed','{}',now()-interval '1 hour',now()-interval '2 hours')`, thread.ID, thread.OwnerKey)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateRun(t.Context(), assistantstate.RunRecord{ID: "run_current", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "queued", Snapshot: []byte(`{}`), RetainUntil: time.Now().Add(time.Hour)}, 1)
	if err != nil || created.ID != "run_current" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err = store.GetRun(t.Context(), thread.OwnerKey, thread.ID, "run_expired"); !errors.Is(err, assistantstate.ErrNotFound) {
		t.Fatalf("expired run remains: %v", err)
	}
}

func TestPostgresAssistantRunCompletionIsAtomicIntegration(t *testing.T) {
	store := prepareAssistantRunStore(t)
	thread, err := store.CreateThread(t.Context(), assistantstate.ThreadRecord{ID: "thread_complete", OwnerKey: "owner", Snapshot: []byte(`{}`)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(t.Context(), assistantstate.RunRecord{ID: "run_complete", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "queued", Snapshot: []byte(`{"response_id":"resp"}`), RetainUntil: time.Now().Add(time.Hour)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = "completed"
	run.Snapshot = []byte(`{"response_id":"resp","done":true}`)
	step := assistantstate.RunStepRecord{ID: "step_complete", RunID: run.ID, ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "completed", Snapshot: []byte(`{"type":"message_creation"}`)}
	message := &assistantstate.MessageRecord{ID: "msg_complete", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Snapshot: []byte(`{"role":"assistant","content":[{"type":"text"}]}`)}
	completed, completedStep, completedMessage, err := store.CompleteRun(t.Context(), run, "queued", run.Revision, step, message, 1, 1)
	if err != nil || completed.Status != "completed" || completedStep.ID != step.ID || completedMessage == nil || completedMessage.ID != message.ID {
		t.Fatalf("run=%+v step=%+v message=%+v err=%v", completed, completedStep, completedMessage, err)
	}
	if _, _, _, err = store.CompleteRun(t.Context(), run, "queued", run.Revision, step, message, 1, 1); !errors.Is(err, assistantstate.ErrConflict) {
		t.Fatalf("stale completion err=%v", err)
	}

	second, err := store.CreateRun(t.Context(), assistantstate.RunRecord{ID: "run_rollback", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "queued", Snapshot: []byte(`{}`), RetainUntil: time.Now().Add(time.Hour)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	second.Status = "completed"
	rollbackStep := assistantstate.RunStepRecord{ID: "step_rollback", RunID: second.ID, ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "completed", Snapshot: []byte(`{}`)}
	rollbackMessage := &assistantstate.MessageRecord{ID: "msg_rollback", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Snapshot: []byte(`{}`)}
	if _, _, _, err = store.CompleteRun(t.Context(), second, "queued", second.Revision, rollbackStep, rollbackMessage, 1, 1); !errors.Is(err, assistantstate.ErrQuotaExceeded) {
		t.Fatalf("quota completion err=%v", err)
	}
	persisted, err := store.GetRun(t.Context(), thread.OwnerKey, thread.ID, second.ID)
	if err != nil || persisted.Status != "queued" {
		t.Fatalf("rollback run=%+v err=%v", persisted, err)
	}
	steps, _, err := store.ListRunSteps(t.Context(), thread.OwnerKey, thread.ID, second.ID, assistantstate.RunPageOptions{Limit: 10, Order: "asc"})
	if err != nil || len(steps) != 0 {
		t.Fatalf("rollback steps=%+v err=%v", steps, err)
	}
}

func TestPostgresAssistantRunToolStepTransitionIsAtomicIntegration(t *testing.T) {
	store := prepareAssistantRunStore(t)
	thread, err := store.CreateThread(t.Context(), assistantstate.ThreadRecord{ID: "thread_tool_step", OwnerKey: "owner", Snapshot: []byte(`{}`)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(t.Context(), assistantstate.RunRecord{ID: "run_tool_step", ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "queued", Snapshot: []byte(`{}`), RetainUntil: time.Now().Add(time.Hour)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = "in_progress"
	run, err = store.TransitionRun(t.Context(), run, "queued", run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Snapshot = "requires_action", []byte(`{"required_action":{"type":"submit_tool_outputs"}}`)
	step := assistantstate.RunStepRecord{ID: "step_tool_calls", RunID: run.ID, ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "completed", Snapshot: []byte(`{"type":"tool_calls"}`)}
	_, err = store.CreateRunStep(t.Context(), assistantstate.RunStepRecord{ID: "step_existing", RunID: run.ID, ThreadID: thread.ID, OwnerKey: thread.OwnerKey, Status: "in_progress", Snapshot: []byte(`{}`)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.TransitionRunWithStep(t.Context(), run, "in_progress", run.Revision, step, 1); !errors.Is(err, assistantstate.ErrQuotaExceeded) {
		t.Fatalf("quota err=%v", err)
	}
	persisted, err := store.GetRun(t.Context(), thread.OwnerKey, thread.ID, run.ID)
	if err != nil || persisted.Status != "in_progress" {
		t.Fatalf("rollback run=%+v err=%v", persisted, err)
	}
	steps, _, err := store.ListRunSteps(t.Context(), thread.OwnerKey, thread.ID, run.ID, assistantstate.RunPageOptions{Limit: 10, Order: "asc"})
	if err != nil || len(steps) != 1 || steps[0].ID != "step_existing" {
		t.Fatalf("rollback steps=%+v err=%v", steps, err)
	}
	updated, created, err := store.TransitionRunWithStep(t.Context(), run, "in_progress", run.Revision, step, 2)
	if err != nil || updated.Status != "requires_action" || created.ID != step.ID {
		t.Fatalf("run=%+v step=%+v err=%v", updated, created, err)
	}
	if _, _, err = store.TransitionRunWithStep(t.Context(), run, "in_progress", run.Revision, step, 2); !errors.Is(err, assistantstate.ErrConflict) {
		t.Fatalf("stale transition err=%v", err)
	}
}

func prepareAssistantRunStore(t *testing.T) *PostgresStore {
	t.Helper()
	store := prepareAssistantThreadStore(t)
	_, err := store.pool.Exec(t.Context(), `CREATE TABLE gateway_assistant_runs (
		id TEXT NOT NULL,thread_id TEXT NOT NULL,owner_key TEXT NOT NULL,status TEXT NOT NULL,snapshot JSONB NOT NULL,
		revision BIGINT NOT NULL DEFAULT 1,retain_until TIMESTAMPTZ NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY(owner_key,thread_id,id),FOREIGN KEY(owner_key,thread_id) REFERENCES gateway_assistant_threads(owner_key,id) ON DELETE CASCADE);
		CREATE UNIQUE INDEX gateway_assistant_runs_one_active_idx ON gateway_assistant_runs(owner_key,thread_id) WHERE status IN ('queued','in_progress','requires_action','cancelling');
		CREATE TABLE gateway_assistant_run_steps (
		id TEXT NOT NULL,run_id TEXT NOT NULL,thread_id TEXT NOT NULL,owner_key TEXT NOT NULL,status TEXT NOT NULL,snapshot JSONB NOT NULL,
		revision BIGINT NOT NULL DEFAULT 1,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY(owner_key,thread_id,run_id,id),FOREIGN KEY(owner_key,thread_id,run_id) REFERENCES gateway_assistant_runs(owner_key,thread_id,id) ON DELETE CASCADE)`)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

var _ assistantstate.RunStore = (*PostgresStore)(nil)
