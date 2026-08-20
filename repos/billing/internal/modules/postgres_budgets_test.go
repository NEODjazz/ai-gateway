package modules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresBudgetReservationsAreAtomicAndLifecycleAware(t *testing.T) {
	dsn := os.Getenv("BILLING_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("BILLING_POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	applyBudgetTestMigration(t, ctx, pool)

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	team := "atomic-" + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_tokens) VALUES('team',$1,'day','USD',10)`, team); err != nil {
		t.Fatal(err)
	}
	checker := NewPostgresBudgetPolicyChecker(dsn, time.Minute)
	defer checker.Close()
	if err := checker.Ready(ctx); err != nil {
		t.Fatal(err)
	}

	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			event := budgetTestEvent("concurrent-"+suffix+"-"+string(rune('a'+i)), team, 3)
			err := checker.Apply(ctx, event)
			if err == nil {
				allowed.Add(1)
				return
			}
			if !errors.Is(err, ErrBudgetExceeded) {
				t.Errorf("unexpected reserve error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := allowed.Load(); got != 3 {
		t.Fatalf("atomic token budget admitted %d reservations, want 3", got)
	}

	idempotentTeam := "idempotent-" + suffix
	idempotentRequest := "same-request-" + suffix
	var idempotentErrors atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := checker.Apply(ctx, budgetTestEvent(idempotentRequest, idempotentTeam, 1)); err != nil {
				idempotentErrors.Add(1)
			}
		}()
	}
	wg.Wait()
	if idempotentErrors.Load() != 0 {
		t.Fatalf("concurrent idempotent reserves returned %d errors", idempotentErrors.Load())
	}
	var duplicateRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing_budget_reservations WHERE request_id=$1`, idempotentRequest).Scan(&duplicateRows); err != nil || duplicateRows != 1 {
		t.Fatalf("concurrent idempotency produced rows=%d err=%v", duplicateRows, err)
	}

	var firstRequest string
	if err := pool.QueryRow(ctx, `SELECT request_id FROM billing_budget_reservations WHERE team_id=$1 AND state='reserved' ORDER BY request_id LIMIT 1`, team).Scan(&firstRequest); err != nil {
		t.Fatal(err)
	}
	if err := checker.Apply(ctx, budgetTestEvent(firstRequest, team, 3)); err != nil {
		t.Fatalf("idempotent reserve failed: %v", err)
	}
	cancel := budgetTestEvent(firstRequest, team, 0)
	cancel.Phase = "cancel"
	if err := checker.Apply(ctx, cancel); err != nil {
		t.Fatal(err)
	}
	if err := checker.Apply(ctx, budgetTestEvent(firstRequest, team, 3)); !errors.Is(err, ErrBillingConflict) {
		t.Fatalf("finalized request id was reusable, err=%v", err)
	}
	if err := checker.Apply(ctx, budgetTestEvent("after-cancel-"+suffix, team, 3)); err != nil {
		t.Fatalf("cancel did not release capacity: %v", err)
	}

	commit := budgetTestEvent("after-cancel-"+suffix, team, 4)
	commit.Phase = "commit"
	if err := checker.Apply(ctx, commit); err != nil {
		t.Fatalf("commit of actual usage failed: %v", err)
	}
	if err := checker.Apply(ctx, budgetTestEvent("over-actual-"+suffix, team, 1)); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("committed actual usage was not enforced, err=%v", err)
	}

	reused := budgetTestEvent(firstRequest, "another-team-"+suffix, 1)
	if err := checker.Apply(ctx, reused); err == nil || errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("request id reuse by another identity must be rejected, err=%v", err)
	}

	providerA, providerB := "provider-a-"+suffix, "provider-b-"+suffix
	if _, err := pool.Exec(ctx, `INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_tokens) VALUES('provider',$1,'day','USD',3),('provider',$2,'day','USD',3)`, providerA, providerB); err != nil {
		t.Fatal(err)
	}
	moving := budgetTestEvent("moving-"+suffix, "routing-"+suffix, 3)
	moving.ProviderEndpointName = providerA
	if err := checker.Apply(ctx, moving); err != nil {
		t.Fatal(err)
	}
	moving.ProviderEndpointName = providerB
	if err := checker.Apply(ctx, moving); err != nil {
		t.Fatalf("fallback reservation could not move providers: %v", err)
	}
	releasedA := budgetTestEvent("released-a-"+suffix, "routing-"+suffix, 3)
	releasedA.ProviderEndpointName = providerA
	if err := checker.Apply(ctx, releasedA); err != nil {
		t.Fatalf("old provider still consumed the moved reservation: %v", err)
	}
	fullB := budgetTestEvent("full-b-"+suffix, "routing-"+suffix, 1)
	fullB.ProviderEndpointName = providerB
	if err := checker.Apply(ctx, fullB); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("new provider did not receive moved reservation, err=%v", err)
	}

	costUser := "cost-user-" + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_cost) VALUES('user',$1,'month','USD',0.01)`, costUser); err != nil {
		t.Fatal(err)
	}
	costFirst := budgetTestEvent("cost-first-"+suffix, "cost-team-"+suffix, 0)
	costFirst.UserID, costFirst.Cost = costUser, 0.006
	if err := checker.Apply(ctx, costFirst); err != nil {
		t.Fatal(err)
	}
	costSecond := budgetTestEvent("cost-second-"+suffix, "cost-team-"+suffix, 0)
	costSecond.UserID, costSecond.Cost = costUser, 0.005
	if err := checker.Apply(ctx, costSecond); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("cost budget was not enforced, err=%v", err)
	}
	costFirst.Phase, costFirst.Cost = "cancel", 0
	if err := checker.Apply(ctx, costFirst); err != nil {
		t.Fatal(err)
	}
	if err := checker.Apply(ctx, costSecond); err != nil {
		t.Fatalf("cancel did not release cost budget: %v", err)
	}
}

func TestPostgresBudgetReservationExpires(t *testing.T) {
	dsn := os.Getenv("BILLING_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("BILLING_POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	applyBudgetTestMigration(t, ctx, pool)
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	team := "expiry-" + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_tokens) VALUES('team',$1,'day','USD',3)`, team); err != nil {
		t.Fatal(err)
	}
	checker := NewPostgresBudgetPolicyChecker(dsn, 20*time.Millisecond)
	defer checker.Close()
	if err := checker.Apply(ctx, budgetTestEvent("expiring-"+suffix, team, 3)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if err := checker.Apply(ctx, budgetTestEvent("after-expiry-"+suffix, team, 3)); err != nil {
		t.Fatalf("expired reservation still consumed budget: %v", err)
	}
}

func applyBudgetTestMigration(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "postgres", "004_budgets.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
}

func budgetTestEvent(requestID, team string, tokens int) BillingEvent {
	return BillingEvent{
		RequestID: requestID, TeamID: team, APIKeyFingerprint: "key-" + team,
		Model: "model", Provider: "provider", Phase: "reserve", TotalTokens: tokens,
		Currency: "USD", Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}
