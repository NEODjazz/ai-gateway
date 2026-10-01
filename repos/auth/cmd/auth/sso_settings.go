package main

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"ai-gateway-auth/internal/modules"
)

func registerSSORoutes(mux *http.ServeMux, module *modules.AuthModule, secret string) {
	admin := func(next http.HandlerFunc) http.HandlerFunc {
		return managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
			if !slices.Contains(strings.Split(r.Header.Get("X-Actor-Roles"), ","), "admin") {
				http.Error(w, "global administrator required", http.StatusForbidden)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			next(w, r)
		})
	}
	mux.HandleFunc("GET /internal/v1/sso/settings", admin(func(w http.ResponseWriter, r *http.Request) {
		view, err := module.SSOManager().View(r.Context())
		writeSSOResult(w, view, err)
	}))
	mux.HandleFunc("PUT /internal/v1/sso/settings", admin(func(w http.ResponseWriter, r *http.Request) {
		var input modules.SSODraftInput
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		view, err := module.SSOManager().SaveDraft(r.Context(), input)
		if err == nil {
			logManagementAction(r, "sso.draft.save", "browser-sso")
		}
		writeSSOResult(w, view, err)
	}))
	mux.HandleFunc("POST /internal/v1/sso/discover", admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Issuer string `json:"issuer"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		metadata, err := modules.DiscoverSSO(r.Context(), input.Issuer)
		writeSSOResult(w, metadata, err)
	}))
	mux.HandleFunc("POST /internal/v1/sso/test", admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		ticket, err := module.SSOManager().StartTest(r.Context(), input.ExpectedRevision, r.Header.Get("X-Actor-ID"))
		if err == nil {
			logManagementAction(r, "sso.test.start", "browser-sso")
		}
		writeSSOResult(w, map[string]string{"ticket": ticket}, err)
	}))
	mux.HandleFunc("POST /internal/v1/sso/action", admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ExpectedRevision int64  `json:"expected_revision"`
			Action           string `json:"action"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		view, err := module.ChangeSSO(r.Context(), input.Action, input.ExpectedRevision, r.Header.Get("X-Actor-ID"))
		if err == nil {
			logManagementAction(r, "sso."+input.Action, "browser-sso")
		}
		writeSSOResult(w, view, err)
	}))
	// Secrets are only exposed over the existing authenticated service channel.
	mux.HandleFunc("GET /internal/v1/sso/active", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if module.SSOManager() == nil {
			writeSSOResult(w, nil, nil)
			return
		}
		state, _, err := module.SSOManager().Load(r.Context())
		writeSSOResult(w, state.Active, err)
	}))
	mux.HandleFunc("GET /internal/v1/sso/test-profile", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		profile, err := module.SSOManager().TestProfile(r.Context())
		writeSSOResult(w, profile, err)
	}))
	mux.HandleFunc("POST /internal/v1/sso/verify-test", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ProfileID string `json:"profile_id"`
			Ticket    string `json:"ticket"`
			Token     string `json:"token"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		err := module.VerifySSOTest(r.Context(), input.ProfileID, input.Ticket, input.Token)
		if err == nil {
			logManagementAction(r, "sso.test.passed", "browser-sso")
		}
		writeSSOResult(w, map[string]bool{"verified": err == nil}, err)
	}))
}

func writeSSOResult(w http.ResponseWriter, value any, err error) {
	if err == nil {
		writeManagementJSON(w, http.StatusOK, value)
		return
	}
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, modules.ErrSSOConfiguration):
		status = http.StatusBadRequest
	case errors.Is(err, modules.ErrSSOConflict):
		status = http.StatusConflict
	case errors.Is(err, modules.ErrUnauthorized):
		status = http.StatusForbidden
	}
	http.Error(w, "SSO settings operation failed", status)
}
