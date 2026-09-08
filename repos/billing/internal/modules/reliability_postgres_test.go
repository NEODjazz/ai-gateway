package modules

import (
	"ai-gateway-billing/internal/openai"
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

func reliabilityTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("BILLING_POSTGRES_TEST_DSN")
	if dsn == "" {
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("BILLING_POSTGRES_TEST_DSN required")
		}
		t.Skip("BILLING_POSTGRES_TEST_DSN is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
func TestPostgresBudgetAndOutboxRollbackTogether(t *testing.T) {
	pool := reliabilityTestPool(t)
	ctx := context.Background()
	id := "atomic-fail-" + time.Now().Format("150405.000000000")
	repo, err := NewPostgresOutboxRepository(os.Getenv("BILLING_POSTGRES_TEST_DSN"), NoopUsageEventWriter{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	m := NewBillingModuleWithPricing(true, PricingConfig{Currency: "USD"})
	m.policy = NewPostgresBudgetPolicyChecker(os.Getenv("BILLING_POSTGRES_TEST_DSN"), time.Minute)
	m.durable = repo
	t.Cleanup(m.Close)
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `ALTER TABLE billing_outbox DROP CONSTRAINT IF EXISTS test_reject_atomic_event`)
		if err != nil {
			t.Error(err)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM billing_outbox WHERE payload->>'request_id'=$1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM billing_event_ledger WHERE request_id=$1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM billing_budget_reservations WHERE request_id=$1`, id)
	})
	req := RequestContext{RequestID: id, CredentialID: "key-atomic", UserID: "user-atomic", BillingPhase: "reserve", Request: openai.ChatCompletionRequest{Model: "model"}, Usage: &openai.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8}}
	if err := m.Handle(ctx, &req); err != nil {
		t.Fatal(err)
	}
	// Failure injection is confined to this disposable integration database.
	if _, err := pool.Exec(ctx, `ALTER TABLE billing_outbox ADD CONSTRAINT test_reject_atomic_event CHECK ((payload->>'request_id') NOT LIKE 'atomic-fail-%')`); err != nil {
		t.Fatal(err)
	}
	req.BillingPhase = "commit"
	if err := m.Handle(ctx, &req); err == nil {
		t.Fatal("expected outbox insert failure")
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM billing_budget_reservations WHERE request_id=$1`, id).Scan(&state); err != nil || state != "reserved" {
		t.Fatalf("budget committed without outbox: state=%s err=%v", state, err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE billing_outbox DROP CONSTRAINT test_reject_atomic_event`); err != nil {
		t.Fatal(err)
	}
	if err := m.Handle(ctx, &req); err != nil {
		t.Fatal(err)
	}
	if err := m.Handle(ctx, &req); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing_outbox WHERE event_id=$1`, id+":commit").Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate/missing outbox: count=%d err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM billing_budget_reservations WHERE request_id=$1`, id).Scan(&state); err != nil || state != "committed" {
		t.Fatal("budget not committed")
	}
}

func TestPostgresOutboxSurvivesWorkerRestartAndDeliveryFailure(t *testing.T) {
	pool := reliabilityTestPool(t)
	ctx := context.Background()
	id := "restart-" + time.Now().Format("150405.000000000")
	dsn := os.Getenv("BILLING_POSTGRES_TEST_DSN")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM billing_outbox WHERE event_id=$1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM billing_event_ledger WHERE event_id=$1`, id)
	})
	first, err := NewPostgresOutboxRepository(dsn, failedUsageWriter{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	created, err := first.Enqueue(ctx, BillingEvent{RequestID: id, EventID: id, Phase: "commit", TotalTokens: 42})
	if err != nil || !created {
		first.Close()
		t.Fatal("enqueue failed")
	}
	if _, err := first.DeliverOnce(ctx); err == nil {
		first.Close()
		t.Fatal("failure not reported")
	}
	first.Close()
	if _, err := pool.Exec(ctx, `UPDATE billing_outbox SET available_at=now(),locked_at=NULL WHERE event_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	writer := &recordingUsageWriter{}
	second, err := NewPostgresOutboxRepository(dsn, writer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	if delivered, err := second.DeliverOnce(ctx); err != nil || !delivered {
		t.Fatalf("restart delivery: %v %v", delivered, err)
	}
	if len(writer.events) != 1 || writer.events[0].TotalTokens != 42 {
		t.Fatal("persisted event lost")
	}
	if created, err := second.Enqueue(ctx, BillingEvent{RequestID: id, EventID: id, Phase: "commit"}); err != nil || created {
		t.Fatal("duplicate replay")
	}
	var attempts int
	var delivered bool
	if err := pool.QueryRow(ctx, `SELECT attempts,delivered_at IS NOT NULL FROM billing_outbox WHERE event_id=$1`, id).Scan(&attempts, &delivered); err != nil || attempts != 2 || !delivered {
		t.Fatal("delivery state not durable")
	}
}

func TestPostgresTokenBudgetCannotOverflow(t *testing.T) {
	pool := reliabilityTestPool(t)
	ctx := context.Background()
	team := "overflow-" + time.Now().Format("150405.000000000")
	if _, err := pool.Exec(ctx, `INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_tokens) VALUES('team',$1,'day','USD',1000)`, team); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM billing_budget_reservations WHERE team_id=$1`, team); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM billing_budget_policies WHERE scope_type='team' AND scope_id=$1`, team); err != nil {
			t.Error(err)
		}
	})
	checker := NewPostgresBudgetPolicyChecker(os.Getenv("BILLING_POSTGRES_TEST_DSN"), time.Minute)
	t.Cleanup(checker.Close)
	if err := checker.Apply(ctx, budgetTestEvent(team+"-first", team, 100)); err != nil {
		t.Fatal(err)
	}
	if err := checker.Apply(ctx, budgetTestEvent(team+"-large", team, int(^uint(0)>>1))); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("oversized reserve bypassed budget: %v", err)
	}
}
