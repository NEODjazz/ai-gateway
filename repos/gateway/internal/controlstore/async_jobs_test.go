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
	kind := "response-test:" + time.Now().UTC().Format("150405.000000000")
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM gateway_async_jobs WHERE kind=$1`, kind)
	})
	job := asyncstate.Job{Kind: kind, ResourceID: "resp_a", OwnerKey: "owner", EndpointID: "endpoint", ExecutionID: "exec_a", Payload: []byte(`{"model":"m"}`)}
	created, err := store.EnqueueAsyncJob(t.Context(), job)
	if err != nil || !created {
		t.Fatalf("created=%t err=%v", created, err)
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
