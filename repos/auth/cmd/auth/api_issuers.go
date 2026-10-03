package main

import (
	"net/http"
	"slices"
	"strings"

	"ai-gateway-auth/internal/modules"
)

func registerAPIIssuerRoutes(mux *http.ServeMux, module *modules.AuthModule, secret string) {
	admin := func(next http.HandlerFunc) http.HandlerFunc {
		return managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
			if !slices.Contains(strings.Split(r.Header.Get("X-Actor-Roles"), ","), "admin") {
				http.Error(w, "global administrator required", 403)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			next(w, r)
		})
	}
	mux.HandleFunc("GET /internal/v1/api-issuers", admin(func(w http.ResponseWriter, r *http.Request) {
		views, err := module.APIIssuers(r.Context())
		writeSSOResult(w, map[string]any{"data": views}, err)
	}))
	mux.HandleFunc("POST /internal/v1/api-issuers", admin(func(w http.ResponseWriter, r *http.Request) {
		var input modules.APIIssuerInput
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		view, err := module.CreateAPIIssuer(r.Context(), input)
		if err == nil {
			logManagementAction(r, "api-issuer.create", input.ID)
		}
		writeSSOResult(w, view, err)
	}))
	mux.HandleFunc("PUT /internal/v1/api-issuers/{id}", admin(func(w http.ResponseWriter, r *http.Request) {
		var input modules.APIIssuerDraftInput
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		view, err := module.SaveAPIIssuerDraft(r.Context(), r.PathValue("id"), input)
		if err == nil {
			logManagementAction(r, "api-issuer.draft.save", r.PathValue("id"))
		}
		writeSSOResult(w, view, err)
	}))
	mux.HandleFunc("POST /internal/v1/api-issuers/{id}/test", admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ExpectedRevision int64  `json:"expected_revision"`
			Token            string `json:"token"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		view, err := module.TestAPIIssuer(r.Context(), r.PathValue("id"), input.ExpectedRevision, r.Header.Get("X-Actor-ID"), input.Token)
		if err == nil {
			logManagementAction(r, "api-issuer.test.passed", r.PathValue("id"))
		}
		writeSSOResult(w, view, err)
	}))
	mux.HandleFunc("POST /internal/v1/api-issuers/{id}/action", admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ExpectedRevision int64  `json:"expected_revision"`
			Action           string `json:"action"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		view, err := module.ChangeAPIIssuer(r.Context(), r.PathValue("id"), input.Action, input.ExpectedRevision, r.Header.Get("X-Actor-ID"))
		if err == nil {
			logManagementAction(r, "api-issuer."+input.Action, r.PathValue("id"))
		}
		writeSSOResult(w, view, err)
	}))
}
