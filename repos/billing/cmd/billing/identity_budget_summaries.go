package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"ai-gateway-billing/internal/modules"
)

func (h budgetManagementHandler) identitySummaries(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var q modules.IdentityBudgetSummariesQuery
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&q) != nil || decoder.Decode(new(any)) != io.EOF || !q.Valid() {
		http.Error(w, "invalid identity budget summary query", http.StatusBadRequest)
		return
	}
	manager, ok := h.manager.(interface {
		IdentityBudgetSummaries(context.Context, modules.IdentityBudgetSummariesQuery, time.Time) ([]modules.IdentityBudgetSummary, error)
	})
	if !ok {
		http.Error(w, "identity budget summaries unavailable", http.StatusServiceUnavailable)
		return
	}
	data, err := manager.IdentityBudgetSummaries(r.Context(), q, h.now())
	if err != nil {
		writeBudgetFailure(w, err)
		return
	}
	writeBudgetJSON(w, http.StatusOK, map[string]any{"data": data})
}
