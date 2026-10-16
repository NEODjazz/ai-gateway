package main

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"ai-gateway-auth/internal/modules"
)

func registerOrganizationMembershipRoutes(mux *http.ServeMux, module *modules.AuthModule, secret string) {
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
	mux.HandleFunc("GET /internal/v1/organizations/{id}/members", admin(func(w http.ResponseWriter, r *http.Request) {
		limit, ok := managementLimit(w, r)
		if !ok {
			return
		}
		offset, ok := managementOffset(w, r)
		if !ok {
			return
		}
		page, err := module.ListOrganizationMemberships(r.Context(), r.PathValue("id"), offset, limit)
		if err != nil {
			writeOrganizationMembershipFailure(w, err)
			return
		}
		writeManagementJSON(w, http.StatusOK, page)
	}))
	mux.HandleFunc("PUT /internal/v1/organizations/{id}/members/{user_id}", admin(func(w http.ResponseWriter, r *http.Request) {
		var member modules.OrganizationMembership
		if !decodeManagementJSON(w, r, &member) {
			return
		}
		member.OrganizationID, member.UserID = r.PathValue("id"), r.PathValue("user_id")
		saved, err := module.PutOrganizationMembership(r.Context(), member)
		if err != nil {
			writeOrganizationMembershipFailure(w, err)
			return
		}
		logManagementAction(r, "organization.member.upsert", member.OrganizationID+":"+member.UserID)
		writeManagementJSON(w, http.StatusOK, saved)
	}))
}

func writeOrganizationMembershipFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, modules.ErrInvalidDirectoryEntry):
		http.Error(w, "invalid organization membership", http.StatusBadRequest)
	case errors.Is(err, modules.ErrDirectoryNotFound):
		http.Error(w, "organization not found", http.StatusNotFound)
	default:
		http.Error(w, "organization directory unavailable", http.StatusServiceUnavailable)
	}
}
