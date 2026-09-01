package modules

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type BudgetPolicySpec struct {
	ScopeType string   `json:"scope_type"`
	ScopeID   string   `json:"scope_id"`
	Period    string   `json:"period"`
	Currency  string   `json:"currency"`
	MaxCost   *float64 `json:"max_cost,omitempty"`
	MaxTokens *int64   `json:"max_tokens,omitempty"`
	Enabled   *bool    `json:"enabled,omitempty"`
}

type ManagedBudgetPolicy struct {
	ID        int64     `json:"id"`
	ScopeType string    `json:"scope_type"`
	ScopeID   string    `json:"scope_id"`
	Period    string    `json:"period"`
	Currency  string    `json:"currency"`
	MaxCost   *float64  `json:"max_cost,omitempty"`
	MaxTokens *int64    `json:"max_tokens,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BudgetSummary struct {
	Policy          ManagedBudgetPolicy `json:"policy"`
	WindowStart     time.Time           `json:"window_start"`
	WindowEnd       time.Time           `json:"window_end"`
	UsedCost        float64             `json:"used_cost"`
	RemainingCost   *float64            `json:"remaining_cost,omitempty"`
	UsedTokens      int64               `json:"used_tokens"`
	RemainingTokens *int64              `json:"remaining_tokens,omitempty"`
}

type BudgetManager interface {
	ListBudgetPolicies(context.Context) ([]ManagedBudgetPolicy, error)
	GetBudgetPolicy(context.Context, int64) (ManagedBudgetPolicy, bool, error)
	CreateBudgetPolicy(context.Context, BudgetPolicySpec) (ManagedBudgetPolicy, error)
	UpdateBudgetPolicy(context.Context, int64, BudgetPolicySpec) (ManagedBudgetPolicy, bool, error)
	DisableBudgetPolicy(context.Context, int64) (bool, error)
	BudgetSummary(context.Context, int64, time.Time) (BudgetSummary, bool, error)
}

func (m BillingModule) BudgetManager() (BudgetManager, error) {
	manager, ok := m.policy.(*PostgresBudgetPolicyChecker)
	if !ok || manager == nil {
		return nil, errors.New("budget management is unavailable")
	}
	return manager, nil
}

func (c *PostgresBudgetPolicyChecker) ListBudgetPolicies(ctx context.Context) ([]ManagedBudgetPolicy, error) {
	if err := c.managementReady(); err != nil {
		return nil, err
	}
	rows, err := c.pool.Query(ctx, budgetPolicySelect+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ManagedBudgetPolicy, 0)
	for rows.Next() {
		policy, err := scanManagedBudgetPolicy(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, policy)
	}
	return result, rows.Err()
}

func (c *PostgresBudgetPolicyChecker) GetBudgetPolicy(ctx context.Context, id int64) (ManagedBudgetPolicy, bool, error) {
	if err := c.managementReady(); err != nil {
		return ManagedBudgetPolicy{}, false, err
	}
	policy, err := scanManagedBudgetPolicy(c.pool.QueryRow(ctx, budgetPolicySelect+` WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedBudgetPolicy{}, false, nil
	}
	return policy, err == nil, err
}

func (c *PostgresBudgetPolicyChecker) CreateBudgetPolicy(ctx context.Context, spec BudgetPolicySpec) (ManagedBudgetPolicy, error) {
	if err := c.managementReady(); err != nil {
		return ManagedBudgetPolicy{}, err
	}
	spec, err := normalizeBudgetPolicySpec(spec)
	if err != nil {
		return ManagedBudgetPolicy{}, err
	}
	policy, err := scanManagedBudgetPolicy(c.pool.QueryRow(ctx, `
		INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_cost,max_tokens,enabled)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		RETURNING id,scope_type,scope_id,period,currency,max_cost::float8,max_tokens,enabled,created_at,updated_at`,
		spec.ScopeType, spec.ScopeID, spec.Period, spec.Currency, spec.MaxCost, spec.MaxTokens, *spec.Enabled))
	return policy, err
}

func (c *PostgresBudgetPolicyChecker) UpdateBudgetPolicy(ctx context.Context, id int64, spec BudgetPolicySpec) (ManagedBudgetPolicy, bool, error) {
	if err := c.managementReady(); err != nil {
		return ManagedBudgetPolicy{}, false, err
	}
	spec, err := normalizeBudgetPolicySpec(spec)
	if err != nil {
		return ManagedBudgetPolicy{}, false, err
	}
	policy, err := scanManagedBudgetPolicy(c.pool.QueryRow(ctx, `
		UPDATE billing_budget_policies
		SET scope_type=$2,scope_id=$3,period=$4,currency=$5,max_cost=$6,max_tokens=$7,enabled=$8,updated_at=now()
		WHERE id=$1
		RETURNING id,scope_type,scope_id,period,currency,max_cost::float8,max_tokens,enabled,created_at,updated_at`,
		id, spec.ScopeType, spec.ScopeID, spec.Period, spec.Currency, spec.MaxCost, spec.MaxTokens, *spec.Enabled))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedBudgetPolicy{}, false, nil
	}
	return policy, err == nil, err
}

func (c *PostgresBudgetPolicyChecker) DisableBudgetPolicy(ctx context.Context, id int64) (bool, error) {
	if err := c.managementReady(); err != nil {
		return false, err
	}
	command, err := c.pool.Exec(ctx, `UPDATE billing_budget_policies SET enabled=false,updated_at=now() WHERE id=$1`, id)
	return command.RowsAffected() == 1, err
}

func (c *PostgresBudgetPolicyChecker) BudgetSummary(ctx context.Context, id int64, now time.Time) (BudgetSummary, bool, error) {
	policy, found, err := c.GetBudgetPolicy(ctx, id)
	if err != nil || !found {
		return BudgetSummary{}, found, err
	}
	usagePolicy := budgetPolicy{ID: policy.ID, ScopeType: policy.ScopeType, ScopeID: policy.ScopeID, Period: policy.Period}
	cost, tokens, err := budgetUsageForPolicy(ctx, c.pool, usagePolicy, "", now.UTC())
	if err != nil {
		return BudgetSummary{}, false, err
	}
	start := budgetPeriodStart(now.UTC(), policy.Period)
	result := BudgetSummary{Policy: policy, WindowStart: start, WindowEnd: budgetPeriodEnd(start, policy.Period), UsedCost: cost, UsedTokens: tokens}
	if policy.MaxCost != nil {
		remaining := max(0, *policy.MaxCost-cost)
		result.RemainingCost = &remaining
	}
	if policy.MaxTokens != nil {
		remaining := max(int64(0), *policy.MaxTokens-tokens)
		result.RemainingTokens = &remaining
	}
	return result, true, nil
}

func (c *PostgresBudgetPolicyChecker) managementReady() error {
	if c == nil {
		return errors.New("budget management is unavailable")
	}
	if c.initErr != nil {
		return c.initErr
	}
	if c.pool == nil {
		return errors.New("budget management is unavailable")
	}
	return nil
}

const budgetPolicySelect = `SELECT id,scope_type,scope_id,period,currency,max_cost::float8,max_tokens,enabled,created_at,updated_at FROM billing_budget_policies`

type budgetPolicyScanner interface {
	Scan(...any) error
}

func scanManagedBudgetPolicy(scanner budgetPolicyScanner) (ManagedBudgetPolicy, error) {
	var policy ManagedBudgetPolicy
	err := scanner.Scan(&policy.ID, &policy.ScopeType, &policy.ScopeID, &policy.Period, &policy.Currency,
		&policy.MaxCost, &policy.MaxTokens, &policy.Enabled, &policy.CreatedAt, &policy.UpdatedAt)
	return policy, err
}

func normalizeBudgetPolicySpec(spec BudgetPolicySpec) (BudgetPolicySpec, error) {
	spec.ScopeType = strings.ToLower(strings.TrimSpace(spec.ScopeType))
	spec.ScopeID = strings.TrimSpace(spec.ScopeID)
	spec.Period = strings.ToLower(strings.TrimSpace(spec.Period))
	spec.Currency = strings.ToUpper(strings.TrimSpace(spec.Currency))
	if spec.Currency == "" {
		spec.Currency = "USD"
	}
	if spec.Enabled == nil {
		enabled := true
		spec.Enabled = &enabled
	}
	if !oneOf(spec.ScopeType, "global", "key", "user", "team", "organization", "model", "provider", "tag") || spec.ScopeID == "" {
		return spec, errors.New("invalid budget scope")
	}
	if spec.ScopeType == "global" && spec.ScopeID != "*" {
		return spec, errors.New("global budget scope_id must be *")
	}
	if !oneOf(spec.Period, "hour", "day", "week", "month") {
		return spec, errors.New("invalid budget period")
	}
	if len(spec.Currency) != 3 || spec.Currency[0] < 'A' || spec.Currency[0] > 'Z' || spec.Currency[1] < 'A' || spec.Currency[1] > 'Z' || spec.Currency[2] < 'A' || spec.Currency[2] > 'Z' {
		return spec, errors.New("invalid budget currency")
	}
	if (spec.MaxCost == nil || *spec.MaxCost <= 0) && (spec.MaxTokens == nil || *spec.MaxTokens <= 0) {
		return spec, errors.New("budget requires a positive cost or token limit")
	}
	if spec.MaxCost != nil && (*spec.MaxCost <= 0 || *spec.MaxCost > 1e12) {
		return spec, errors.New("invalid cost limit")
	}
	if spec.MaxTokens != nil && (*spec.MaxTokens <= 0 || *spec.MaxTokens > 1e18) {
		return spec, errors.New("invalid token limit")
	}
	return spec, nil
}

func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func budgetPeriodEnd(start time.Time, period string) time.Time {
	switch period {
	case "hour":
		return start.Add(time.Hour)
	case "week":
		return start.AddDate(0, 0, 7)
	case "month":
		return start.AddDate(0, 1, 0)
	default:
		return start.AddDate(0, 0, 1)
	}
}
