package modules

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type IdentityBudgetSummariesQuery struct {
	ScopeType string   `json:"scope_type"`
	IDs       []string `json:"ids"`
}

func (q IdentityBudgetSummariesQuery) Valid() bool {
	if (q.ScopeType != "team" && q.ScopeType != "user") || len(q.IDs) == 0 || len(q.IDs) > 200 {
		return false
	}
	seen := make(map[string]bool, len(q.IDs))
	for _, id := range q.IDs {
		if id == "" || len(id) > 256 || strings.TrimSpace(id) != id || strings.ContainsRune(id, 0) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// Each identity has an exact policy count and at most two enabled-first previews.
// Usage remains per policy, currency and reset window; it must not be summed.
type IdentityBudgetSummary struct {
	ID    string          `json:"id"`
	Total int             `json:"total"`
	Data  []BudgetSummary `json:"data"`
}

func (c *PostgresBudgetPolicyChecker) IdentityBudgetSummaries(ctx context.Context, q IdentityBudgetSummariesQuery, now time.Time) ([]IdentityBudgetSummary, error) {
	if !q.Valid() {
		return nil, errors.New("invalid identity budget summary query")
	}
	if err := c.managementReady(); err != nil {
		return nil, err
	}
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result := make([]IdentityBudgetSummary, len(q.IDs))
	positions := make(map[string]int, len(q.IDs))
	for i, id := range q.IDs {
		result[i] = IdentityBudgetSummary{ID: id, Data: []BudgetSummary{}}
		positions[id] = i
	}
	rows, err := tx.Query(ctx, `SELECT scope_id,count(*) FROM billing_budget_policies WHERE scope_type=$1 AND scope_id=ANY($2) GROUP BY scope_id`, q.ScopeType, q.IDs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var total int
		if err := rows.Scan(&id, &total); err != nil {
			rows.Close()
			return nil, err
		}
		result[positions[id]].Total = total
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	now = now.UTC()
	rows, err = tx.Query(ctx, budgetSummaryColumns+` FROM (
	 SELECT * FROM (SELECT *,row_number() OVER(PARTITION BY scope_id ORDER BY enabled DESC,id) AS position FROM billing_budget_policies WHERE scope_type=$6 AND scope_id=ANY($7)) ranked WHERE position<=2
	) p `+budgetSummaryUsageGrouped+` ORDER BY p.scope_id,p.enabled DESC,p.id`, budgetPeriodStart(now, "hour"), budgetPeriodStart(now, "day"), budgetPeriodStart(now, "week"), budgetPeriodStart(now, "month"), now, q.ScopeType, q.IDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p ManagedBudgetPolicy
		var cost float64
		var tokens int64
		if err := rows.Scan(&p.ID, &p.ScopeType, &p.ScopeID, &p.Period, &p.Currency, &p.MaxCost, &p.MaxTokens, &p.Enabled, &p.CreatedAt, &p.UpdatedAt, &cost, &tokens); err != nil {
			return nil, err
		}
		i := positions[p.ScopeID]
		result[i].Data = append(result[i].Data, budgetSummaryForUsage(p, cost, tokens, now))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
