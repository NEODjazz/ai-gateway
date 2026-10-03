package main

import (
	"ai-gateway-auth/internal/modules"
	"errors"
	"net/http"
)

func registerJWTPrincipalRoutes(mux *http.ServeMux, module *modules.AuthModule, secret string) {
	mux.HandleFunc("POST /internal/v1/jwt-principals:reauthorize", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Identity     *modules.JWTIdentity `json:"jwt_identity"`
			UserID       string               `json:"user_id"`
			CredentialID string               `json:"credential_id"`
			Roles        []string             `json:"roles"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		req := modules.RequestContext{JWTIdentity: input.Identity, UserID: input.UserID, CredentialID: input.CredentialID, Roles: input.Roles}
		if err := module.ReauthorizeJWTPrincipal(r.Context(), &req); err != nil {
			if errors.Is(err, modules.ErrUnauthorized) {
				http.Error(w, "principal authorization revoked", http.StatusUnauthorized)
			} else {
				http.Error(w, "identity directory unavailable", http.StatusServiceUnavailable)
			}
			return
		}
		writeManagementJSON(w, http.StatusOK, map[string]bool{"authorized": true})
	}))
	mux.HandleFunc("GET /internal/v1/jwt-principals", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		limit, ok := managementLimit(w, r)
		if !ok {
			return
		}
		offset, ok := managementOffset(w, r)
		if !ok {
			return
		}
		page, err := module.ListJWTPrincipals(r.Context(), r.URL.Query().Get("user_id"), offset, limit)
		if err != nil {
			writePrincipalFailure(w, err)
			return
		}
		writeManagementJSON(w, http.StatusOK, page)
	}))
	mux.HandleFunc("PUT /internal/v1/jwt-principals", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		var p modules.JWTPrincipalPolicy
		if !decodeManagementJSON(w, r, &p) {
			return
		}
		saved, err := module.PutJWTPrincipal(r.Context(), p)
		if err != nil {
			writePrincipalFailure(w, err)
			return
		}
		logManagementAction(r, "jwt_principal.upsert", p.UserID)
		writeManagementJSON(w, http.StatusOK, saved)
	}))
}
func writePrincipalFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, modules.ErrInvalidDirectoryEntry):
		http.Error(w, "invalid principal policy", http.StatusBadRequest)
	case errors.Is(err, modules.ErrDirectoryConflict):
		http.Error(w, "principal user and organization ownership are immutable", http.StatusConflict)
	default:
		http.Error(w, "identity directory unavailable", http.StatusServiceUnavailable)
	}
}
