package main

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"

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
		report, err := reporter.Report(r.Context(), days)
		if err != nil {
			http.Error(w, "usage reporting unavailable", http.StatusServiceUnavailable)
			return
		}
		writeBudgetJSON(w, http.StatusOK, report)
	})
}
