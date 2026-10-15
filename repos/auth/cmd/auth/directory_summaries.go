package main

import (
	"net/http"

	"ai-gateway-auth/internal/modules"
)

func registerDirectorySummaryRoutes(mux *http.ServeMux, module *modules.AuthModule, secret string) {
	mux.HandleFunc("POST /internal/v1/directory-summaries", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		var q modules.DirectorySummaryQuery
		if !decodeManagementJSON(w, r, &q) {
			return
		}
		if !q.Valid() {
			http.Error(w, "invalid directory summary query", http.StatusBadRequest)
			return
		}
		data, err := module.DirectorySummaries(r.Context(), q)
		if err != nil {
			writeTeamOrganizationFailure(w, err)
			return
		}
		writeManagementJSON(w, http.StatusOK, map[string]any{"data": data})
	}))
}
