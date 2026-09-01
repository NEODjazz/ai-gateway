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

type KeyBudgetSubject struct {
	KeyID          string `json:"key_id"`
	UserID         string `json:"user_id,omitempty"`
	TeamID         string `json:"team_id,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
}

type KeyBudgetProjection struct {
	KeyID    string          `json:"key_id"`
	Policies []BudgetSummary `json:"policies"`
}

type BudgetManager interface {
	ListBudgetPolicies(context.Context) ([]ManagedBudgetPolicy, error)
	GetBudgetPolicy(context.Context, int64) (ManagedBudgetPolicy, bool, error)
	CreateBudgetPolicy(context.Context, BudgetPolicySpec) (ManagedBudgetPolicy, error)
	UpdateBudgetPolicy(context.Context, int64, BudgetPolicySpec) (ManagedBudgetPolicy, bool, error)
	DisableBudgetPolicy(context.Context, int64) (bool, error)
	BudgetSummary(context.Context, int64, time.Time) (BudgetSummary, bool, error)
	KeyBudgetProjections(context.Context, []KeyBudgetSubject, time.Time) ([]KeyBudgetProjection, error)
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

func (c *PostgresBudgetPolicyChecker) KeyBudgetProjections(ctx context.Context, subjects []KeyBudgetSubject, now time.Time) ([]KeyBudgetProjection, error) {
	if err := c.managementReady(); err != nil {
		return nil, err
	}
	subjects, err := normalizeKeyBudgetSubjects(subjects)
	if err != nil {
		return nil, err
	}
	keyIDs, userIDs, teamIDs, organizationIDs := budgetSubjectIDs(subjects)
	now = now.UTC()
	rows, err := c.pool.Query(ctx, `
		SELECT p.id,p.scope_type,p.scope_id,p.period,p.currency,p.max_cost::float8,p.max_tokens,
		       p.enabled,p.created_at,p.updated_at,
		       COALESCE(SUM(CASE WHEN r.state='committed' THEN r.actual_cost ELSE r.reserved_cost END),0)::float8,
		       COALESCE(SUM(CASE WHEN r.state='committed' THEN r.actual_tokens ELSE r.reserved_tokens END),0)
		FROM billing_budget_policies p
		LEFT JOIN billing_budget_reservations r ON
		     r.created_at >= CASE p.period WHEN 'hour' THEN $5::timestamptz WHEN 'week' THEN $7::timestamptz WHEN 'month' THEN $8::timestamptz ELSE $6::timestamptz END
		 AND (r.state='committed' OR (r.state='reserved' AND r.reservation_expires_at > $9::timestamptz))
		 AND CASE p.scope_type
		       WHEN 'global' THEN true
		       WHEN 'key' THEN r.credential_id=p.scope_id
		       WHEN 'user' THEN r.user_id=p.scope_id
		       WHEN 'team' THEN r.team_id=p.scope_id
		       WHEN 'organization' THEN r.organization_id=p.scope_id
		       ELSE false
		     END
		WHERE p.enabled AND (
			p.scope_type='global'
			OR (p.scope_type='key' AND p.scope_id=ANY($1))
			OR (p.scope_type='user' AND p.scope_id=ANY($2))
			OR (p.scope_type='team' AND p.scope_id=ANY($3))
			OR (p.scope_type='organization' AND p.scope_id=ANY($4))
		)
		GROUP BY p.id,p.scope_type,p.scope_id,p.period,p.currency,p.max_cost,p.max_tokens,
		         p.enabled,p.created_at,p.updated_at
		ORDER BY p.id`, keyIDs, userIDs, teamIDs, organizationIDs,
		budgetPeriodStart(now, "hour"), budgetPeriodStart(now, "day"), budgetPeriodStart(now, "week"), budgetPeriodStart(now, "month"), now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := make([]BudgetSummary, 0)
	for rows.Next() {
		var policy ManagedBudgetPolicy
		var usedCost float64
		var usedTokens int64
		if err := rows.Scan(&policy.ID, &policy.ScopeType, &policy.ScopeID, &policy.Period, &policy.Currency,
			&policy.MaxCost, &policy.MaxTokens, &policy.Enabled, &policy.CreatedAt, &policy.UpdatedAt,
			&usedCost, &usedTokens); err != nil {
			return nil, err
		}
		start := budgetPeriodStart(now, policy.Period)
		summary := BudgetSummary{Policy: policy, WindowStart: start, WindowEnd: budgetPeriodEnd(start, policy.Period), UsedCost: usedCost, UsedTokens: usedTokens}
		if policy.MaxCost != nil {
			remaining := max(0, *policy.MaxCost-usedCost)
			summary.RemainingCost = &remaining
		}
		if policy.MaxTokens != nil {
			remaining := max(int64(0), *policy.MaxTokens-usedTokens)
			summary.RemainingTokens = &remaining
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return projectKeyBudgets(subjects, summaries), nil
}

func normalizeKeyBudgetSubjects(subjects []KeyBudgetSubject) ([]KeyBudgetSubject, error) {
	if len(subjects) == 0 || len(subjects) > 500 {
		return nil, errors.New("invalid key budget subjects")
	}
	result := make([]KeyBudgetSubject, 0, len(subjects))
	seen := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		subject.KeyID = strings.TrimSpace(subject.KeyID)
		subject.UserID = strings.TrimSpace(subject.UserID)
		subject.TeamID = strings.TrimSpace(subject.TeamID)
		subject.OrganizationID = strings.TrimSpace(subject.OrganizationID)
		if subject.KeyID == "" || len(subject.KeyID) > 256 || len(subject.UserID) > 256 || len(subject.TeamID) > 256 || len(subject.OrganizationID) > 256 {
			return nil, errors.New("invalid key budget subject")
		}
		if _, exists := seen[subject.KeyID]; exists {
			return nil, errors.New("invalid duplicate key budget subject")
		}
		seen[subject.KeyID] = struct{}{}
		result = append(result, subject)
	}
	return result, nil
}

func budgetSubjectIDs(subjects []KeyBudgetSubject) (keys, users, teams, organizations []string) {
	for _, subject := range subjects {
		keys = append(keys, subject.KeyID)
		if subject.UserID != "" {
			users = append(users, subject.UserID)
		}
		if subject.TeamID != "" {
			teams = append(teams, subject.TeamID)
		}
		if subject.OrganizationID != "" {
			organizations = append(organizations, subject.OrganizationID)
		}
	}
	return keys, users, teams, organizations
}

func projectKeyBudgets(subjects []KeyBudgetSubject, summaries []BudgetSummary) []KeyBudgetProjection {
	result := make([]KeyBudgetProjection, 0, len(subjects))
	for _, subject := range subjects {
		projection := KeyBudgetProjection{KeyID: subject.KeyID, Policies: make([]BudgetSummary, 0)}
		for _, summary := range summaries {
			policy := summary.Policy
			if policy.ScopeType == "global" ||
				policy.ScopeType == "key" && policy.ScopeID == subject.KeyID ||
				policy.ScopeType == "user" && policy.ScopeID == subject.UserID ||
				policy.ScopeType == "team" && policy.ScopeID == subject.TeamID ||
				policy.ScopeType == "organization" && policy.ScopeID == subject.OrganizationID {
				projection.Policies = append(projection.Policies, summary)
			}
		}
		result = append(result, projection)
	}
	return result
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
