package controlstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
)

func TestPostgresAsyncJobLifecycleAndFencingIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	prepareAsyncJobTable(t, store)
	kind := "response-test:" + time.Now().UTC().Format("150405.000000000")
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM gateway_async_jobs WHERE kind=$1`, kind)
	})
	job := asyncstate.Job{Kind: kind, ResourceID: "resp_a", OwnerKey: "owner", EndpointID: "endpoint", ExecutionID: "exec_a", Payload: []byte(`{"model":"m"}`)}
	created, err := store.EnqueueAsyncJob(t.Context(), job)
	if err != nil || !created {
		t.Fatalf("created=%t err=%v", created, err)
	}
	if exists, err := store.HasAsyncJob(t.Context(), kind, job.ResourceID, job.OwnerKey); err != nil || !exists {
		t.Fatalf("pending exists=%t err=%v", exists, err)
	}
	if exists, err := store.HasAsyncJob(t.Context(), kind, job.ResourceID, "other-owner"); err != nil || exists {
		t.Fatalf("cross-owner exists=%t err=%v", exists, err)
	}
	created, err = store.EnqueueAsyncJob(t.Context(), job)
	if err != nil || created {
		t.Fatalf("idempotent created=%t err=%v", created, err)
	}
	conflict := job
	conflict.Payload = []byte(`{"model":"other"}`)
	if _, err := store.EnqueueAsyncJob(t.Context(), conflict); !errors.Is(err, asyncstate.ErrConflict) {
		t.Fatalf("conflict error=%v", err)
	}

	claimed, err := store.ClaimAsyncJobs(t.Context(), kind, 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 || claimed[0].LeaseGeneration != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := store.CompleteAsyncJob(t.Context(), kind, job.ResourceID, 2); !errors.Is(err, asyncstate.ErrLeaseLost) {
		t.Fatalf("stale completion error=%v", err)
	}
	if err := store.RetryAsyncJob(t.Context(), kind, job.ResourceID, 1, 0); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimAsyncJobs(t.Context(), kind, 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 2 || claimed[0].LeaseGeneration != 2 {
		t.Fatalf("reclaimed=%+v err=%v", claimed, err)
	}
	if err := store.CompleteAsyncJob(t.Context(), kind, job.ResourceID, claimed[0].LeaseGeneration); err != nil {
		t.Fatal(err)
	}
	if exists, err := store.HasAsyncJob(t.Context(), kind, job.ResourceID, job.OwnerKey); err != nil || exists {
		t.Fatalf("completed exists=%t err=%v", exists, err)
	}
	if jobs, err := store.ClaimAsyncJobs(t.Context(), kind, 1, time.Minute); err != nil || len(jobs) != 0 {
		t.Fatalf("completed jobs=%+v err=%v", jobs, err)
	}
}

func TestPostgresAsyncJobClaimIsExclusiveIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	prepareAsyncJobTable(t, store)
	kind := "claim-test:" + time.Now().UTC().Format("150405.000000000")
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM gateway_async_jobs WHERE kind=$1`, kind)
	})
	if _, err := store.EnqueueAsyncJob(t.Context(), asyncstate.Job{Kind: kind, ResourceID: "resp_one", OwnerKey: "owner", EndpointID: "endpoint", ExecutionID: "exec_one", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan []asyncstate.Job, 2)
	errorsFound := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			jobs, claimErr := store.ClaimAsyncJobs(context.Background(), kind, 1, time.Minute)
			results <- jobs
			errorsFound <- claimErr
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	close(errorsFound)
	claimed := 0
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	for jobs := range results {
		claimed += len(jobs)
	}
	if claimed != 1 {
		t.Fatalf("claimed jobs=%d, want 1", claimed)
	}
}

func prepareAsyncJobTable(t *testing.T, store *PostgresStore) {
	t.Helper()
	_, err := store.pool.Exec(t.Context(), `CREATE TABLE gateway_async_jobs (
		kind TEXT NOT NULL, resource_id TEXT NOT NULL, owner_key TEXT NOT NULL,
		endpoint_id TEXT NOT NULL, execution_id TEXT NOT NULL UNIQUE, payload BYTEA NOT NULL,
		state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','leased')),
		attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
		lease_generation BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
		available_at TIMESTAMPTZ NOT NULL DEFAULT now(), lease_until TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (kind,resource_id), CHECK (length(kind) BETWEEN 1 AND 64),
		CHECK (length(resource_id) BETWEEN 1 AND 256), CHECK (length(owner_key) BETWEEN 1 AND 256),
		CHECK (length(endpoint_id) BETWEEN 1 AND 128), CHECK (length(execution_id) BETWEEN 1 AND 128),
		CHECK (octet_length(payload) BETWEEN 1 AND 65536))`)
	if err != nil {
		t.Fatalf("create isolated async jobs table: %v", err)
	}
}

func TestAsyncJobValidation(t *testing.T) {
	valid := asyncstate.Job{Kind: "response", ResourceID: "resp_a", OwnerKey: "owner", EndpointID: "endpoint", ExecutionID: "exec", Payload: []byte(`{}`)}
	if !asyncstate.Valid(valid) {
		t.Fatal("valid job rejected")
	}
	invalid := valid
	invalid.ResourceID = "../escape"
	if asyncstate.Valid(invalid) {
		t.Fatal("invalid resource accepted")
	}
	invalid = valid
	invalid.Payload = make([]byte, asyncstate.MaxPayloadBytes+1)
	if asyncstate.Valid(invalid) {
		t.Fatal("oversized payload accepted")
	}
}
