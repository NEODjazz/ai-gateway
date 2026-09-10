package modules

import (
	"context"
	"errors"
	"math"
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
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("BILLING_POSTGRES_TEST_DSN is required")
		}
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

	managedProvider := "managed-provider-" + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_tokens) VALUES('provider',$1,'day','USD',3)`, managedProvider); err != nil {
		t.Fatal(err)
	}
	managedProviderFirst := budgetTestEvent("managed-provider-first-"+suffix, "managed-provider-team-"+suffix, 3)
	managedProviderFirst.ProviderID = managedProvider
	managedProviderFirst.ProviderEndpointName = "deployment-" + suffix
	managedProviderFirst.ProviderEndpointType = "openai-compatible"
	if err := checker.Apply(ctx, managedProviderFirst); err != nil {
		t.Fatalf("managed provider reservation failed: %v", err)
	}
	var storedProvider string
	if err := pool.QueryRow(ctx, `SELECT provider_name FROM billing_budget_reservations WHERE request_id=$1`, managedProviderFirst.RequestID).Scan(&storedProvider); err != nil || storedProvider != managedProvider {
		t.Fatalf("stored provider=%q err=%v", storedProvider, err)
	}
	managedProviderOver := budgetTestEvent("managed-provider-over-"+suffix, "managed-provider-team-"+suffix, 1)
	managedProviderOver.ProviderID = managedProvider
	managedProviderOver.ProviderEndpointName = "another-deployment-" + suffix
	managedProviderOver.ProviderEndpointType = "openai-compatible"
	if err := checker.Apply(ctx, managedProviderOver); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("managed provider budget was not enforced across deployments, err=%v", err)
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

	organization := "organization-" + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_tokens) VALUES('organization',$1,'day','USD',5)`, organization); err != nil {
		t.Fatal(err)
	}
	organizationFirst := budgetTestEvent("organization-first-"+suffix, "organization-team-"+suffix, 3)
	organizationFirst.OrganizationID = organization
	if err := checker.Apply(ctx, organizationFirst); err != nil {
		t.Fatalf("organization reservation failed: %v", err)
	}
	var storedOrganization string
	if err := pool.QueryRow(ctx, `SELECT organization_id FROM billing_budget_reservations WHERE request_id=$1`, organizationFirst.RequestID).Scan(&storedOrganization); err != nil || storedOrganization != organization {
		t.Fatalf("stored organization=%q err=%v", storedOrganization, err)
	}
	organizationRetry := budgetTestEvent(organizationFirst.RequestID, "organization-team-"+suffix, 3)
	organizationRetry.OrganizationID = "changed-organization"
	if err := checker.Apply(ctx, organizationRetry); err != nil || organizationRetry.OrganizationID != organization {
		t.Fatalf("organization snapshot was not preserved: organization=%q err=%v", organizationRetry.OrganizationID, err)
	}
	organizationOver := budgetTestEvent("organization-over-"+suffix, "organization-team-"+suffix, 3)
	organizationOver.OrganizationID = organization
	if err := checker.Apply(ctx, organizationOver); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("organization budget was not enforced: %v", err)
	}
	otherOrganization := budgetTestEvent("organization-other-"+suffix, "organization-team-"+suffix, 3)
	otherOrganization.OrganizationID = "other-" + organization
	if err := checker.Apply(ctx, otherOrganization); err != nil {
		t.Fatalf("organization budget leaked across organizations: %v", err)
	}

	tagA, tagB := "tag-a-"+suffix, "tag-b-"+suffix
	if _, err := pool.Exec(ctx, `INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_tokens) VALUES('tag',$1,'day','USD',5),('tag',$2,'day','USD',10)`, tagA, tagB); err != nil {
		t.Fatal(err)
	}
	tagged := budgetTestEvent("tagged-"+suffix, "tag-team-"+suffix, 3)
	tagged.Tags = []string{tagA, tagB}
	if err := checker.Apply(ctx, tagged); err != nil {
		t.Fatalf("multi-tag reservation failed: %v", err)
	}
	var storedTags []string
	if err := pool.QueryRow(ctx, `SELECT tags FROM billing_budget_reservations WHERE request_id=$1`, tagged.RequestID).Scan(&storedTags); err != nil || len(storedTags) != 2 {
		t.Fatalf("stored tags=%v err=%v", storedTags, err)
	}
	overTagA := budgetTestEvent("over-tag-a-"+suffix, "tag-team-"+suffix, 3)
	overTagA.Tags = []string{tagA}
	if err := checker.Apply(ctx, overTagA); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("tag A budget was not enforced, err=%v", err)
	}
	withinTagB := budgetTestEvent("within-tag-b-"+suffix, "tag-team-"+suffix, 6)
	withinTagB.Tags = []string{tagB}
	if err := checker.Apply(ctx, withinTagB); err != nil {
		t.Fatalf("independent tag B budget rejected valid usage: %v", err)
	}
	overTagB := budgetTestEvent("over-tag-b-"+suffix, "tag-team-"+suffix, 2)
	overTagB.Tags = []string{tagB}
	if err := checker.Apply(ctx, overTagB); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("tag B budget was not enforced, err=%v", err)
	}
	retryWithChangedTags := budgetTestEvent(tagged.RequestID, "tag-team-"+suffix, 3)
	retryWithChangedTags.Tags = []string{tagB}
	if err := checker.Apply(ctx, retryWithChangedTags); err != nil || len(retryWithChangedTags.Tags) != 2 || retryWithChangedTags.Tags[0] != tagA {
		t.Fatalf("reservation tag snapshot was not preserved: tags=%v err=%v", retryWithChangedTags.Tags, err)
	}

	pricingRequest := "pricing-snapshot-" + suffix
	pricingReserve := budgetTestEvent(pricingRequest, "pricing-team-"+suffix, 300)
	pricingReserve.InputTokens, pricingReserve.OutputTokens, pricingReserve.InputCharacters, pricingReserve.InputPages, pricingReserve.InputAudioMilliseconds, pricingReserve.SearchRequests = 100, 200, 1000, 3, 90000, 5
	pricingReserve.CatalogVersion, pricingReserve.PricingKey = "v1", "provider/model@v1"
	pricingReserve.InputCostPer1M, pricingReserve.OutputCostPer1M, pricingReserve.SearchCostPer1K, pricingReserve.CharacterCostPer1M, pricingReserve.PageCostPer1K = 1, 2, 10, 15, 100
	pricingReserve.AudioCostPerMinute = 0.12
	pricingReserve.Cost = pricingCost(pricingReserve.InputTokens, pricingReserve.OutputTokens, pricingReserve.InputCharacters, pricingReserve.InputPages, pricingReserve.InputAudioMilliseconds, pricingReserve.SearchRequests, PricingSnapshot{InputCostPer1M: 1, OutputCostPer1M: 2, SearchCostPer1K: 10, CharacterCostPer1M: 15, PageCostPer1K: 100, AudioCostPerMinute: 0.12})
	if err := checker.Apply(ctx, pricingReserve); err != nil {
		t.Fatal(err)
	}
	pricingCommit := budgetTestEvent(pricingRequest, "pricing-team-"+suffix, 100)
	pricingCommit.Phase = "commit"
	pricingCommit.InputTokens, pricingCommit.OutputTokens, pricingCommit.InputCharacters, pricingCommit.InputPages, pricingCommit.InputAudioMilliseconds, pricingCommit.SearchRequests = 50, 50, 500, 2, 30000, 2
	pricingCommit.CatalogVersion, pricingCommit.PricingKey = "v2", "provider/model@v2"
	pricingCommit.InputCostPer1M, pricingCommit.OutputCostPer1M, pricingCommit.SearchCostPer1K, pricingCommit.CharacterCostPer1M, pricingCommit.PageCostPer1K = 100, 200, 1000, 1500, 2000
	pricingCommit.AudioCostPerMinute = 12
	pricingCommit.Cost = pricingCost(50, 50, 500, 2, 30000, 2, PricingSnapshot{InputCostPer1M: 100, OutputCostPer1M: 200, SearchCostPer1K: 1000, CharacterCostPer1M: 1500, PageCostPer1K: 2000, AudioCostPerMinute: 12})
	if err := checker.Apply(ctx, pricingCommit); err != nil {
		t.Fatal(err)
	}
	expectedPinnedCost := pricingCost(50, 50, 500, 2, 30000, 2, PricingSnapshot{InputCostPer1M: 1, OutputCostPer1M: 2, SearchCostPer1K: 10, CharacterCostPer1M: 15, PageCostPer1K: 100, AudioCostPerMinute: 0.12})
	if pricingCommit.CatalogVersion != "v1" || pricingCommit.PricingKey != "provider/model@v1" || pricingCommit.SearchCostPer1K != 10 || pricingCommit.CharacterCostPer1M != 15 || pricingCommit.PageCostPer1K != 100 || pricingCommit.AudioCostPerMinute != 0.12 || math.Abs(pricingCommit.Cost-expectedPinnedCost) > 1e-12 {
		t.Fatalf("commit did not use reserved pricing snapshot: %+v", pricingCommit)
	}
}

func TestPostgresBudgetReservationExpires(t *testing.T) {
	dsn := os.Getenv("BILLING_POSTGRES_TEST_DSN")
	if dsn == "" {
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("BILLING_POSTGRES_TEST_DSN is required")
		}
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

func TestPostgresBudgetManagementLifecycleAndSummary(t *testing.T) {
	dsn := os.Getenv("BILLING_POSTGRES_TEST_DSN")
	if dsn == "" {
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("BILLING_POSTGRES_TEST_DSN is required")
		}
		t.Skip("BILLING_POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	applyBudgetTestMigration(t, ctx, pool)
	checker := NewPostgresBudgetPolicyChecker(dsn, time.Minute)
	defer checker.Close()
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	team := "managed-" + suffix
	maxTokens := int64(100)
	created, err := checker.CreateBudgetPolicy(ctx, BudgetPolicySpec{ScopeType: "team", ScopeID: team, Period: "day", MaxTokens: &maxTokens})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID <= 0 || created.Currency != "USD" || !created.Enabled {
		t.Fatalf("created=%+v", created)
	}
	if err := checker.Apply(ctx, budgetTestEvent("managed-request-"+suffix, team, 25)); err != nil {
		t.Fatal(err)
	}
	foreignCurrency := budgetTestEvent("managed-eur-request-"+suffix, team, 40)
	foreignCurrency.Currency = "EUR"
	if err := checker.Apply(ctx, foreignCurrency); err != nil {
		t.Fatal(err)
	}
	summary, found, err := checker.BudgetSummary(ctx, created.ID, time.Now())
	if err != nil || !found {
		t.Fatalf("summary found=%v err=%v", found, err)
	}
	if summary.UsedTokens != 25 || summary.RemainingTokens == nil || *summary.RemainingTokens != 75 {
		t.Fatalf("summary=%+v", summary)
	}
	summaries, err := checker.ListBudgetSummaries(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var listed *BudgetSummary
	for index := range summaries {
		if summaries[index].Policy.ID == created.ID {
			listed = &summaries[index]
			break
		}
	}
	if listed == nil || listed.UsedTokens != 25 || listed.RemainingTokens == nil || *listed.RemainingTokens != 75 {
		t.Fatalf("listed summary=%+v", listed)
	}
	projections, err := checker.KeyBudgetProjections(ctx, []KeyBudgetSubject{{KeyID: "key-" + team, TeamID: team}}, time.Now())
	if err != nil || len(projections) != 1 || len(projections[0].Policies) != 1 || projections[0].Policies[0].UsedTokens != 25 || projections[0].Policies[0].RemainingTokens == nil || *projections[0].Policies[0].RemainingTokens != 75 {
		t.Fatalf("projections=%+v err=%v", projections, err)
	}
	enabled := true
	maxTokens = 200
	updated, found, err := checker.UpdateBudgetPolicy(ctx, created.ID, BudgetPolicySpec{ScopeType: "team", ScopeID: team, Period: "week", Currency: "EUR", MaxTokens: &maxTokens, Enabled: &enabled})
	if err != nil || !found || updated.Period != "week" || updated.Currency != "EUR" {
		t.Fatalf("updated=%+v found=%v err=%v", updated, found, err)
	}
	if disabled, err := checker.DisableBudgetPolicy(ctx, created.ID); err != nil || !disabled {
		t.Fatalf("disabled=%v err=%v", disabled, err)
	}
	got, found, err := checker.GetBudgetPolicy(ctx, created.ID)
	if err != nil || !found || got.Enabled {
		t.Fatalf("got=%+v found=%v err=%v", got, found, err)
	}
}

func applyBudgetTestMigration(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, name := range []string{"004_budgets.sql", "005_pricing_snapshots.sql", "006_management_audit.sql", "007_tag_budgets.sql", "008_organization_budgets.sql", "010_billing_server_tools.sql", "011_billing_characters.sql", "012_billing_pages.sql", "013_billing_audio_duration.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
}

func budgetTestEvent(requestID, team string, tokens int) *BillingEvent {
	return &BillingEvent{
		RequestID: requestID, TeamID: team, APIKeyFingerprint: "key-" + team,
		Model: "model", Provider: "provider", Phase: "reserve", TotalTokens: tokens,
		Currency: "USD", Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

func TestBudgetProviderIdentity(t *testing.T) {
	event := BillingEvent{
		ProviderID:           "azure-open-ai",
		ProviderEndpointName: "azure-open-ai-gpt-5.6-luna",
		ProviderEndpointType: "openai-compatible",
		Provider:             "legacy-provider",
	}

	if got := budgetProviderName(event); got != event.ProviderID {
		t.Fatalf("budgetProviderName()=%q, want managed provider ID %q", got, event.ProviderID)
	}
	wantScopes := []string{event.ProviderID, event.ProviderEndpointName, event.ProviderEndpointType, event.Provider}
	gotScopes := budgetProviderScopes(event)
	if len(gotScopes) != len(wantScopes) {
		t.Fatalf("budgetProviderScopes()=%v, want %v", gotScopes, wantScopes)
	}
	for index := range wantScopes {
		if gotScopes[index] != wantScopes[index] {
			t.Fatalf("budgetProviderScopes()=%v, want %v", gotScopes, wantScopes)
		}
	}

	legacyReservation := budgetReservation{
		ProviderName: event.ProviderEndpointName,
		ProviderType: event.ProviderEndpointType,
		Model:        "gpt-5.6-luna",
	}
	event.Model = legacyReservation.Model
	if !samePricingRoute(legacyReservation, event) {
		t.Fatal("legacy endpoint-name reservation no longer matches its managed provider route")
	}
}
