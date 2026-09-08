package modules

import (
	"context"
	"os"
	"testing"
	"time"
)

type channelUsageWriter struct {
	events chan BillingEvent
}

func (w channelUsageWriter) WriteUsageEvent(_ context.Context, event BillingEvent) error {
	w.events <- event
	return nil
}

func TestPostgresOutboxIsDurableAndIdempotent(t *testing.T) {
	dsn := os.Getenv("BILLING_POSTGRES_TEST_DSN")
	if dsn == "" {
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("BILLING_POSTGRES_TEST_DSN is required")
		}
		t.Skip("BILLING_POSTGRES_TEST_DSN is not set")
	}
	writer := channelUsageWriter{events: make(chan BillingEvent, 1)}
	repository, err := NewPostgresOutboxRepository(dsn, writer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	ctx := context.Background()
	if err := repository.Ready(ctx); err != nil {
		t.Fatalf("repository is not ready after migration: %v", err)
	}
	suffix := time.Now().UTC().Format("20060102150405.000000000")

	reserve := BillingEvent{EventID: "integration-reserve-" + suffix, RequestID: "integration-" + suffix, Phase: "reserve"}
	created, err := repository.Reserve(ctx, reserve)
	if err != nil || !created {
		t.Fatalf("first reservation failed: created=%v err=%v", created, err)
	}
	created, err = repository.Reserve(ctx, reserve)
	if err != nil || created {
		t.Fatalf("duplicate reservation was not ignored: created=%v err=%v", created, err)
	}

	commit := BillingEvent{EventID: "integration-commit-" + suffix, RequestID: "integration-" + suffix, Phase: "commit", TotalTokens: 42}
	created, err = repository.Enqueue(ctx, commit)
	if err != nil || !created {
		t.Fatalf("first enqueue failed: created=%v err=%v", created, err)
	}
	created, err = repository.Enqueue(ctx, commit)
	if err != nil || created {
		t.Fatalf("duplicate enqueue was not ignored: created=%v err=%v", created, err)
	}
	delivered, err := repository.DeliverOnce(ctx)
	if err != nil || !delivered {
		t.Fatalf("outbox delivery failed: delivered=%v err=%v", delivered, err)
	}
	select {
	case event := <-writer.events:
		if event.EventID != commit.EventID || event.TotalTokens != 42 {
			t.Fatalf("unexpected delivered event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for outbox delivery")
	}
}
