package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"ai-gateway-billing/internal/modules"
)

func registerIdentitySpend(mux *http.ServeMux, reporter modules.UsageReporter, initErr error, secret string) {
	secret = strings.TrimSpace(secret)
	mux.HandleFunc("POST /internal/v1/usage/identities", func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("X-Management-Token")
		if secret == "" || len(provided) != len(secret) || subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		org, ok := managementOrganization(w, r)
		if !ok {
			return
		}
		if org != "" {
			http.Error(w, "global usage summaries require a platform administrator", http.StatusForbidden)
			return
		}
		var q modules.IdentitySpendQuery
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&q) != nil || decoder.Decode(new(any)) != io.EOF || !q.Valid() {
			http.Error(w, "invalid identity spend query", http.StatusBadRequest)
			return
		}
		client, ok := reporter.(interface {
			IdentitySpend(context.Context, modules.IdentitySpendQuery) ([]modules.IdentitySpend, error)
		})
		if !ok || initErr != nil {
			http.Error(w, "usage reporting unavailable", http.StatusServiceUnavailable)
			return
		}
		data, err := client.IdentitySpend(r.Context(), q)
		if err != nil {
			http.Error(w, "usage reporting unavailable", http.StatusServiceUnavailable)
			return
		}
		writeBudgetJSON(w, http.StatusOK, map[string]any{"data": data})
	})
}
