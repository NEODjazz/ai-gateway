package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type IdentityBudgetQuery struct {
	ScopeType string
	ScopeID   string
	Offset    int
	Limit     int
}

type IdentityBudgetPage struct {
	Data   []BudgetSummary `json:"data"`
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
}

type IdentityBudgetPageClient interface {
	IdentityBudgetPage(context.Context, ManagementAudit, IdentityBudgetQuery) (IdentityBudgetPage, error)
}

func (c *RemoteBudgetManagementClient) IdentityBudgetPage(ctx context.Context, audit ManagementAudit, q IdentityBudgetQuery) (IdentityBudgetPage, error) {
	values := url.Values{"scope_type": {q.ScopeType}, "scope_id": {q.ScopeID}, "offset": {strconv.Itoa(q.Offset)}, "limit": {strconv.Itoa(q.Limit)}}
	var page IdentityBudgetPage
	err := c.call(ctx, http.MethodGet, "/internal/v1/budgets/page?"+values.Encode(), audit, nil, &page)
	return page, err
}

func (h Handler) ListIdentityBudgets(w http.ResponseWriter, r *http.Request) {
	audit, ok := h.budgetAdmin(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	q := IdentityBudgetQuery{ScopeType: values.Get("scope_type"), ScopeID: values.Get("scope_id"), Limit: 25}
	var err error
	if values.Has("limit") {
		q.Limit, err = strconv.Atoi(values.Get("limit"))
	}
	if err == nil && values.Has("offset") {
		q.Offset, err = strconv.Atoi(values.Get("offset"))
	}
	if err != nil || (q.ScopeType != "team" && q.ScopeType != "user") || q.ScopeID == "" || len(q.ScopeID) > 256 || strings.TrimSpace(q.ScopeID) != q.ScopeID || strings.ContainsRune(q.ScopeID, 0) || q.Offset < 0 || q.Limit < 1 || q.Limit > 100 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid identity budget query")
		return
	}
	client, ok := h.budgets.(IdentityBudgetPageClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "identity budget pages are unavailable")
		return
	}
	page, err := client.IdentityBudgetPage(r.Context(), audit, q)
	if err != nil {
		writeBudgetManagementFailure(w, err)
		return
	}
	if page.Offset != q.Offset || page.Limit != q.Limit || page.Total < 0 || len(page.Data) > q.Limit {
		writeError(w, http.StatusBadGateway, "invalid_management_response", "invalid identity budget page")
		return
	}
	for _, summary := range page.Data {
		if summary.Policy.ScopeType != q.ScopeType || summary.Policy.ScopeID != q.ScopeID {
			writeError(w, http.StatusBadGateway, "invalid_management_response", "budget page does not match the requested identity")
			return
		}
	}
	writeJSON(w, http.StatusOK, page)
}
