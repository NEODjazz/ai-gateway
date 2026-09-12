package controlstore

import (
	"errors"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/containerstate"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func TestPostgresContainerOwnershipLifecycleIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	_, err = store.pool.Exec(t.Context(), `CREATE TABLE gateway_containers (container_id TEXT PRIMARY KEY,owner_key TEXT NOT NULL,endpoint TEXT NOT NULL,model TEXT NOT NULL,deployment TEXT NOT NULL,snapshot JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		t.Fatal(err)
	}
	record := containerstate.Record{OwnerKey: "owner-a", Binding: provider.ContainerBinding{Endpoint: "sandbox", Model: "model-a", Deployment: strings.Repeat("a", 64)}, Container: openai.Container{ID: "cntr_a", Object: "container", Name: "analysis", Status: "running", MemoryLimit: "1g"}}
	created, err := store.CreateContainerRecord(t.Context(), record, 1)
	if err != nil || created.CreatedAt.IsZero() {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err = store.GetContainerRecord(t.Context(), "owner-b", record.Container.ID); !errors.Is(err, containerstate.ErrNotFound) {
		t.Fatalf("cross-owner err=%v", err)
	}
	otherOwner := record
	otherOwner.OwnerKey = "owner-b"
	if _, err = store.CreateContainerRecord(t.Context(), otherOwner, 1); !errors.Is(err, containerstate.ErrConflict) {
		t.Fatalf("provider ID collision err=%v", err)
	}
	second := record
	second.Container.ID = "cntr_b"
	if _, err = store.CreateContainerRecord(t.Context(), second, 1); !errors.Is(err, containerstate.ErrQuotaExceeded) {
		t.Fatalf("quota err=%v", err)
	}
	container := created.Container
	container.Status = "expired"
	if updated, err := store.UpdateContainerRecord(t.Context(), record.OwnerKey, container); err != nil || updated.Container.Status != "expired" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	page, next, err := store.ListContainerRecords(t.Context(), record.OwnerKey, 1, "")
	if err != nil || len(page) != 1 || next != "" {
		t.Fatalf("page=%+v next=%q err=%v", page, next, err)
	}
	pageRecord := record
	pageRecord.OwnerKey = "owner-page"
	pageRecord.Container.ID = "cntr_page_a"
	if _, err = store.CreateContainerRecord(t.Context(), pageRecord, 2); err != nil {
		t.Fatal(err)
	}
	pageRecord.Container.ID = "cntr_page_b"
	if _, err = store.CreateContainerRecord(t.Context(), pageRecord, 2); err != nil {
		t.Fatal(err)
	}
	firstPage, next, err := store.ListContainerRecords(t.Context(), pageRecord.OwnerKey, 1, "")
	if err != nil || len(firstPage) != 1 || next == "" {
		t.Fatalf("first page=%+v next=%q err=%v", firstPage, next, err)
	}
	secondPage, secondNext, err := store.ListContainerRecords(t.Context(), pageRecord.OwnerKey, 1, next)
	if err != nil || len(secondPage) != 1 || secondNext != "" || secondPage[0].Container.ID == firstPage[0].Container.ID {
		t.Fatalf("second page=%+v next=%q err=%v", secondPage, secondNext, err)
	}
	if err = store.DeleteContainerRecord(t.Context(), "owner-c", record.Container.ID); !errors.Is(err, containerstate.ErrNotFound) {
		t.Fatalf("cross-owner delete err=%v", err)
	}
	if err = store.DeleteContainerRecord(t.Context(), record.OwnerKey, record.Container.ID); err != nil {
		t.Fatal(err)
	}
}
