package modules

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type IdentityBudgetQuery struct {
	ScopeType string
	ScopeID   string
	Offset    int
	Limit     int
}

func (q IdentityBudgetQuery) Valid() bool {
	return (q.ScopeType == "team" || q.ScopeType == "user") && q.ScopeID != "" && len(q.ScopeID) <= 256 && strings.TrimSpace(q.ScopeID) == q.ScopeID && !strings.ContainsRune(q.ScopeID, 0) && q.Offset >= 0 && q.Limit >= 1 && q.Limit <= 100
}

type IdentityBudgetPage struct {
	Data   []BudgetSummary `json:"data"`
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
}

// IdentityBudgetPage reads only policies directly assigned to the requested
// identity. Global, inherited and per-key caps remain independent policies.
func (c *PostgresBudgetPolicyChecker) IdentityBudgetPage(ctx context.Context, q IdentityBudgetQuery, now time.Time) (IdentityBudgetPage, error) {
	if !q.Valid() {
		return IdentityBudgetPage{}, errors.New("invalid identity budget query")
	}
	if err := c.managementReady(); err != nil {
		return IdentityBudgetPage{}, err
	}
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return IdentityBudgetPage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	page := IdentityBudgetPage{Data: make([]BudgetSummary, 0), Offset: q.Offset, Limit: q.Limit}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM billing_budget_policies WHERE scope_type=$1 AND scope_id=$2`, q.ScopeType, q.ScopeID).Scan(&page.Total); err != nil {
		return IdentityBudgetPage{}, err
	}
	now = now.UTC()
	rows, err := tx.Query(ctx, budgetSummaryColumns+` FROM (
	 SELECT * FROM billing_budget_policies WHERE scope_type=$6 AND scope_id=$7 ORDER BY id OFFSET $8 LIMIT $9
	) p `+budgetSummaryUsage, budgetPeriodStart(now, "hour"), budgetPeriodStart(now, "day"), budgetPeriodStart(now, "week"), budgetPeriodStart(now, "month"), now, q.ScopeType, q.ScopeID, q.Offset, q.Limit)
	if err != nil {
		return IdentityBudgetPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var policy ManagedBudgetPolicy
		var cost float64
		var tokens int64
		if err := rows.Scan(&policy.ID, &policy.ScopeType, &policy.ScopeID, &policy.Period, &policy.Currency, &policy.MaxCost, &policy.MaxTokens, &policy.Enabled, &policy.CreatedAt, &policy.UpdatedAt, &cost, &tokens); err != nil {
			return IdentityBudgetPage{}, err
		}
		page.Data = append(page.Data, budgetSummaryForUsage(policy, cost, tokens, now))
	}
	if err := rows.Err(); err != nil {
		return IdentityBudgetPage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IdentityBudgetPage{}, err
	}
	return page, nil
}
