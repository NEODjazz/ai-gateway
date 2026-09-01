package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"ai-gateway-auth/internal/modules"
)

const managementTokenHeader = "X-Management-Token"

func registerManagementRoutes(mux *http.ServeMux, module *modules.AuthModule, sharedSecret string) {
	registerIdentityDirectoryRoutes(mux, module, sharedSecret)
	mux.HandleFunc("GET /internal/v1/keys", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		query := modules.VirtualKeyListQuery{Limit: 100}
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 500 {
				http.Error(w, "invalid virtual key list limit", http.StatusBadRequest)
				return
			}
			query.Limit = parsed
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 || parsed > 1_000_000 {
				http.Error(w, "invalid virtual key list offset", http.StatusBadRequest)
				return
			}
			query.Offset = parsed
		}
		query.Search = r.URL.Query().Get("search")
		query.OrganizationID = r.URL.Query().Get("organization_id")
		query.TeamID = r.URL.Query().Get("team_id")
		query.UserID = r.URL.Query().Get("user_id")
		query.KeyID = r.URL.Query().Get("key_id")
		query.AccessGroupID = r.URL.Query().Get("access_group_id")
		query.Status = r.URL.Query().Get("status")
		query.SortBy = r.URL.Query().Get("sort_by")
		query.SortOrder = r.URL.Query().Get("sort_order")
		page, err := module.ListVirtualKeysPage(r.Context(), query)
		if errors.Is(err, modules.ErrInvalidVirtualKey) {
			http.Error(w, "invalid virtual key list query", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "virtual key listing failed", http.StatusServiceUnavailable)
			return
		}
		writeManagementJSON(w, http.StatusOK, page)
	}))
	mux.HandleFunc("POST /internal/v1/keys", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		spec, ok := decodeManagedVirtualKey(w, r)
		if !ok {
			return
		}
		issued, err := module.CreateVirtualKey(r.Context(), spec)
		if errors.Is(err, modules.ErrInvalidVirtualKey) {
			http.Error(w, "invalid virtual key policy", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "virtual key creation failed", http.StatusServiceUnavailable)
			return
		}
		logManagementAction(r, "virtual_key.create", issued.ID)
		writeManagementJSON(w, http.StatusCreated, issued)
	}))
	mux.HandleFunc("POST /internal/v1/keys/{id}/rotate", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		spec, ok := decodeManagedVirtualKey(w, r)
		if !ok {
			return
		}
		issued, err := module.RotateVirtualKey(r.Context(), r.PathValue("id"), spec)
		if errors.Is(err, modules.ErrInvalidVirtualKey) {
			http.Error(w, "invalid virtual key policy", http.StatusBadRequest)
			return
		}
		if errors.Is(err, modules.ErrVirtualKeyNotFound) {
			http.Error(w, "virtual key not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "virtual key rotation failed", http.StatusServiceUnavailable)
			return
		}
		logManagementAction(r, "virtual_key.rotate", issued.ID)
		writeManagementJSON(w, http.StatusCreated, issued)
	}))
	mux.HandleFunc("PUT /internal/v1/keys/{id}", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		spec, ok := decodeManagedVirtualKey(w, r)
		if !ok {
			return
		}
		updated, err := module.UpdateVirtualKey(r.Context(), r.PathValue("id"), spec)
		if errors.Is(err, modules.ErrInvalidVirtualKey) {
			http.Error(w, "invalid virtual key policy", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "virtual key update failed", http.StatusServiceUnavailable)
			return
		}
		if !updated {
			http.Error(w, "virtual key not found", http.StatusNotFound)
			return
		}
		logManagementAction(r, "virtual_key.update", r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, route := range []struct {
		pattern  string
		disabled bool
		action   string
	}{
		{"POST /internal/v1/keys/{id}/disable", true, "virtual_key.disable"},
		{"POST /internal/v1/keys/{id}/enable", false, "virtual_key.enable"},
	} {
		route := route
		mux.HandleFunc(route.pattern, managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
			updated, err := module.SetVirtualKeyDisabled(r.Context(), r.PathValue("id"), route.disabled)
			if err != nil {
				http.Error(w, "virtual key status update failed", http.StatusServiceUnavailable)
				return
			}
			if !updated {
				http.Error(w, "virtual key not found or status unchanged", http.StatusNotFound)
				return
			}
			logManagementAction(r, route.action, r.PathValue("id"))
			w.WriteHeader(http.StatusNoContent)
		}))
	}
	mux.HandleFunc("DELETE /internal/v1/keys/{id}", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		revoked, err := module.RevokeVirtualKey(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "virtual key revocation failed", http.StatusServiceUnavailable)
			return
		}
		if !revoked {
			http.Error(w, "virtual key not found", http.StatusNotFound)
			return
		}
		logManagementAction(r, "virtual_key.revoke", r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	}))
}

func registerIdentityDirectoryRoutes(mux *http.ServeMux, module *modules.AuthModule, sharedSecret string) {
	mux.HandleFunc("GET /internal/v1/organizations", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		limit, ok := managementLimit(w, r)
		if !ok {
			return
		}
		organizations, err := module.ListOrganizations(r.Context(), limit)
		if err != nil {
			http.Error(w, "organization directory unavailable", http.StatusServiceUnavailable)
			return
		}
		writeManagementJSON(w, http.StatusOK, map[string]any{"data": organizations})
	}))
	mux.HandleFunc("PUT /internal/v1/organizations/{id}", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		var organization modules.Organization
		if !decodeManagementJSON(w, r, &organization) {
			return
		}
		organization.ID = r.PathValue("id")
		saved, err := module.PutOrganization(r.Context(), organization)
		if errors.Is(err, modules.ErrInvalidDirectoryEntry) {
			http.Error(w, "invalid organization", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "organization directory unavailable", http.StatusServiceUnavailable)
			return
		}
		logManagementAction(r, "organization.upsert", saved.ID)
		writeManagementJSON(w, http.StatusOK, saved)
	}))
	mux.HandleFunc("PUT /internal/v1/organizations/{id}/teams/{team_id}", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		saved, err := module.PutOrganizationTeam(r.Context(), r.PathValue("id"), r.PathValue("team_id"))
		if errors.Is(err, modules.ErrInvalidDirectoryEntry) {
			http.Error(w, "invalid organization team", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "organization directory unavailable", http.StatusServiceUnavailable)
			return
		}
		logManagementAction(r, "organization.team.upsert", saved.ID+":"+r.PathValue("team_id"))
		writeManagementJSON(w, http.StatusOK, saved)
	}))
	mux.HandleFunc("GET /internal/v1/users", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		limit, ok := managementLimit(w, r)
		if !ok {
			return
		}
		users, err := module.ListDirectoryUsers(r.Context(), r.URL.Query().Get("team_id"), limit)
		if err != nil {
			http.Error(w, "identity directory unavailable", http.StatusServiceUnavailable)
			return
		}
		writeManagementJSON(w, http.StatusOK, map[string]any{"data": users})
	}))
	mux.HandleFunc("PUT /internal/v1/users/{id}", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		var user modules.DirectoryUser
		if !decodeManagementJSON(w, r, &user) {
			return
		}
		user.ID = r.PathValue("id")
		saved, err := module.PutDirectoryUser(r.Context(), user)
		if errors.Is(err, modules.ErrInvalidDirectoryEntry) {
			http.Error(w, "invalid user", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "identity directory unavailable", http.StatusServiceUnavailable)
			return
		}
		logManagementAction(r, "user.upsert", saved.ID)
		writeManagementJSON(w, http.StatusOK, saved)
	}))
	mux.HandleFunc("GET /internal/v1/teams", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		limit, ok := managementLimit(w, r)
		if !ok {
			return
		}
		teams, err := module.ListDirectoryTeams(r.Context(), r.URL.Query().Get("team_id"), limit)
		if err != nil {
			http.Error(w, "identity directory unavailable", http.StatusServiceUnavailable)
			return
		}
		writeManagementJSON(w, http.StatusOK, map[string]any{"data": teams})
	}))
	mux.HandleFunc("PUT /internal/v1/teams/{id}", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		var team modules.DirectoryTeam
		if !decodeManagementJSON(w, r, &team) {
			return
		}
		team.ID = r.PathValue("id")
		saved, err := module.PutDirectoryTeam(r.Context(), team)
		if errors.Is(err, modules.ErrInvalidDirectoryEntry) {
			http.Error(w, "invalid team", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "identity directory unavailable", http.StatusServiceUnavailable)
			return
		}
		logManagementAction(r, "team.upsert", saved.ID)
		writeManagementJSON(w, http.StatusOK, saved)
	}))
	mux.HandleFunc("PUT /internal/v1/teams/{id}/members/{user_id}", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		var membership modules.TeamMembership
		if !decodeManagementJSON(w, r, &membership) {
			return
		}
		membership.TeamID = r.PathValue("id")
		membership.UserID = r.PathValue("user_id")
		saved, err := module.PutTeamMembership(r.Context(), membership)
		if errors.Is(err, modules.ErrInvalidDirectoryEntry) {
			http.Error(w, "invalid membership", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "identity directory unavailable", http.StatusServiceUnavailable)
			return
		}
		logManagementAction(r, "team.membership.upsert", saved.TeamID+":"+saved.UserID)
		writeManagementJSON(w, http.StatusOK, saved)
	}))
}

func managementLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return 0, false
		}
		limit = parsed
	}
	return limit, true
}

func decodeManagementJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func managementAuthorized(sharedSecret string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get(managementTokenHeader)
		expectedHash := sha256.Sum256([]byte(sharedSecret))
		providedHash := sha256.Sum256([]byte(provided))
		if sharedSecret == "" || subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if strings.TrimSpace(r.Header.Get("X-Request-ID")) == "" || strings.TrimSpace(r.Header.Get("X-Actor-ID")) == "" || strings.TrimSpace(r.Header.Get("X-Actor-Credential-ID")) == "" {
			http.Error(w, "missing audit identity", http.StatusBadRequest)
			return
		}
		next(w, r)
	}
}

func decodeManagedVirtualKey(w http.ResponseWriter, r *http.Request) (modules.ManagedVirtualKey, bool) {
	var spec modules.ManagedVirtualKey
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		http.Error(w, "invalid virtual key policy", http.StatusBadRequest)
		return spec, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid virtual key policy", http.StatusBadRequest)
		return spec, false
	}
	return spec, true
}

func logManagementAction(r *http.Request, action, target string) {
	payload, _ := json.Marshal(map[string]string{
		"event": "management_action", "request_id": r.Header.Get("X-Request-ID"),
		"actor_id": r.Header.Get("X-Actor-ID"), "actor_credential_id": r.Header.Get("X-Actor-Credential-ID"),
		"action": action, "target_id": target,
	})
	log.Print(string(payload))
}

func writeManagementJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
