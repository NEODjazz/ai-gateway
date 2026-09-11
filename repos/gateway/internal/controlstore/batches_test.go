package controlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/batchstate"
	"ai-gateway-gateway/internal/filestate"
)

func TestPostgresBatchLifecycleIsAtomicAndOwnerIsolatedIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	prepareBatchTables(t, store)
	file := filestate.File{ID: "file_input", OwnerKey: "owner-a", Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: 3, Content: []byte("{}\n")}
	if _, err = store.Create(t.Context(), file, 1024); err != nil {
		t.Fatal(err)
	}
	batch := batchstate.Batch{ID: "batch_a", OwnerKey: "owner-a", InputFileID: file.ID, Endpoint: "/v1/chat/completions", CompletionWindow: "24h", OutputExpirySeconds: 3600, Status: "queued", Metadata: []byte(`{}`), Identity: []byte(`{"credential_id":"credential"}`), Total: 2, ExpiresAt: time.Now().Add(24 * time.Hour)}
	items := []batchstate.Item{{BatchID: batch.ID, OwnerKey: batch.OwnerKey, Ordinal: 0, CustomID: "one", URL: batch.Endpoint, Body: []byte(`{"model":"a"}`), Identity: []byte(`{}`), State: "pending", ExecutionID: "exec_one"}, {BatchID: batch.ID, OwnerKey: batch.OwnerKey, Ordinal: 1, CustomID: "two", URL: batch.Endpoint, Body: []byte(`{"model":"b"}`), Identity: []byte(`{}`), State: "pending", ExecutionID: "exec_two"}}
	jobs := []asyncstate.Job{{Kind: batchJobKindForTest, ResourceID: "batch_a:0", OwnerKey: "owner-a", EndpointID: "gateway", ExecutionID: "exec_one", Payload: []byte(`{"ordinal":0}`)}, {Kind: batchJobKindForTest, ResourceID: "batch_a:1", OwnerKey: "owner-a", EndpointID: "gateway", ExecutionID: "exec_two", Payload: []byte(`{"ordinal":1}`)}}
	created, err := store.CreateBatch(t.Context(), batch, items, jobs, 10)
	if err != nil {
		t.Fatal(err)
	}
	if created.Total != 2 || created.Status != "queued" || created.OutputExpirySeconds != 3600 {
		t.Fatalf("created=%+v", created)
	}
	if _, err = store.GetBatch(t.Context(), "owner-b", batch.ID); !errors.Is(err, batchstate.ErrNotFound) {
		t.Fatalf("cross-owner error=%v", err)
	}
	claimed, err := store.ClaimAsyncJobs(t.Context(), batchJobKindForTest, 10, time.Minute)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claimed=%d err=%v", len(claimed), err)
	}
	started, err := store.StartBatch(t.Context(), batch.OwnerKey, batch.ID)
	if err != nil || started.Status != "in_progress" || started.InProgressAt == nil {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	items[0].Result = []byte(`{"custom_id":"one"}`)
	if err = store.StageBatchItem(t.Context(), items[0], false); err != nil {
		t.Fatal(err)
	}
	staged, err := store.GetBatchItem(t.Context(), batch.OwnerKey, batch.ID, 0)
	if err != nil || staged.State != "settling_success" || string(staged.Result) != string(items[0].Result) {
		t.Fatalf("staged=%+v err=%v", staged, err)
	}
	progress, err := store.FinishBatchItem(t.Context(), items[0], false)
	if err != nil || progress.Status != "in_progress" || progress.Completed != 1 {
		t.Fatalf("progress=%+v err=%v", progress, err)
	}
	items[1].Result = []byte(`{"custom_id":"two","error":{}}`)
	if err = store.StageBatchItem(t.Context(), items[1], true); err != nil {
		t.Fatal(err)
	}
	finalizing, err := store.FinishBatchItem(t.Context(), items[1], true)
	if err != nil || finalizing.Status != "finalizing" || finalizing.Failed != 1 {
		t.Fatalf("finalizing=%+v err=%v", finalizing, err)
	}
	completed, err := store.FinalizeBatch(t.Context(), batch.OwnerKey, batch.ID, "file_output", "file_errors")
	if err != nil || completed.Status != "completed" || completed.OutputFileID != "file_output" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestPostgresBatchCreationRollsBackItemsAndJobsIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	prepareBatchTables(t, store)
	file := filestate.File{ID: "file_input", OwnerKey: "owner", Filename: "input", Purpose: "batch", ContentType: "application/jsonl", Bytes: 3, Content: []byte("{}\n")}
	if _, err = store.Create(t.Context(), file, 1024); err != nil {
		t.Fatal(err)
	}
	batch := batchstate.Batch{ID: "batch_rollback", OwnerKey: "owner", InputFileID: file.ID, Endpoint: "/v1/chat/completions", CompletionWindow: "24h", Status: "queued", Metadata: []byte(`{}`), Identity: []byte(`{}`), Total: 1, ExpiresAt: time.Now().Add(time.Hour)}
	item := batchstate.Item{BatchID: batch.ID, OwnerKey: batch.OwnerKey, Ordinal: 0, CustomID: "one", URL: batch.Endpoint, Body: []byte(`{}`), Identity: []byte(`{}`), State: "pending", ExecutionID: "exec"}
	invalid := asyncstate.Job{Kind: batchJobKindForTest, ResourceID: "wrong", OwnerKey: "owner", EndpointID: "gateway", ExecutionID: "exec", Payload: []byte(`{}`)}
	if _, err = store.CreateBatch(t.Context(), batch, []batchstate.Item{item}, []asyncstate.Job{invalid}, 10); !errors.Is(err, batchstate.ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
	if _, err = store.GetBatch(t.Context(), "owner", batch.ID); !errors.Is(err, batchstate.ErrNotFound) {
		t.Fatalf("batch survived rollback: %v", err)
	}
}

const batchJobKindForTest = "batch.item.v1"

func prepareBatchTables(t *testing.T, store *PostgresStore) {
	t.Helper()
	prepareAsyncJobTable(t, store)
	_, err := store.pool.Exec(t.Context(), `CREATE TABLE gateway_files (id TEXT PRIMARY KEY,owner_key TEXT NOT NULL,filename TEXT NOT NULL,purpose TEXT NOT NULL,content_type TEXT NOT NULL,bytes BIGINT NOT NULL,content BYTEA NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),expires_at TIMESTAMPTZ);CREATE TABLE gateway_batches (id TEXT PRIMARY KEY,owner_key TEXT NOT NULL,input_file_id TEXT NOT NULL,endpoint TEXT NOT NULL,completion_window TEXT NOT NULL,output_expiry_seconds BIGINT NOT NULL DEFAULT 0,status TEXT NOT NULL,metadata JSONB NOT NULL,identity JSONB NOT NULL,total INTEGER NOT NULL,completed INTEGER NOT NULL DEFAULT 0,failed INTEGER NOT NULL DEFAULT 0,output_file_id TEXT NOT NULL DEFAULT '',error_file_id TEXT NOT NULL DEFAULT '',created_at TIMESTAMPTZ NOT NULL DEFAULT now(),in_progress_at TIMESTAMPTZ,expires_at TIMESTAMPTZ NOT NULL,finalizing_at TIMESTAMPTZ,completed_at TIMESTAMPTZ,failed_at TIMESTAMPTZ,expired_at TIMESTAMPTZ,cancelling_at TIMESTAMPTZ,cancelled_at TIMESTAMPTZ);CREATE TABLE gateway_batch_items (batch_id TEXT NOT NULL REFERENCES gateway_batches(id) ON DELETE CASCADE,owner_key TEXT NOT NULL,ordinal INTEGER NOT NULL,custom_id TEXT NOT NULL,url TEXT NOT NULL,body JSONB NOT NULL,identity JSONB NOT NULL,state TEXT NOT NULL,execution_id TEXT NOT NULL UNIQUE,result BYTEA,PRIMARY KEY(batch_id,ordinal),UNIQUE(batch_id,custom_id))`)
	if err != nil {
		t.Fatal(err)
	}
}

var _ = context.Background
