package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/controlstore"
	"ai-gateway-gateway/internal/modules"
	"github.com/jackc/pgx/v5/pgxpool"
)

func prepareAgentMCPBackgroundTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `CREATE TABLE gateway_a2a_tasks (
 id TEXT PRIMARY KEY,owner_key TEXT NOT NULL,agent_id TEXT NOT NULL,model TEXT NOT NULL,context_id TEXT NOT NULL,
 state TEXT NOT NULL,payload BYTEA NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 2097152),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),expires_at TIMESTAMPTZ NOT NULL);
 CREATE TABLE gateway_mcp_tool_calls (
 scope_key TEXT NOT NULL,idempotency_key TEXT NOT NULL,request_hash TEXT NOT NULL,execution_id TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('pending','completed')),http_status INTEGER NOT NULL DEFAULT 0,response BYTEA,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(scope_key,idempotency_key));
 CREATE TABLE gateway_async_jobs (
 kind TEXT NOT NULL,resource_id TEXT NOT NULL,owner_key TEXT NOT NULL,endpoint_id TEXT NOT NULL,execution_id TEXT NOT NULL UNIQUE,payload BYTEA NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','leased')),attempts INTEGER NOT NULL DEFAULT 0,lease_generation BIGINT NOT NULL DEFAULT 0,
 available_at TIMESTAMPTZ NOT NULL DEFAULT now(),lease_until TIMESTAMPTZ,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(kind,resource_id));`)
	if err != nil {
		t.Fatal("could not prepare isolated background agent tables")
	}
}

func configurePostgresBackgroundAgent(t *testing.T, h Handler, store *controlstore.PostgresStore) Handler {
	t.Helper()
	h = h.WithA2ATaskStore(store, h.a2aTaskConfig).WithMCPCallStore(store)
	var err error
	h, err = h.WithAgentMCPBackground(store, []byte("synthetic-background-key-material"))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPostgresAgentMCPBackgroundAtomicQueueAndReplicaWorkerIntegration(t *testing.T) {
	store, pool, dsn := agentPostgresTestStore(t)
	prepareAgentMCPBackgroundTables(t, pool)
	h, llm, client := agentMCPApprovalHandler(t)
	h = configurePostgresBackgroundAgent(t, h, store)
	// A failed job insert must roll back the task insert too, before any effects.
	if _, err := pool.Exec(t.Context(), `ALTER TABLE gateway_async_jobs ADD CONSTRAINT reject_agent CHECK (kind <> 'agent.mcp.v1')`); err != nil {
		t.Fatal(err)
	}
	rejected := agentMCPSend(h, "SendMessage", `,"configuration":{"returnImmediately":true}`)
	var tasks int
	if rejected.Code < 500 || pool.QueryRow(t.Context(), `SELECT count(*) FROM gateway_a2a_tasks`).Scan(&tasks) != nil || tasks != 0 || len(llm.requests) != 0 || client.calls != 0 {
		t.Fatal("failed outbox insert left a task or performed effects")
	}
	if _, err := pool.Exec(t.Context(), `ALTER TABLE gateway_async_jobs DROP CONSTRAINT reject_agent`); err != nil {
		t.Fatal(err)
	}
	task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", `,"configuration":{"returnImmediately":true}`))
	replicaStore, err := controlstore.NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replicaStore.Close)
	replica := configurePostgresBackgroundAgent(t, h, replicaStore)
	counts := make(chan int, 2)
	failures := make(chan error, 2)
	var workers sync.WaitGroup
	for _, handler := range []Handler{h, replica} {
		workers.Add(1)
		go func(current Handler) {
			defer workers.Done()
			count, err := current.ProcessAgentMCPBackground(t.Context())
			counts <- count
			failures <- err
		}(handler)
	}
	workers.Wait()
	close(counts)
	close(failures)
	total := 0
	for count := range counts {
		total += count
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	review := readBackgroundAgentTask(t, replica, task.ID)
	if total != 1 || len(llm.requests) != 1 || client.callCalls != 0 || review.Status.State != "TASK_STATE_INPUT_REQUIRED" {
		t.Fatal("replicas executed a queued model more than once or missed approval")
	}
	queued := agentMCPResultTask(t, backgroundAgentDecision(replica, agentMCPDecision(t, review, true, "approve-background")))
	if queued.Status.State != "TASK_STATE_SUBMITTED" || client.callCalls != 0 {
		t.Fatal("background approval executed synchronously")
	}
	// Cancellation arrives while the second model settles, after one durable MCP call.
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	llm.afterResponse = func() { enterOnce.Do(func() { close(entered) }); <-release }
	result := make(chan error, 1)
	go func() { _, err := replica.ProcessAgentMCPBackground(t.Context()); result <- err }()
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		unblock()
		t.Fatal("background model did not start")
	}
	canceled := agentMCPResultTask(t, agentMCPTaskRPC(h, "CancelTask", a2aMessage{}, task.ID))
	unblock()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("background cancellation did not settle")
	}
	if canceled.Status.State != "TASK_STATE_WORKING" || readBackgroundAgentTask(t, h, task.ID).Status.State != "TASK_STATE_CANCELED" || client.callCalls != 1 || len(llm.requests) != 2 {
		t.Fatal("running cancellation lied about settlement or repeated effects")
	}
	if pool.QueryRow(t.Context(), `SELECT count(*) FROM gateway_async_jobs`).Scan(&tasks) != nil || tasks != 0 {
		t.Fatal("settled background job was not deleted")
	}
	if llm.requests[0].RequestID == llm.requests[1].RequestID {
		t.Fatal("independent model steps shared billing identity")
	}
	var calls int
	if pool.QueryRow(t.Context(), `SELECT count(*) FROM gateway_mcp_tool_calls WHERE state='completed'`).Scan(&calls) != nil || calls != 1 {
		t.Fatal("actual MCP result was not durably committed")
	}
}

type failingAgentTaskUpdateStore struct {
	*controlstore.PostgresStore
	fail bool
}

func (s *failingAgentTaskUpdateStore) UpdateA2ATask(ctx context.Context, task a2astate.Task, expected time.Time, ttl time.Duration) (a2astate.Task, error) {
	if s.fail {
		return a2astate.Task{}, a2astate.ErrUnavailable
	}
	return s.PostgresStore.UpdateA2ATask(ctx, task, expected, ttl)
}

func TestPostgresAgentMCPBackgroundInterruptedEffectIsNotReplayedIntegration(t *testing.T) {
	store, pool, dsn := agentPostgresTestStore(t)
	prepareAgentMCPBackgroundTables(t, pool)
	h, llm, client := agentMCPApprovalHandler(t)
	h = configurePostgresBackgroundAgent(t, h, store)
	task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", `,"configuration":{"returnImmediately":true}`))
	failures := &failingAgentTaskUpdateStore{PostgresStore: store}
	broken := h.WithA2ATaskStore(failures, h.a2aTaskConfig)
	// The model has executed, but neither the next checkpoint nor failure cleanup can persist.
	llm.afterResponse = func() { failures.fail = true }
	if _, err := broken.ProcessAgentMCPBackground(t.Context()); err == nil || len(llm.requests) != 1 || client.callCalls != 0 {
		t.Fatal("did not reproduce interrupted model settlement")
	}
	llm.afterResponse = nil
	identity := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	stored, err := store.GetA2ATask(t.Context(), identity, "research", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := decodeAgentMCPState(stored.Payload)
	if err != nil || !state.WorkerStarted || state.Iterations != 1 {
		t.Fatal("effect claim was not durable before the model")
	}
	state.Deadline = time.Now().Add(-time.Minute)
	public, err := decodeA2ATask(stored.Payload)
	if err != nil {
		t.Fatal(err)
	}
	stored.Payload, err = encodeAgentMCPTask(public, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.UpdateA2ATask(t.Context(), stored, stored.UpdatedAt, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE gateway_async_jobs SET available_at=now()-interval '1 second',lease_until=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	replicaStore, err := controlstore.NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replicaStore.Close)
	replica := configurePostgresBackgroundAgent(t, h, replicaStore)
	if _, err := replica.ProcessAgentMCPBackground(t.Context()); err != nil {
		t.Fatal(err)
	}
	if readBackgroundAgentTask(t, replica, task.ID).Status.State != "TASK_STATE_FAILED" || len(llm.requests) != 1 || client.callCalls != 0 {
		t.Fatal("a restarted replica repeated unknown model or tool effects")
	}
	jobs, err := replicaStore.ClaimAsyncJobs(t.Context(), a2astate.AgentJobKind, 1, time.Minute)
	if err != nil || len(jobs) != 0 {
		t.Fatal("interrupted job was not settled")
	}
	if _, err := store.GetA2ATask(t.Context(), "another-owner", "research", task.ID); !errors.Is(err, a2astate.ErrNotFound) {
		t.Fatal("background task crossed owner boundary")
	}
}
