package main

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-gateway-billing/internal/modules"
)

func registerUsageManagement(mux *http.ServeMux, reporter modules.UsageReporter, initErr error, secret string) {
	mux.HandleFunc("GET /internal/v1/usage/report", func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("X-Management-Token")
		secret = strings.TrimSpace(secret)
		if secret == "" || len(provided) != len(secret) || subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if reporter == nil || initErr != nil {
			http.Error(w, "usage reporting unavailable", http.StatusServiceUnavailable)
			return
		}
		days := 30
		if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 90 {
				http.Error(w, "days must be between 1 and 90", http.StatusBadRequest)
				return
			}
			days = parsed
		}
		scopeType := strings.TrimSpace(r.URL.Query().Get("scope_type"))
		scopeID := strings.TrimSpace(r.URL.Query().Get("scope_id"))
		var report modules.UsageReport
		var err error
		fromRaw, toRaw := strings.TrimSpace(r.URL.Query().Get("from")), strings.TrimSpace(r.URL.Query().Get("to"))
		model, providerID := strings.TrimSpace(r.URL.Query().Get("model")), strings.TrimSpace(r.URL.Query().Get("provider"))
		filtered := fromRaw != "" || toRaw != "" || model != "" || providerID != ""
		if filtered {
			filteredReporter, ok := reporter.(modules.FilteredUsageReporter)
			from, fromErr := time.Parse(time.RFC3339, fromRaw)
			to, toErr := time.Parse(time.RFC3339, toRaw)
			if !ok || fromErr != nil || toErr != nil || !to.After(from) || to.Sub(from) > 90*24*time.Hour || len(model) > 256 || len(providerID) > 256 || ((scopeType == "") != (scopeID == "")) {
				http.Error(w, "invalid usage filter", http.StatusBadRequest)
				return
			}
			scope := modules.UsageScope{Type: scopeType, ID: scopeID}
			report, err = filteredReporter.ReportQuery(r.Context(), modules.UsageReportQuery{From: from, To: to, Scope: scope, Model: model, Provider: providerID})
		} else if scopeType == "" && scopeID == "" {
			report, err = reporter.Report(r.Context(), days)
		} else if scoped, ok := reporter.(modules.ScopedUsageReporter); ok && (scopeType == "key" || scopeType == "user" || scopeType == "team") && scopeID != "" && len(scopeID) <= 256 {
			report, err = scoped.ReportScoped(r.Context(), days, modules.UsageScope{Type: scopeType, ID: scopeID})
		} else {
			http.Error(w, "invalid usage scope", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "usage reporting unavailable", http.StatusServiceUnavailable)
			return
		}
		writeBudgetJSON(w, http.StatusOK, report)
	})
}
