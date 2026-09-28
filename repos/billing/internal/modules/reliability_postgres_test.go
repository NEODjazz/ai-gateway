package modules

import (
	"ai-gateway-billing/internal/openai"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"math"
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

func TestPostgresExactZeroCommitReleasesReservation(t *testing.T) {
	pool := reliabilityTestPool(t)
	ctx := context.Background()
	id := "exact-zero-" + time.Now().Format("150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM billing_outbox WHERE payload->>'request_id'=$1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM billing_event_ledger WHERE request_id=$1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM billing_budget_reservations WHERE request_id=$1`, id)
	})
	repository, err := NewPostgresOutboxRepository(os.Getenv("BILLING_POSTGRES_TEST_DSN"), NoopUsageEventWriter{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	module := NewBillingModuleWithPricing(true, PricingConfig{Currency: "USD"})
	module.policy = NewPostgresBudgetPolicyChecker(os.Getenv("BILLING_POSTGRES_TEST_DSN"), time.Minute)
	module.durable = repository
	t.Cleanup(module.Close)
	req := RequestContext{
		RequestID: id, CredentialID: "key-zero", UserID: "user-zero", BillingPhase: "reserve",
		Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "reserve tokens"}}},
	}
	if err := module.Handle(ctx, &req); err != nil {
		t.Fatal(err)
	}
	req.BillingPhase = "commit"
	req.PostResponse = true
	req.Usage = &openai.Usage{}
	req.Metadata["usage.estimated"] = "false"
	if err := module.Handle(ctx, &req); err != nil {
		t.Fatal(err)
	}
	var state string
	var actualTokens int64
	if err := pool.QueryRow(ctx, `SELECT state,actual_tokens FROM billing_budget_reservations WHERE request_id=$1`, id).Scan(&state, &actualTokens); err != nil || state != "committed" || actualTokens != 0 {
		t.Fatalf("reservation state=%q actual_tokens=%d err=%v", state, actualTokens, err)
	}
	var totalTokens int
	var estimated bool
	if err := pool.QueryRow(ctx, `SELECT (payload->>'total_tokens')::integer,(payload->>'usage_estimated')::boolean FROM billing_outbox WHERE event_id=$1`, id+":commit").Scan(&totalTokens, &estimated); err != nil || totalTokens != 0 || estimated {
		t.Fatalf("outbox total_tokens=%d estimated=%t err=%v", totalTokens, estimated, err)
	}
}

func TestPostgresDeploymentPricingMatchesCommittedUsageAndOutbox(t *testing.T) {
	pool := reliabilityTestPool(t)
	ctx := t.Context()
	idPrefix := "deployment-pricing-" + time.Now().Format("150405.000000000")
	repository, err := NewPostgresOutboxRepository(os.Getenv("BILLING_POSTGRES_TEST_DSN"), NoopUsageEventWriter{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	module := NewBillingModuleWithPricing(true, PricingConfig{Currency: "USD"})
	module.policy = NewPostgresBudgetPolicyChecker(os.Getenv("BILLING_POSTGRES_TEST_DSN"), time.Minute)
	module.durable = repository
	t.Cleanup(module.Close)
	for index, test := range []struct {
		deployment, inputPrice, outputPrice string
		cost                                float64
	}{
		{deployment: "deployment-a", inputPrice: "1", outputPrice: "2", cost: 0.002},
		{deployment: "deployment-b", inputPrice: "3", outputPrice: "4", cost: 0.005},
	} {
		id := fmt.Sprintf("%s-%d", idPrefix, index)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM billing_outbox WHERE payload->>'request_id'=$1`, id)
			_, _ = pool.Exec(context.Background(), `DELETE FROM billing_event_ledger WHERE request_id=$1`, id)
			_, _ = pool.Exec(context.Background(), `DELETE FROM billing_budget_reservations WHERE request_id=$1`, id)
		})
		req := RequestContext{
			RequestID: id, CredentialID: "key-pricing", UserID: "user-pricing", BillingPhase: "reserve",
			Request: openai.ChatCompletionRequest{Model: "shared-model"},
			Usage:   &openai.Usage{PromptTokens: 1000},
			Metadata: map[string]string{
				"provider.endpoint.name":           test.deployment,
				"provider.endpoint.type":           "openai-compatible",
				"model_catalog.version":            "pricing-v1",
				"model_catalog.pricing_key":        test.deployment + "/shared-model",
				"model_catalog.input_cost_per_1m":  test.inputPrice,
				"model_catalog.output_cost_per_1m": test.outputPrice,
				"model_catalog.currency":           "USD",
			},
		}
		if err := module.Handle(ctx, &req); err != nil {
			t.Fatal(err)
		}
		req.BillingPhase = "commit"
		req.PostResponse = true
		req.Usage = &openai.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}
		req.Metadata["usage.estimated"] = "false"
		if err := module.Handle(ctx, &req); err != nil {
			t.Fatal(err)
		}
		var state string
		var actualTokens int
		var actualCost float64
		if err := pool.QueryRow(ctx, `SELECT state,actual_tokens,actual_cost::float8 FROM billing_budget_reservations WHERE request_id=$1`, id).Scan(&state, &actualTokens, &actualCost); err != nil || state != "committed" || actualTokens != 1500 || math.Abs(actualCost-test.cost) > 1e-10 {
			t.Fatalf("deployment=%s state=%s tokens=%d cost=%g err=%v", test.deployment, state, actualTokens, actualCost, err)
		}
		var outboxTokens int
		var outboxCost float64
		var pricingKey string
		var estimated bool
		if err := pool.QueryRow(ctx, `SELECT (payload->>'total_tokens')::int,(payload->>'cost')::float8,payload->>'pricing_key',(payload->>'usage_estimated')::boolean FROM billing_outbox WHERE event_id=$1`, id+":commit").Scan(&outboxTokens, &outboxCost, &pricingKey, &estimated); err != nil || outboxTokens != 1500 || math.Abs(outboxCost-test.cost) > 1e-10 || pricingKey != test.deployment+"/shared-model" || estimated {
			t.Fatalf("deployment=%s outbox tokens=%d cost=%g pricing=%q estimated=%t err=%v", test.deployment, outboxTokens, outboxCost, pricingKey, estimated, err)
		}
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
