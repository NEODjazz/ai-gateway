package controlstore

import (
	"errors"
	"testing"

	"ai-gateway-gateway/internal/finetunestate"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func TestPostgresFineTuningOwnershipLifecycleIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	_, err = store.pool.Exec(t.Context(), `CREATE TABLE gateway_fine_tuning_jobs (job_id TEXT PRIMARY KEY,owner_key TEXT NOT NULL,endpoint TEXT NOT NULL,model TEXT NOT NULL,deployment TEXT NOT NULL,snapshot JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		t.Fatal(err)
	}
	record := finetunestate.Record{OwnerKey: "owner-a", Binding: provider.FineTuningBinding{Endpoint: "training", Model: "model", Deployment: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, Job: openai.FineTuningJob{ID: "ftjob_a", Object: "fine_tuning.job", Model: "model", TrainingFile: "file_a", Status: "queued"}}
	created, err := store.CreateFineTuningRecord(t.Context(), record, 1)
	if err != nil || created.CreatedAt.IsZero() || created.Job.Status != "queued" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err = store.GetFineTuningRecord(t.Context(), "owner-b", record.Job.ID); !errors.Is(err, finetunestate.ErrNotFound) {
		t.Fatalf("cross-owner err=%v", err)
	}
	second := record
	second.Job.ID = "ftjob_b"
	if _, err = store.CreateFineTuningRecord(t.Context(), second, 1); !errors.Is(err, finetunestate.ErrQuotaExceeded) {
		t.Fatalf("quota err=%v", err)
	}
	job := created.Job
	job.Status = "running"
	model := "ft:model:owner:suffix:1"
	job.FineTunedModel = &model
	updated, err := store.UpdateFineTuningRecord(t.Context(), record.OwnerKey, job)
	if err != nil || updated.Job.Status != "running" || updated.UpdatedAt.Before(updated.CreatedAt) {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	page, next, err := store.ListFineTuningRecords(t.Context(), record.OwnerKey, 1, "")
	if err != nil || len(page) != 1 || next != "" || page[0].Job.ID != record.Job.ID {
		t.Fatalf("page=%+v next=%q err=%v", page, next, err)
	}
	found, err := store.FindFineTuningRecordByModel(t.Context(), record.OwnerKey, model)
	if err != nil || found.Job.ID != record.Job.ID {
		t.Fatalf("found=%+v err=%v", found, err)
	}
	if _, err := store.FindFineTuningRecordByModel(t.Context(), "owner-b", model); !errors.Is(err, finetunestate.ErrNotFound) {
		t.Fatalf("cross-owner model lookup err=%v", err)
	}
}

func TestPostgresFineTuningOwnershipRejectsInvalidInput(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.CreateFineTuningRecord(t.Context(), finetunestate.Record{}, 1); !errors.Is(err, finetunestate.ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
}
