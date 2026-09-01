package modules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type budgetPolicy struct {
	ID        int64
	ScopeType string
	ScopeID   string
	Period    string
	MaxCost   float64
	MaxTokens int64
}

type PostgresBudgetPolicyChecker struct {
	pool    *pgxpool.Pool
	ttl     time.Duration
	initErr error
}

type budgetReservation struct {
	State           string
	Owner           string
	Tags            []string
	OrganizationID  string
	ProviderName    string
	ProviderType    string
	Model           string
	Currency        string
	CatalogVersion  string
	PricingKey      string
	InputCostPer1M  float64
	OutputCostPer1M float64
}

func NewPostgresBudgetPolicyChecker(dsn string, ttl time.Duration) *PostgresBudgetPolicyChecker {
	checker := &PostgresBudgetPolicyChecker{ttl: ttl}
	if strings.TrimSpace(dsn) == "" {
		checker.initErr = errors.New("POSTGRES_DSN is required when budgets or quotas are enabled")
		return checker
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		checker.initErr = errors.New("invalid postgres budget configuration")
		return checker
	}
	if ttl <= 0 {
		checker.ttl = 15 * time.Minute
	}
	checker.pool = pool
	return checker
}

func (c *PostgresBudgetPolicyChecker) Ready(ctx context.Context) error {
	if c.initErr != nil {
		return c.initErr
	}
	if err := c.pool.Ping(ctx); err != nil {
		return errors.New("billing policy postgres is unavailable")
	}
	var policies, reservations, pricingSnapshots, tagSnapshots, organizationSnapshots bool
	if err := c.pool.QueryRow(ctx, `
		SELECT to_regclass('public.billing_budget_policies') IS NOT NULL,
		       to_regclass('public.billing_budget_reservations') IS NOT NULL,
		       EXISTS (SELECT 1 FROM information_schema.columns
		               WHERE table_schema='public' AND table_name='billing_budget_reservations'
		                 AND column_name='catalog_version'),
		       EXISTS (SELECT 1 FROM information_schema.columns
		               WHERE table_schema='public' AND table_name='billing_budget_reservations'
		                 AND column_name='tags'),
		       EXISTS (SELECT 1 FROM information_schema.columns
		               WHERE table_schema='public' AND table_name='billing_budget_reservations'
		                 AND column_name='organization_id')`).Scan(&policies, &reservations, &pricingSnapshots, &tagSnapshots, &organizationSnapshots); err != nil || !policies || !reservations || !pricingSnapshots || !tagSnapshots || !organizationSnapshots {
		return errors.New("billing budget migration is not applied")
	}
	return nil
}

func (c *PostgresBudgetPolicyChecker) Close() {
	if c != nil && c.pool != nil {
		c.pool.Close()
	}
}

func (c *PostgresBudgetPolicyChecker) Apply(ctx context.Context, event *BillingEvent) error {
	if event == nil {
		return errors.New("billing event is required")
	}
	if c.initErr != nil {
		return c.initErr
	}
	if event.RequestID == "" {
		return errors.New("request_id is required for budget accounting")
	}
	if event.Phase != "reserve" && event.Phase != "commit" && event.Phase != "cancel" {
		return fmt.Errorf("invalid budget phase %q", event.Phase)
	}
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, event.RequestID); err != nil {
		return err
	}
	reservation, found, err := reservationState(ctx, tx, event.RequestID)
	if err != nil {
		return err
	}
	if found && reservation.Owner != reservationOwner(*event) {
		return fmt.Errorf("%w: request_id belongs to another billing identity", ErrBillingConflict)
	}
	if found {
		// Tag- and organization-scoped budgets must use the identity snapshot captured by the
		// original reservation, including during retries, fallbacks and commit.
		event.Tags = append([]string(nil), reservation.Tags...)
		event.OrganizationID = reservation.OrganizationID
	}
	if found && (event.Phase == "commit" || (event.Phase == "reserve" && samePricingRoute(reservation, *event))) {
		applyReservationPricing(event, reservation)
	}
	policies, err := applicableBudgetPolicies(ctx, tx, *event)
	if err != nil {
		return err
	}

	switch event.Phase {
	case "reserve":
		if found {
			if reservation.State != "reserved" {
				return fmt.Errorf("%w: request lifecycle is already %s", ErrBillingConflict, reservation.State)
			}
			if err := checkBudgetPolicies(ctx, tx, policies, *event); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				UPDATE billing_budget_reservations
				SET provider_name=$2, provider_type=$3, model=$4, currency=$5,
				    reserved_cost=$6, reserved_tokens=$7, catalog_version=$8,
				    pricing_key=$9, input_cost_per_1m=$10, output_cost_per_1m=$11, updated_at=now()
				WHERE request_id=$1`, event.RequestID, budgetProviderName(*event),
				budgetProviderType(*event), event.Model, event.Currency, event.Cost, event.TotalTokens,
				event.CatalogVersion, event.PricingKey, event.InputCostPer1M, event.OutputCostPer1M)
			break
		}
		if err := checkBudgetPolicies(ctx, tx, policies, *event); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO billing_budget_reservations
			(request_id, owner_key, credential_id, user_id, team_id, organization_id, tags, provider_name,
			 provider_type, model, currency, state, reserved_cost, reserved_tokens,
			 catalog_version, pricing_key, input_cost_per_1m, output_cost_per_1m,
			 reservation_expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'reserved',$12,$13,$14,$15,$16,$17,$18)`,
			event.RequestID, reservationOwner(*event), event.APIKeyFingerprint, event.UserID,
			event.TeamID, event.OrganizationID, budgetTags(*event), budgetProviderName(*event), budgetProviderType(*event), event.Model,
			event.Currency, event.Cost, event.TotalTokens, event.CatalogVersion, event.PricingKey,
			event.InputCostPer1M, event.OutputCostPer1M, time.Now().UTC().Add(c.ttl))
	case "commit":
		if found && reservation.State == "committed" {
			return tx.Commit(ctx)
		}
		if found && reservation.State == "canceled" {
			return fmt.Errorf("%w: canceled request cannot be committed", ErrBillingConflict)
		}
		if found {
			_, err = tx.Exec(ctx, `
				UPDATE billing_budget_reservations
				SET state='committed', actual_cost=$2, actual_tokens=$3, updated_at=now()
				WHERE request_id=$1`, event.RequestID, event.Cost, event.TotalTokens)
		} else if !found {
			if event.CatalogVersion == "" {
				return errors.New("commit has no reservation or resolved pricing snapshot")
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO billing_budget_reservations
				(request_id, owner_key, credential_id, user_id, team_id, organization_id, tags, provider_name,
				 provider_type, model, currency, state, actual_cost, actual_tokens,
				 catalog_version, pricing_key, input_cost_per_1m, output_cost_per_1m,
				 reservation_expires_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'committed',$12,$13,$14,$15,$16,$17,now())`,
				event.RequestID, reservationOwner(*event), event.APIKeyFingerprint, event.UserID,
				event.TeamID, event.OrganizationID, budgetTags(*event), budgetProviderName(*event), budgetProviderType(*event), event.Model,
				event.Currency, event.Cost, event.TotalTokens, event.CatalogVersion, event.PricingKey,
				event.InputCostPer1M, event.OutputCostPer1M)
		}
	case "cancel":
		if found && reservation.State == "committed" {
			return fmt.Errorf("%w: committed request cannot be canceled", ErrBillingConflict)
		}
		if found && reservation.State == "reserved" {
			_, err = tx.Exec(ctx, `
				UPDATE billing_budget_reservations
				SET state='canceled', reserved_cost=0, reserved_tokens=0, updated_at=now()
				WHERE request_id=$1`, event.RequestID)
		} else if !found {
			_, err = tx.Exec(ctx, `
				INSERT INTO billing_budget_reservations
				(request_id, owner_key, credential_id, user_id, team_id, organization_id, tags, provider_name,
				 provider_type, model, currency, state, reservation_expires_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'canceled',now())`,
				event.RequestID, reservationOwner(*event), event.APIKeyFingerprint, event.UserID,
				event.TeamID, event.OrganizationID, budgetTags(*event), budgetProviderName(*event), budgetProviderType(*event), event.Model, event.Currency)
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func checkBudgetPolicies(ctx context.Context, tx pgx.Tx, policies []budgetPolicy, event BillingEvent) error {
	for _, policy := range policies {
		cost, tokens, err := budgetUsage(ctx, tx, policy, event, time.Now().UTC())
		if err != nil {
			return err
		}
		if policy.MaxCost > 0 && cost+event.Cost > policy.MaxCost+1e-12 {
			return &BudgetExceededError{PolicyID: policy.ID, Dimension: "cost"}
		}
		if policy.MaxTokens > 0 && tokens+int64(event.TotalTokens) > policy.MaxTokens {
			return &BudgetExceededError{PolicyID: policy.ID, Dimension: "tokens"}
		}
	}
	return nil
}

type BudgetExceededError struct {
	PolicyID  int64
	Dimension string
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("%s: policy %d %s limit", ErrBudgetExceeded, e.PolicyID, e.Dimension)
}

func (e *BudgetExceededError) Unwrap() error { return ErrBudgetExceeded }

func applicableBudgetPolicies(ctx context.Context, tx pgx.Tx, event BillingEvent) ([]budgetPolicy, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, scope_type, scope_id, period,
		       COALESCE(max_cost, 0)::float8, COALESCE(max_tokens, 0)
		FROM billing_budget_policies
		WHERE enabled AND currency=$1 AND (
			scope_type='global'
			OR (scope_type='key' AND scope_id=$2)
			OR (scope_type='user' AND scope_id=$3)
			OR (scope_type='team' AND scope_id=$4)
			OR (scope_type='organization' AND scope_id=$5)
			OR (scope_type='model' AND scope_id=$6)
			OR (scope_type='provider' AND scope_id = ANY($7))
			OR (scope_type='tag' AND scope_id = ANY($8))
		)
		ORDER BY id
		FOR UPDATE`, event.Currency, event.APIKeyFingerprint, event.UserID, event.TeamID, event.OrganizationID,
		event.Model, []string{budgetProviderName(event), budgetProviderType(event)}, budgetTags(event))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var policies []budgetPolicy
	for rows.Next() {
		var policy budgetPolicy
		if err := rows.Scan(&policy.ID, &policy.ScopeType, &policy.ScopeID, &policy.Period, &policy.MaxCost, &policy.MaxTokens); err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func reservationState(ctx context.Context, tx pgx.Tx, requestID string) (budgetReservation, bool, error) {
	var reservation budgetReservation
	err := tx.QueryRow(ctx, `
		SELECT state, owner_key, tags, organization_id, provider_name, provider_type, model, currency,
		       catalog_version, pricing_key, input_cost_per_1m::float8, output_cost_per_1m::float8
		FROM billing_budget_reservations WHERE request_id=$1 FOR UPDATE`, requestID).Scan(
		&reservation.State, &reservation.Owner, &reservation.Tags, &reservation.OrganizationID, &reservation.ProviderName, &reservation.ProviderType,
		&reservation.Model, &reservation.Currency, &reservation.CatalogVersion, &reservation.PricingKey,
		&reservation.InputCostPer1M, &reservation.OutputCostPer1M)
	if errors.Is(err, pgx.ErrNoRows) {
		return budgetReservation{}, false, nil
	}
	return reservation, err == nil, err
}

func samePricingRoute(reservation budgetReservation, event BillingEvent) bool {
	return reservation.ProviderName == budgetProviderName(event) &&
		reservation.ProviderType == budgetProviderType(event) && reservation.Model == event.Model
}

func applyReservationPricing(event *BillingEvent, reservation budgetReservation) {
	event.Currency = reservation.Currency
	event.CatalogVersion = reservation.CatalogVersion
	event.PricingKey = reservation.PricingKey
	event.InputCostPer1M = reservation.InputCostPer1M
	event.OutputCostPer1M = reservation.OutputCostPer1M
	event.Cost = pricingCost(event.InputTokens, event.OutputTokens, PricingSnapshot{
		InputCostPer1M: reservation.InputCostPer1M, OutputCostPer1M: reservation.OutputCostPer1M,
	})
}

func budgetUsage(ctx context.Context, tx pgx.Tx, policy budgetPolicy, event BillingEvent, now time.Time) (float64, int64, error) {
	return budgetUsageForPolicy(ctx, tx, policy, event.RequestID, now)
}

type budgetUsageQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func budgetUsageForPolicy(ctx context.Context, query budgetUsageQuerier, policy budgetPolicy, excludedRequestID string, now time.Time) (float64, int64, error) {
	start := budgetPeriodStart(now, policy.Period)
	var cost float64
	var tokens int64
	err := query.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN state='committed' THEN actual_cost ELSE reserved_cost END),0)::float8,
		       COALESCE(SUM(CASE WHEN state='committed' THEN actual_tokens ELSE reserved_tokens END),0)
		FROM billing_budget_reservations
		WHERE created_at >= $1
		  AND (state='committed' OR (state='reserved' AND reservation_expires_at > now()))
		  AND request_id <> $4
		  AND CASE $2
			WHEN 'global' THEN true
			WHEN 'key' THEN credential_id=$3
			WHEN 'user' THEN user_id=$3
			WHEN 'team' THEN team_id=$3
			WHEN 'organization' THEN organization_id=$3
			WHEN 'model' THEN model=$3
			WHEN 'provider' THEN provider_name=$3 OR provider_type=$3
			WHEN 'tag' THEN $3 = ANY(tags)
			ELSE false
		  END`, start, policy.ScopeType, policy.ScopeID, excludedRequestID).Scan(&cost, &tokens)
	return cost, tokens, err
}

func budgetPeriodStart(now time.Time, period string) time.Time {
	now = now.UTC()
	switch period {
	case "hour":
		return now.Truncate(time.Hour)
	case "week":
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		offset := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset)
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	}
}

func reservationOwner(event BillingEvent) string {
	return strings.Join([]string{event.APIKeyFingerprint, event.UserID, event.TeamID}, "|")
}

func budgetTags(event BillingEvent) []string {
	if event.Tags == nil {
		return []string{}
	}
	return event.Tags
}

func budgetProviderName(event BillingEvent) string {
	if event.ProviderEndpointName != "" {
		return event.ProviderEndpointName
	}
	return event.Provider
}

func budgetProviderType(event BillingEvent) string {
	if event.ProviderEndpointType != "" {
		return event.ProviderEndpointType
	}
	return event.Provider
}
