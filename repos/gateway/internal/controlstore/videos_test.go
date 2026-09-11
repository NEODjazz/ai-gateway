package controlstore

import (
	"errors"
	"testing"

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/videostate"
)

func TestPostgresVideoOwnershipLifecycleIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	_, err = store.pool.Exec(t.Context(), `CREATE TABLE gateway_video_jobs (video_id TEXT NOT NULL,owner_key TEXT NOT NULL,endpoint TEXT NOT NULL,model TEXT NOT NULL,deployment TEXT NOT NULL,snapshot JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(owner_key,video_id))`)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "a cat"
	record := videostate.Record{OwnerKey: "owner-a", Binding: provider.VideoBinding{Endpoint: "video", Model: "public-video", Deployment: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, Video: openai.Video{ID: "video_a", Object: "video", Model: "public-video", Status: "queued", Prompt: &prompt, Seconds: "4", Size: "720x1280"}}
	created, err := store.CreateVideoRecord(t.Context(), record, 1)
	if err != nil || created.CreatedAt.IsZero() || created.Video.Status != "queued" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err = store.GetVideoRecord(t.Context(), "owner-b", record.Video.ID); !errors.Is(err, videostate.ErrNotFound) {
		t.Fatalf("cross-owner err=%v", err)
	}
	otherOwner := record
	otherOwner.OwnerKey = "owner-b"
	if _, err = store.CreateVideoRecord(t.Context(), otherOwner, 1); err != nil {
		t.Fatalf("same provider ID for another owner: %v", err)
	}
	second := record
	second.Video.ID = "video_b"
	if _, err = store.CreateVideoRecord(t.Context(), second, 1); !errors.Is(err, videostate.ErrQuotaExceeded) {
		t.Fatalf("quota err=%v", err)
	}
	video := created.Video
	video.Status = "in_progress"
	updated, err := store.UpdateVideoRecord(t.Context(), record.OwnerKey, video)
	if err != nil || updated.Video.Status != "in_progress" || updated.UpdatedAt.Before(updated.CreatedAt) {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	page, next, err := store.ListVideoRecords(t.Context(), record.OwnerKey, 1, "")
	if err != nil || len(page) != 1 || next != "" || page[0].Video.ID != record.Video.ID {
		t.Fatalf("page=%+v next=%q err=%v", page, next, err)
	}
	if err = store.DeleteVideoRecord(t.Context(), "owner-c", record.Video.ID); !errors.Is(err, videostate.ErrNotFound) {
		t.Fatalf("cross-owner delete err=%v", err)
	}
	if err = store.DeleteVideoRecord(t.Context(), record.OwnerKey, record.Video.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetVideoRecord(t.Context(), record.OwnerKey, record.Video.ID); !errors.Is(err, videostate.ErrNotFound) {
		t.Fatalf("deleted err=%v", err)
	}
}

func TestPostgresVideoAndSettlementJobAreAtomicIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	_, err = store.pool.Exec(t.Context(), `CREATE TABLE gateway_video_jobs (video_id TEXT NOT NULL,owner_key TEXT NOT NULL,endpoint TEXT NOT NULL,model TEXT NOT NULL,deployment TEXT NOT NULL,snapshot JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(owner_key,video_id))`)
	if err != nil {
		t.Fatal(err)
	}
	prepareAsyncJobTable(t, store)
	record := videostate.Record{OwnerKey: "owner-a", Binding: provider.VideoBinding{Endpoint: "video", Model: "public-video", Deployment: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, Video: openai.Video{ID: "video_atomic", Object: "video", Model: "public-video", Status: "queued", Seconds: "4"}}
	job := asyncstate.Job{Kind: "video.settlement.v1", ResourceID: record.Video.ID, OwnerKey: record.OwnerKey, EndpointID: record.Binding.Endpoint, ExecutionID: "exec-video", Payload: []byte(`{"request_id":"exec-video"}`)}
	created, err := store.CreateVideoRecordWithJob(t.Context(), record, 1, job)
	if err != nil || created.Video.ID != record.Video.ID {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if found, err := store.HasAsyncJob(t.Context(), job.Kind, job.ResourceID, job.OwnerKey); err != nil || !found {
		t.Fatalf("job found=%t err=%v", found, err)
	}

	conflict := record
	conflict.OwnerKey = "owner-b"
	conflict.Video.ID = "video_rollback"
	conflictJob := job
	conflictJob.ResourceID = conflict.Video.ID
	conflictJob.OwnerKey = conflict.OwnerKey
	if _, err := store.CreateVideoRecordWithJob(t.Context(), conflict, 1, conflictJob); !errors.Is(err, asyncstate.ErrConflict) {
		t.Fatalf("conflict err=%v", err)
	}
	if _, err := store.GetVideoRecord(t.Context(), conflict.OwnerKey, conflict.Video.ID); !errors.Is(err, videostate.ErrNotFound) {
		t.Fatalf("video insert was not rolled back: %v", err)
	}
}

func TestPostgresVideoOwnershipRejectsInvalidInput(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.CreateVideoRecord(t.Context(), videostate.Record{}, 1); !errors.Is(err, videostate.ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
}
