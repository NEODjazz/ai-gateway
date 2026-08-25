package modules

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestValidateAuditEvent(t *testing.T) {
	valid := ManagementAuditEvent{RequestID: "r", ActorID: "a", ActorCredentialID: "c", Action: "budget.update", TargetType: "budget", Outcome: "attempted"}
	if err := validateAuditEvent(valid); err != nil {
		t.Fatal(err)
	}
	valid.Outcome = "unknown"
	if err := validateAuditEvent(valid); !errors.Is(err, ErrInvalidAuditEvent) {
		t.Fatalf("err=%v", err)
	}
}

func TestPostgresAuditAppendAndCursorList(t *testing.T) {
	dsn := os.Getenv("BILLING_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("BILLING_POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	applyBudgetTestMigration(t, ctx, pool)
	pool.Close()
	store, err := NewPostgresAuditStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UTC().Format("150405.000000000")
	first, err := store.Append(ctx, ManagementAuditEvent{RequestID: "req-" + suffix, ActorID: "admin-" + suffix, ActorCredentialID: "credential", Action: "budget.update", TargetType: "budget", TargetID: "1", Outcome: "attempted"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(ctx, ManagementAuditEvent{RequestID: "req-" + suffix, ActorID: "admin-" + suffix, ActorCredentialID: "credential", Action: "budget.update", TargetType: "budget", TargetID: "1", Outcome: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.List(ctx, AuditFilter{ActorID: "admin-" + suffix, Action: "budget.update", Limit: 10})
	if err != nil || len(events) != 2 || events[0].ID != second.ID {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	page, err := store.List(ctx, AuditFilter{BeforeID: second.ID, ActorID: "admin-" + suffix, Limit: 10})
	if err != nil || len(page) != 1 || page[0].ID != first.ID {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}
