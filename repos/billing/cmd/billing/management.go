package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-gateway-billing/internal/modules"
)

type budgetManagementHandler struct {
	manager modules.BudgetManager
	initErr error
	secret  string
	now     func() time.Time
}

func registerBudgetManagement(mux *http.ServeMux, manager modules.BudgetManager, initErr error, secret string) {
	h := budgetManagementHandler{manager: manager, initErr: initErr, secret: strings.TrimSpace(secret), now: time.Now}
	mux.HandleFunc("GET /internal/v1/budgets", h.list)
	mux.HandleFunc("POST /internal/v1/budgets", h.create)
	mux.HandleFunc("GET /internal/v1/budgets/{id}", h.get)
	mux.HandleFunc("PUT /internal/v1/budgets/{id}", h.update)
	mux.HandleFunc("DELETE /internal/v1/budgets/{id}", h.disable)
	mux.HandleFunc("GET /internal/v1/budgets/{id}/summary", h.summary)
	mux.HandleFunc("POST /internal/v1/budgets/key-projections", h.keyProjections)
}

func (h budgetManagementHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	provided := r.Header.Get("X-Management-Token")
	if h.secret == "" || len(provided) != len(h.secret) || subtle.ConstantTimeCompare([]byte(provided), []byte(h.secret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if h.manager == nil || h.initErr != nil {
		http.Error(w, "budget management unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (h budgetManagementHandler) list(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	policies, err := h.manager.ListBudgetPolicies(r.Context())
	if err != nil {
		writeBudgetFailure(w, err)
		return
	}
	writeBudgetJSON(w, http.StatusOK, map[string]any{"data": policies})
}

func (h budgetManagementHandler) create(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	spec, ok := decodeBudgetSpec(w, r)
	if !ok {
		return
	}
	policy, err := h.manager.CreateBudgetPolicy(r.Context(), spec)
	if err != nil {
		writeBudgetFailure(w, err)
		return
	}
	writeBudgetJSON(w, http.StatusCreated, policy)
}

func (h budgetManagementHandler) get(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id, ok := budgetID(w, r)
	if !ok {
		return
	}
	policy, found, err := h.manager.GetBudgetPolicy(r.Context(), id)
	if err != nil {
		writeBudgetFailure(w, err)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeBudgetJSON(w, http.StatusOK, policy)
}

func (h budgetManagementHandler) update(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id, ok := budgetID(w, r)
	if !ok {
		return
	}
	spec, ok := decodeBudgetSpec(w, r)
	if !ok {
		return
	}
	policy, found, err := h.manager.UpdateBudgetPolicy(r.Context(), id, spec)
	if err != nil {
		writeBudgetFailure(w, err)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeBudgetJSON(w, http.StatusOK, policy)
}

func (h budgetManagementHandler) disable(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id, ok := budgetID(w, r)
	if !ok {
		return
	}
	found, err := h.manager.DisableBudgetPolicy(r.Context(), id)
	if err != nil {
		writeBudgetFailure(w, err)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h budgetManagementHandler) summary(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id, ok := budgetID(w, r)
	if !ok {
		return
	}
	summary, found, err := h.manager.BudgetSummary(r.Context(), id, h.now())
	if err != nil {
		writeBudgetFailure(w, err)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeBudgetJSON(w, http.StatusOK, summary)
}

func (h budgetManagementHandler) keyProjections(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var request struct {
		Subjects []modules.KeyBudgetSubject `json:"subjects"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 256<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid key budget projection request", http.StatusBadRequest)
		return
	}
	if len(request.Subjects) == 0 || len(request.Subjects) > 500 {
		http.Error(w, "invalid key budget projection request", http.StatusBadRequest)
		return
	}
	projections, err := h.manager.KeyBudgetProjections(r.Context(), request.Subjects, h.now())
	if err != nil {
		writeBudgetFailure(w, err)
		return
	}
	writeBudgetJSON(w, http.StatusOK, map[string]any{"data": projections})
}

func budgetID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid budget id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func decodeBudgetSpec(w http.ResponseWriter, r *http.Request) (modules.BudgetPolicySpec, bool) {
	var spec modules.BudgetPolicySpec
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		http.Error(w, "invalid budget policy", http.StatusBadRequest)
		return spec, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid budget policy", http.StatusBadRequest)
		return spec, false
	}
	return spec, true
}

func writeBudgetFailure(w http.ResponseWriter, err error) {
	if strings.HasPrefix(err.Error(), "invalid ") || strings.Contains(err.Error(), " requires ") {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, "budget management unavailable", http.StatusServiceUnavailable)
}

func writeBudgetJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
