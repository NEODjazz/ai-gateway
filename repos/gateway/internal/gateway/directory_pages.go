package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type DirectoryPageFilter struct {
	TeamID         string
	Search         string
	Status         string
	Role           string
	SortBy         string
	SortOrder      string
	Offset         int
	Limit          int
	IncludeDeleted bool
}

type DirectoryPageReader interface {
	ListUsersPage(context.Context, ManagementAudit, DirectoryPageFilter) ([]DirectoryUser, int, error)
	ListTeamsPage(context.Context, ManagementAudit, DirectoryPageFilter) ([]DirectoryTeam, int, error)
}

func directoryPage[T any](ctx context.Context, c *RemoteManagementClient, audit ManagementAudit, resource string, filter DirectoryPageFilter) ([]T, int, error) {
	q := url.Values{"team_id": {filter.TeamID}, "search": {filter.Search}, "status": {filter.Status}, "role": {filter.Role}, "offset": {strconv.Itoa(filter.Offset)}, "limit": {strconv.Itoa(filter.Limit)}, "include_deleted": {strconv.FormatBool(filter.IncludeDeleted)}, "sort_by": {filter.SortBy}, "sort_order": {filter.SortOrder}}
	result, err := managementCallWithLimit[struct{}, struct {
		Data             []T `json:"data"`
		Total            int `json:"total"`
		FilteringVersion int `json:"filtering_version"`
	}](ctx, c, http.MethodGet, "/internal/v1/"+resource+"?"+q.Encode(), audit, struct{}{}, 2<<20)
	if err == nil && (filter.Search != "" || filter.Status != "" || filter.Role != "") && result.FilteringVersion != 1 {
		return nil, 0, errors.New("directory backend does not support requested filters")
	}
	return result.Data, result.Total, err
}

func (c *RemoteManagementClient) ListUsersPage(ctx context.Context, audit ManagementAudit, filter DirectoryPageFilter) ([]DirectoryUser, int, error) {
	return directoryPage[DirectoryUser](ctx, c, audit, "users", filter)
}
func (c *RemoteManagementClient) ListTeamsPage(ctx context.Context, audit ManagementAudit, filter DirectoryPageFilter) ([]DirectoryTeam, int, error) {
	return directoryPage[DirectoryTeam](ctx, c, audit, "teams", filter)
}

func directoryPageFilter(w http.ResponseWriter, r *http.Request, users bool) (DirectoryPageFilter, bool) {
	q := r.URL.Query()
	f := DirectoryPageFilter{Search: strings.TrimSpace(q.Get("search")), Status: q.Get("status"), Role: q.Get("role"), SortBy: q.Get("sort_by"), SortOrder: q.Get("sort_order"), IncludeDeleted: true}
	var ok bool
	if f.Limit, ok = directoryLimit(w, r); !ok {
		return f, false
	}
	if f.Offset, ok = directoryOffset(w, r); !ok {
		return f, false
	}
	sorts := []string{"id", "displayName", "externalId", "status", "createdAt", "updatedAt"}
	if users {
		sorts = append(sorts, "userName")
	}
	if len(f.Search) > 256 || strings.ContainsRune(f.Search, 0) || len(f.Role) > 128 || (!users && f.Role != "") || strings.ContainsAny(f.Role, "\x00\r\n") || (f.Status != "" && f.Status != "active" && f.Status != "disabled") || (f.SortBy != "" && (!slices.Contains(sorts, f.SortBy) || (f.SortOrder != "ascending" && f.SortOrder != "descending"))) || (f.SortBy == "" && f.SortOrder != "") {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid directory filter or sort order")
		return f, false
	}
	if q.Has("include_deleted") {
		value, err := strconv.ParseBool(q.Get("include_deleted"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid include_deleted")
			return f, false
		}
		f.IncludeDeleted = value
	}
	return f, true
}

func (h Handler) GetDirectoryUser(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "")
	if !ok {
		return
	}
	user, err := h.directory.GetUser(r.Context(), managementAudit(req), r.PathValue("id"))
	if err != nil {
		writeDirectoryFailure(w, err)
		return
	}
	if !hasRole(req.Roles, "admin") {
		if !slices.Contains(user.TeamIDs, req.TeamID) {
			writeError(w, http.StatusNotFound, "not_found", "user was not found in your team")
			return
		}
		user.TeamIDs = []string{req.TeamID}
	}
	writeJSON(w, http.StatusOK, user)
}

func (h Handler) CreateDirectoryUser(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	var input struct {
		ID         string   `json:"id"`
		ExternalID string   `json:"external_id"`
		Email      string   `json:"email"`
		Name       string   `json:"name"`
		Status     string   `json:"status"`
		Roles      []string `json:"roles"`
	}
	if !decodeDirectoryJSON(w, r, &input) {
		return
	}
	user := DirectoryUser{ID: input.ID, ExternalID: input.ExternalID, Email: input.Email, Name: input.Name, Status: input.Status, Roles: input.Roles}
	event := AuditEvent{Action: "user.create", TargetType: "user", TargetID: user.ID}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := h.directory.CreateUser(r.Context(), audit, user)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusCreated, saved)
}

func (h Handler) CreateDirectoryTeam(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	var input struct {
		OrganizationID string `json:"organization_id"`
		ID             string `json:"id"`
		ExternalID     string `json:"external_id"`
		Name           string `json:"name"`
		Description    string `json:"description"`
		Status         string `json:"status"`
	}
	if !decodeDirectoryJSON(w, r, &input) {
		return
	}
	team := DirectoryTeam{ID: input.ID, ExternalID: input.ExternalID, Name: input.Name, Description: input.Description, Status: input.Status}
	var organizationClient TeamOrganizationClient
	if input.OrganizationID != "" {
		if len(input.OrganizationID) > 256 || strings.ContainsAny(input.OrganizationID, "\x00\r\n") {
			writeError(w, 400, "invalid_request", "invalid organization_id")
			return
		}
		var supported bool
		organizationClient, supported = h.organizations.(TeamOrganizationClient)
		if !supported {
			writeError(w, 503, "management_unavailable", "atomic team creation with organization is unavailable")
			return
		}
	}
	event := AuditEvent{Action: "team.create", TargetType: "team", TargetID: team.ID}
	if input.OrganizationID != "" {
		event.Details = map[string]any{"organization_id": input.OrganizationID}
	}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	var saved DirectoryGroup
	var err error
	if input.OrganizationID != "" {
		saved.Team, err = organizationClient.CreateTeamInOrganization(r.Context(), audit, team, input.OrganizationID)
	} else {
		saved, err = h.directory.SaveGroup(r.Context(), audit, DirectoryGroup{Team: team, Members: []string{}}, true)
	}
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusCreated, saved.Team)
}
