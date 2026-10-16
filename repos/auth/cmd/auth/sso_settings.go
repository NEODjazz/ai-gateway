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
		selected, ok := selectSSOConnection(w, r, module)
		if !ok {
			return
		}
		module := &selected
		view, err := module.SSOManager().View(r.Context())
		writeSSOResult(w, view, err)
	}))
	mux.HandleFunc("PUT /internal/v1/sso/settings", admin(func(w http.ResponseWriter, r *http.Request) {
		var input modules.SSODraftInput
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		selected, ok := selectSSOConnection(w, r, module)
		if !ok {
			return
		}
		module := &selected
		view, err := module.SaveSSODraft(r.Context(), input)
		if err == nil {
			logManagementAction(r, "sso.draft.save", ssoAuditTarget(r))
		}
		writeSSOResult(w, view, err)
	}))
	mux.HandleFunc("POST /internal/v1/sso/discover", admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Issuer          string   `json:"issuer"`
			EndpointOrigins []string `json:"endpoint_origins,omitempty"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		metadata, err := modules.DiscoverSSO(r.Context(), input.Issuer, input.EndpointOrigins)
		writeSSOResult(w, metadata, err)
	}))
	mux.HandleFunc("POST /internal/v1/sso/test", admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		selected, ok := selectSSOConnection(w, r, module)
		if !ok {
			return
		}
		module := &selected
		ticket, err := module.SSOManager().StartTest(r.Context(), input.ExpectedRevision, r.Header.Get("X-Actor-ID"))
		if err == nil {
			logManagementAction(r, "sso.test.start", ssoAuditTarget(r))
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
		selected, ok := selectSSOConnection(w, r, module)
		if !ok {
			return
		}
		module := &selected
		view, err := module.ChangeSSO(r.Context(), input.Action, input.ExpectedRevision, r.Header.Get("X-Actor-ID"))
		if err == nil {
			logManagementAction(r, "sso."+input.Action, ssoAuditTarget(r))
		}
		writeSSOResult(w, view, err)
	}))
	mux.HandleFunc("GET /internal/v1/sso/connections", admin(func(w http.ResponseWriter, r *http.Request) {
		views, err := module.SSOManager().Connections(r.Context())
		writeSSOResult(w, map[string]any{"data": views}, err)
	}))
	mux.HandleFunc("POST /internal/v1/sso/connections", admin(func(w http.ResponseWriter, r *http.Request) {
		var input modules.SSOConnection
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		err := module.SSOManager().CreateConnection(r.Context(), input)
		if err == nil {
			logManagementAction(r, "sso.connection.create", input.ID)
		}
		writeSSOResult(w, input, err)
	}))
	// Secrets are only exposed over the existing authenticated service channel.
	mux.HandleFunc("GET /internal/v1/sso/login-connections", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		views, err := module.SSOManager().Connections(r.Context())
		connections := []modules.SSOConnection{}
		for _, view := range views {
			if view.Active != nil && view.Active.Enabled {
				connections = append(connections, view.SSOConnection)
			}
		}
		writeSSOResult(w, map[string]any{"data": connections}, err)
	}))
	mux.HandleFunc("GET /internal/v1/sso/active", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if module.SSOManager() == nil {
			writeSSOResult(w, nil, nil)
			return
		}
		selected, ok := selectSSOConnection(w, r, module)
		if !ok {
			return
		}
		module := &selected
		state, _, err := module.SSOManager().Load(r.Context())
		writeSSOResult(w, state.Active, err)
	}))
	mux.HandleFunc("GET /internal/v1/sso/test-profile", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		selected, ok := selectSSOConnection(w, r, module)
		if !ok {
			return
		}
		module := &selected
		profile, err := module.SSOManager().TestProfile(r.Context())
		writeSSOResult(w, profile, err)
	}))
	mux.HandleFunc("POST /internal/v1/sso/verify-test", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ProfileID   string `json:"profile_id"`
			Ticket      string `json:"ticket"`
			Token       string `json:"token"`
			Nonce       string `json:"nonce"`
			AccessToken string `json:"access_token,omitempty"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		selected, ok := selectSSOConnection(w, r, module)
		if !ok {
			return
		}
		module := &selected
		err := module.VerifySSOTest(r.Context(), input.ProfileID, input.Ticket, input.Token, input.Nonce, input.AccessToken)
		if err == nil {
			logManagementAction(r, "sso.test.passed", ssoAuditTarget(r))
		}
		writeSSOResult(w, map[string]bool{"verified": err == nil}, err)
	}))
	mux.HandleFunc("POST /internal/v1/sso/sessions", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var input modules.SSOBrowserLogin
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		session, err := module.CreateSSOBrowserSession(r.Context(), input)
		writeSSOResult(w, session, err)
	}))
	mux.HandleFunc("POST /internal/v1/sso/sessions/revoke", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var input struct {
			Token string `json:"token"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		err := module.RevokeSSOBrowserSession(r.Context(), input.Token)
		writeSSOResult(w, map[string]bool{"revoked": err == nil}, err)
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

func selectSSOConnection(w http.ResponseWriter, r *http.Request, module *modules.AuthModule) (modules.AuthModule, bool) {
	if r.URL.Query().Get("connection") == "" {
		return *module, true
	}
	selected, err := module.WithSSOConnection(r.Context(), r.URL.Query().Get("connection"))
	if err != nil {
		writeSSOResult(w, nil, err)
		return *module, false
	}
	return selected, true
}

func ssoAuditTarget(r *http.Request) string {
	id := r.URL.Query().Get("connection")
	if id == "" || id == "default" {
		return "browser-sso"
	}
	return "browser-sso/" + id
}
