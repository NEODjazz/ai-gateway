package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
)

type DirectorySummaryQuery struct {
	ScopeType string   `json:"scope_type"`
	IDs       []string `json:"ids"`
}

func (q DirectorySummaryQuery) valid() bool {
	if (q.ScopeType != "team" && q.ScopeType != "user") || len(q.IDs) == 0 || len(q.IDs) > 200 {
		return false
	}
	seen := make(map[string]bool, len(q.IDs))
	for _, id := range q.IDs {
		if len(id) == 0 || len(id) > 256 || seen[id] {
			return false
		}
		for _, r := range id {
			if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '.' && r != '_' && r != '@' && r != ':' && r != '-' {
				return false
			}
		}
		seen[id] = true
	}
	return true
}

type DirectorySummary struct {
	ID           string                `json:"id"`
	KeyCount     int                   `json:"key_count"`
	Organization *OrganizationMetadata `json:"organization"`
}

type IdentityBudgetSummary struct {
	ID    string          `json:"id"`
	Total int             `json:"total"`
	Data  []BudgetSummary `json:"data"`
}

type DirectorySummaryClient interface {
	DirectorySummaries(context.Context, ManagementAudit, DirectorySummaryQuery) ([]DirectorySummary, error)
}

type IdentityBudgetSummaryClient interface {
	IdentityBudgetSummaries(context.Context, ManagementAudit, DirectorySummaryQuery) ([]IdentityBudgetSummary, error)
}

type IdentitySpendQuery struct {
	DirectorySummaryQuery
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type IdentitySpend struct {
	ID       string  `json:"id"`
	Currency string  `json:"currency"`
	Cost     float64 `json:"cost"`
	Requests uint64  `json:"requests"`
}

type IdentitySpendClient interface {
	IdentitySpend(context.Context, ManagementAudit, IdentitySpendQuery) ([]IdentitySpend, error)
}

func (c *RemoteBudgetManagementClient) IdentitySpend(ctx context.Context, audit ManagementAudit, q IdentitySpendQuery) ([]IdentitySpend, error) {
	var response struct {
		Data []struct {
			ID       string   `json:"id"`
			Currency string   `json:"currency"`
			Cost     *float64 `json:"cost"`
			Requests *uint64  `json:"requests"`
		} `json:"data"`
	}
	err := c.call(ctx, http.MethodPost, "/internal/v1/usage/identities", audit, q, &response)
	if err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, errors.New("identity spend omitted data")
	}
	result := make([]IdentitySpend, 0, len(response.Data))
	for _, row := range response.Data {
		if row.Cost == nil || row.Requests == nil {
			return nil, errors.New("identity spend omitted required amounts")
		}
		result = append(result, IdentitySpend{ID: row.ID, Currency: row.Currency, Cost: *row.Cost, Requests: *row.Requests})
	}
	return result, nil
}

func (c *RemoteManagementClient) DirectorySummaries(ctx context.Context, audit ManagementAudit, q DirectorySummaryQuery) ([]DirectorySummary, error) {
	response, err := managementCallWithLimit[DirectorySummaryQuery, struct {
		Data []struct {
			ID           string          `json:"id"`
			KeyCount     *int            `json:"key_count"`
			Organization json.RawMessage `json:"organization"`
		} `json:"data"`
	}](ctx, c, http.MethodPost, "/internal/v1/directory-summaries", audit, q, 512<<10)
	if err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, errors.New("directory summary omitted data")
	}
	result := make([]DirectorySummary, 0, len(response.Data))
	for _, row := range response.Data {
		if row.KeyCount == nil || len(row.Organization) == 0 {
			return nil, errors.New("directory summary omitted counts or ownership")
		}
		summary := DirectorySummary{ID: row.ID, KeyCount: *row.KeyCount}
		if err := json.Unmarshal(row.Organization, &summary.Organization); err != nil {
			return nil, err
		}
		result = append(result, summary)
	}
	return result, nil
}

func (c *RemoteBudgetManagementClient) IdentityBudgetSummaries(ctx context.Context, audit ManagementAudit, q DirectorySummaryQuery) ([]IdentityBudgetSummary, error) {
	var response struct {
		Data []struct {
			ID    string           `json:"id"`
			Total *int             `json:"total"`
			Data  *[]BudgetSummary `json:"data"`
		} `json:"data"`
	}
	err := c.call(ctx, http.MethodPost, "/internal/v1/budgets/identities", audit, q, &response)
	if err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, errors.New("budget summaries omitted data")
	}
	result := make([]IdentityBudgetSummary, 0, len(response.Data))
	for _, row := range response.Data {
		if row.Total == nil || row.Data == nil {
			return nil, errors.New("budget summary omitted count or policy previews")
		}
		result = append(result, IdentityBudgetSummary{ID: row.ID, Total: *row.Total, Data: *row.Data})
	}
	return result, nil
}

type identityDirectorySummary struct {
	ID        string                 `json:"id"`
	Directory *DirectorySummary      `json:"directory"`
	Policy    *identityPolicySummary `json:"policy"`
	Budgets   *IdentityBudgetSummary `json:"budgets"`
	Spend     *[]IdentitySpend       `json:"spend"`
}

type identityPolicySummary struct {
	ModelAccess   string   `json:"model_access"`
	AllowedModels []string `json:"allowed_models"`
	ModelCount    int      `json:"model_count"`
	RPM           int      `json:"rpm"`
	TPM           int      `json:"tpm"`
}

func (h Handler) DirectorySummaries(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	var q DirectorySummaryQuery
	if !decodeDirectoryJSON(w, r, &q) {
		return
	}
	if !q.valid() {
		writeError(w, http.StatusBadRequest, "invalid_request", "select 1–200 unique team or user IDs")
		return
	}
	audit := managementAudit(actor)
	result := make([]identityDirectorySummary, len(q.IDs))
	positions := make(map[string]int, len(q.IDs))
	sourceErrors := make(map[string]string)
	for i, id := range q.IDs {
		result[i].ID = id
		positions[id] = i
		if h.access != nil {
			p, found := h.access.IdentityPolicy(q.ScopeType, id)
			if !found {
				p = IdentityInferencePolicy{ScopeType: q.ScopeType, ScopeID: id, ModelAccess: "inherit", AllowedModels: []string{}}
			}
			result[i].Policy = &identityPolicySummary{ModelAccess: p.ModelAccess, AllowedModels: append([]string{}, p.AllowedModels[:min(len(p.AllowedModels), 2)]...), ModelCount: len(p.AllowedModels), RPM: p.RPM, TPM: p.TPM}
		}
	}
	if h.access == nil {
		sourceErrors["policy"] = "Identity policy is unavailable."
	}
	if client, supported := h.directory.(DirectorySummaryClient); supported {
		rows, err := client.DirectorySummaries(r.Context(), audit, q)
		if err == nil && validDirectorySummaries(rows, positions) {
			for i := range rows {
				result[positions[rows[i].ID]].Directory = &rows[i]
			}
		} else {
			sourceErrors["directory"] = "Organization and key counts are unavailable."
		}
	} else {
		sourceErrors["directory"] = "Organization and key counts are unavailable."
	}
	if client, supported := h.budgets.(IdentityBudgetSummaryClient); supported {
		rows, err := client.IdentityBudgetSummaries(r.Context(), audit, q)
		if err == nil && validIdentityBudgetSummaries(rows, q, positions) {
			for i := range rows {
				result[positions[rows[i].ID]].Budgets = &rows[i]
			}
		} else {
			sourceErrors["budgets"] = "Budget usage is unavailable."
		}
	} else {
		sourceErrors["budgets"] = "Budget usage is unavailable."
	}
	to := time.Now().UTC().Truncate(time.Second)
	from := to.AddDate(0, 0, -30)
	if client, supported := h.usage.(IdentitySpendClient); supported {
		rows, err := client.IdentitySpend(r.Context(), audit, IdentitySpendQuery{DirectorySummaryQuery: q, From: from, To: to})
		if err == nil && validIdentitySpend(rows, positions) {
			for i := range result {
				empty := []IdentitySpend{}
				result[i].Spend = &empty
			}
			for _, row := range rows {
				target := result[positions[row.ID]].Spend
				*target = append(*target, row)
			}
		} else {
			sourceErrors["spend"] = "Finalized spend is unavailable."
		}
	} else {
		sourceErrors["spend"] = "Finalized spend is unavailable."
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope_type": q.ScopeType, "from": from, "to": to, "data": result, "errors": sourceErrors})
}

func validIdentitySpend(rows []IdentitySpend, positions map[string]int) bool {
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		key := row.ID + "\x00" + row.Currency
		if _, ok := positions[row.ID]; !ok || seen[key] || len(row.Currency) != 3 || strings.IndexFunc(row.Currency, func(c rune) bool { return c < 'A' || c > 'Z' }) >= 0 || math.IsNaN(row.Cost) || math.IsInf(row.Cost, 0) || row.Cost < 0 || row.Requests == 0 {
			return false
		}
		seen[key] = true
	}
	return true
}

func validDirectorySummaries(rows []DirectorySummary, positions map[string]int) bool {
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if _, ok := positions[row.ID]; !ok || seen[row.ID] || row.KeyCount < 0 {
			return false
		}
		if row.Organization != nil && (row.Organization.ID == "" || row.Organization.Name == "" || (row.Organization.Status != "active" && row.Organization.Status != "disabled")) {
			return false
		}
		seen[row.ID] = true
	}
	return true
}

func validIdentityBudgetSummaries(rows []IdentityBudgetSummary, q DirectorySummaryQuery, positions map[string]int) bool {
	if len(rows) != len(positions) {
		return false
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if _, ok := positions[row.ID]; !ok || seen[row.ID] || row.Total < 0 || len(row.Data) != min(row.Total, 2) {
			return false
		}
		policies := make(map[int64]bool, len(row.Data))
		for _, summary := range row.Data {
			p := summary.Policy
			if p.ScopeType != q.ScopeType || p.ScopeID != row.ID || p.ID < 1 || policies[p.ID] || len(p.Currency) != 3 || strings.IndexFunc(p.Currency, func(c rune) bool { return c < 'A' || c > 'Z' }) >= 0 || summary.UsedTokens < 0 || summary.UsedCost < 0 || math.IsNaN(summary.UsedCost) || math.IsInf(summary.UsedCost, 0) || summary.WindowStart.IsZero() || !summary.WindowEnd.After(summary.WindowStart) {
				return false
			}
			policies[p.ID] = true
		}
		seen[row.ID] = true
	}
	return true
}
