package main

import (
	"errors"
	"net/http"
	"strconv"

	"ai-gateway-auth/internal/modules"
)

func registerTeamOrganizationRoutes(mux *http.ServeMux, module *modules.AuthModule, secret string) {
	mux.HandleFunc("GET /internal/v1/organizations/page", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		q := modules.OrganizationPageQuery{Search: values.Get("search"), Status: values.Get("status"), Limit: 25}
		var err error
		if values.Has("offset") {
			q.Offset, err = strconv.Atoi(values.Get("offset"))
		}
		if err == nil && values.Has("limit") {
			q.Limit, err = strconv.Atoi(values.Get("limit"))
		}
		if err != nil || !q.Valid() {
			http.Error(w, "invalid organization page", 400)
			return
		}
		page, err := module.OrganizationPage(r.Context(), q)
		if err != nil {
			writeTeamOrganizationFailure(w, err)
			return
		}
		writeManagementJSON(w, 200, page)
	}))
	mux.HandleFunc("POST /internal/v1/teams", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Team           modules.DirectoryTeam `json:"team"`
			OrganizationID string                `json:"organization_id"`
		}
		if !decodeManagementJSON(w, r, &input) {
			return
		}
		team, err := module.CreateTeamInOrganization(r.Context(), input.Team, input.OrganizationID)
		if err != nil {
			writeTeamOrganizationFailure(w, err)
			return
		}
		logManagementAction(r, "team.create", team.ID)
		writeManagementJSON(w, 201, team)
	}))
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		mux.HandleFunc(method+" /internal/v1/teams/{id}/organization", managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
			var result modules.TeamOrganization
			var err error
			if r.Method == http.MethodGet {
				result, err = module.TeamOrganization(r.Context(), r.PathValue("id"))
			} else {
				var input struct {
					OrganizationID string `json:"organization_id"`
				}
				if !decodeManagementJSON(w, r, &input) {
					return
				}
				result, err = module.ChangeTeamOrganization(r.Context(), r.PathValue("id"), input.OrganizationID, r.Method == http.MethodDelete)
			}
			if err != nil {
				writeTeamOrganizationFailure(w, err)
				return
			}
			if r.Method != http.MethodGet {
				logManagementAction(r, "team.organization."+r.Method, result.TeamID)
			}
			writeManagementJSON(w, 200, result)
		}))
	}
}

func writeTeamOrganizationFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, modules.ErrInvalidDirectoryEntry):
		http.Error(w, "invalid organization or team", 400)
	case errors.Is(err, modules.ErrDirectoryNotFound):
		http.Error(w, "organization assignment not found", 404)
	case errors.Is(err, modules.ErrOrganizationTeamConflict), errors.Is(err, modules.ErrDirectoryConflict):
		http.Error(w, "organization assignment conflicts with existing ownership", 409)
	default:
		http.Error(w, "organization directory unavailable", 503)
	}
}
