package modules

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIdentityBudgetQueryValidation(t *testing.T) {
	valid := IdentityBudgetQuery{ScopeType: "team", ScopeID: "platform", Limit: 25}
	for _, change := range []func(*IdentityBudgetQuery){func(q *IdentityBudgetQuery) { q.ScopeType = "global" }, func(q *IdentityBudgetQuery) { q.ScopeID = "" }, func(q *IdentityBudgetQuery) { q.ScopeID = " bad" }, func(q *IdentityBudgetQuery) { q.ScopeID = "bad\x00" }, func(q *IdentityBudgetQuery) { q.Limit = 101 }, func(q *IdentityBudgetQuery) { q.Offset = -1 }} {
		q := valid
		change(&q)
		if q.Valid() {
			t.Fatalf("accepted invalid query %+v", q)
		}
	}
	if !valid.Valid() {
		t.Fatal("rejected valid query")
	}
}

func TestPostgresIdentityBudgetPagesIntegration(t *testing.T) {
	dsn := os.Getenv("BILLING_POSTGRES_TEST_DSN")
	if dsn == "" {
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("BILLING_POSTGRES_TEST_DSN is required")
		}
		t.Skip("BILLING_POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "identity_budgets_" + hex.EncodeToString(nonce[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	applyBudgetTestMigration(t, ctx, pool)
	checker := &PostgresBudgetPolicyChecker{pool: pool}
	if _, err := pool.Exec(ctx, `INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_cost,max_tokens,enabled)
	 SELECT 'team','platform','day',CASE WHEN i=60 THEN 'EUR' ELSE 'USD' END,10,100,i<>59 FROM generate_series(1,60) i;
	 INSERT INTO billing_budget_policies(scope_type,scope_id,period,max_tokens) VALUES('user','platform','day',100),('team','foreign','day',100),('global','*','day',100)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO billing_budget_reservations(request_id,owner_key,team_id,user_id,currency,state,reserved_cost,reserved_tokens,actual_cost,actual_tokens,reservation_expires_at,created_at) VALUES
	 ('committed','key','platform','platform','USD','committed',8,80,2,20,$1,$2),
	 ('live','key','platform','platform','USD','reserved',1,10,0,0,$1,$2),
	 ('expired','key','platform','platform','USD','reserved',5,50,0,0,$2,$2),
	 ('canceled','key','platform','platform','USD','canceled',5,50,0,0,$1,$2),
	 ('eur','key','platform','platform','EUR','committed',0,0,4,40,$1,$2),
	 ('foreign','key','foreign','foreign','USD','committed',0,0,9,90,$1,$2),
	 ('previous','key','platform','platform','USD','committed',0,0,9,90,$1,$3)`, now.Add(time.Hour), now.Add(-time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	q := IdentityBudgetQuery{ScopeType: "team", ScopeID: "platform", Offset: 50, Limit: 25}
	page, err := checker.IdentityBudgetPage(ctx, q, now)
	if err != nil || page.Total != 60 || len(page.Data) != 10 || page.Offset != 50 || page.Limit != 25 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	for i, row := range page.Data {
		if row.Policy.ID != int64(i+51) || row.Policy.ScopeID != "platform" || row.Policy.ScopeType != "team" {
			t.Fatalf("unstable or escaped identity: %+v", row)
		}
		cost, tokens := float64(3), int64(30)
		if row.Policy.Currency == "EUR" {
			cost, tokens = 4, 40
		}
		if row.UsedCost != cost || row.UsedTokens != tokens || row.RemainingCost == nil || *row.RemainingCost != 10-cost || row.RemainingTokens == nil || *row.RemainingTokens != 100-tokens || !row.WindowEnd.Equal(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("incorrect usage or window: %+v", row)
		}
	}
	if page.Data[8].Policy.Enabled {
		t.Fatal("disabled policy omitted or changed")
	}
	q.Offset = 500
	empty, err := checker.IdentityBudgetPage(ctx, q, now)
	if err != nil || empty.Total != 60 || len(empty.Data) != 0 {
		t.Fatalf("empty page=%+v err=%v", empty, err)
	}
	q.ScopeType, q.Offset = "user", 0
	user, err := checker.IdentityBudgetPage(ctx, q, now)
	if err != nil || user.Total != 1 || len(user.Data) != 1 || user.Data[0].Policy.ScopeType != "user" || user.Data[0].UsedTokens != 30 {
		t.Fatalf("user page=%+v err=%v", user, err)
	}
	batch, err := checker.IdentityBudgetSummaries(ctx, IdentityBudgetSummariesQuery{ScopeType: "team", IDs: []string{"platform", "no-budgets", "foreign"}}, now)
	if err != nil || len(batch) != 3 || batch[0].ID != "platform" || batch[0].Total != 60 || len(batch[0].Data) != 2 || batch[1].Total != 0 || len(batch[1].Data) != 0 || batch[2].Total != 1 || batch[2].Data[0].UsedTokens != 90 {
		t.Fatalf("incorrect batch counts or scope: %+v err=%v", batch, err)
	}
	for _, summary := range batch[0].Data {
		if summary.UsedCost != 3 || summary.UsedTokens != 30 || summary.Policy.ScopeID != "platform" || summary.Policy.Currency != "USD" || !summary.Policy.Enabled || summary.WindowEnd != page.Data[0].WindowEnd {
			t.Fatalf("incorrect preview usage: %+v", summary)
		}
	}
	users, err := checker.IdentityBudgetSummaries(ctx, IdentityBudgetSummariesQuery{ScopeType: "user", IDs: []string{"platform", "foreign"}}, now)
	if err != nil || len(users) != 2 || users[0].Total != 1 || users[0].Data[0].Policy.ScopeType != "user" || users[1].Total != 0 {
		t.Fatalf("batch escaped scope type: %+v err=%v", users, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO billing_budget_policies(scope_type,scope_id,period,max_tokens,enabled) VALUES('team','preview','day',100,false),('team','preview','week',100,false),('team','preview','month',100,true)`); err != nil {
		t.Fatal(err)
	}
	previews, err := checker.IdentityBudgetSummaries(ctx, IdentityBudgetSummariesQuery{ScopeType: "team", IDs: []string{"preview"}}, now)
	if err != nil || previews[0].Total != 3 || len(previews[0].Data) != 2 || !previews[0].Data[0].Policy.Enabled || previews[0].Data[1].Policy.Enabled {
		t.Fatalf("enabled-first preview lost: %+v err=%v", previews, err)
	}
	ids := make([]string, 200)
	for i := range 199 {
		ids[i] = "no-budgets-" + strconv.Itoa(i)
	}
	ids[199] = "platform"
	large, err := checker.IdentityBudgetSummaries(ctx, IdentityBudgetSummariesQuery{ScopeType: "team", IDs: ids}, now)
	if err != nil || len(large) != 200 || large[199].Total != 60 || len(large[199].Data) != 2 {
		t.Fatalf("large page truncated: count=%d err=%v", len(large), err)
	}
}
