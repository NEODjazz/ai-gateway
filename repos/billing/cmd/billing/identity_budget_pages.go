package main

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"ai-gateway-billing/internal/modules"
)

func (h budgetManagementHandler) identityPage(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	values := r.URL.Query()
	q := modules.IdentityBudgetQuery{ScopeType: values.Get("scope_type"), ScopeID: values.Get("scope_id"), Limit: 25}
	var err error
	if values.Has("limit") {
		q.Limit, err = strconv.Atoi(values.Get("limit"))
	}
	if err == nil && values.Has("offset") {
		q.Offset, err = strconv.Atoi(values.Get("offset"))
	}
	if err != nil || !q.Valid() {
		http.Error(w, "invalid identity budget query", http.StatusBadRequest)
		return
	}
	manager, ok := h.manager.(interface {
		IdentityBudgetPage(context.Context, modules.IdentityBudgetQuery, time.Time) (modules.IdentityBudgetPage, error)
	})
	if !ok {
		http.Error(w, "identity budget pages unavailable", http.StatusServiceUnavailable)
		return
	}
	page, err := manager.IdentityBudgetPage(r.Context(), q, h.now())
	if err != nil {
		writeBudgetFailure(w, err)
		return
	}
	writeBudgetJSON(w, http.StatusOK, page)
}
