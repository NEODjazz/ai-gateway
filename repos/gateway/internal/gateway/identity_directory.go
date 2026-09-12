package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type DirectoryUser struct {
	ID         string     `json:"id"`
	ExternalID string     `json:"external_id,omitempty"`
	Email      string     `json:"email,omitempty"`
	Name       string     `json:"name,omitempty"`
	Status     string     `json:"status"`
	Roles      []string   `json:"roles,omitempty"`
	TeamIDs    []string   `json:"team_ids,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}
type DirectoryTeam struct {
	ID          string     `json:"id"`
	ExternalID  string     `json:"external_id,omitempty"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Status      string     `json:"status"`
	MemberCount int        `json:"member_count"`
	MemberIDs   []string   `json:"member_ids,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}
type TeamMembership struct {
	TeamID    string    `json:"team_id"`
	UserID    string    `json:"user_id"`
	Roles     []string  `json:"roles,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DirectoryGroup struct {
	Team    DirectoryTeam `json:"team"`
	Members []string      `json:"members"`
}

type IdentityDirectoryClient interface {
	ListUsers(context.Context, ManagementAudit, string, int, int, bool, string, string) ([]DirectoryUser, int, error)
	GetUser(context.Context, ManagementAudit, string) (DirectoryUser, error)
	FindUser(context.Context, ManagementAudit, string, string) (DirectoryUser, bool, error)
	CreateUser(context.Context, ManagementAudit, DirectoryUser) (DirectoryUser, error)
	DeleteUser(context.Context, ManagementAudit, string) (DirectoryUser, error)
	PutUser(context.Context, ManagementAudit, string, DirectoryUser) (DirectoryUser, error)
	ListTeams(context.Context, ManagementAudit, string, int, int, bool, string, string) ([]DirectoryTeam, int, error)
	PutTeam(context.Context, ManagementAudit, string, DirectoryTeam) (DirectoryTeam, error)
	PutMembership(context.Context, ManagementAudit, string, string, TeamMembership) (TeamMembership, error)
	ListMemberships(context.Context, ManagementAudit, string, int) ([]TeamMembership, error)
	DeleteMembership(context.Context, ManagementAudit, string, string) error
	GetGroup(context.Context, ManagementAudit, string) (DirectoryGroup, error)
	FindGroup(context.Context, ManagementAudit, string, string) (DirectoryTeam, bool, error)
	SaveGroup(context.Context, ManagementAudit, DirectoryGroup, bool) (DirectoryGroup, error)
}

func (h Handler) WithIdentityDirectory(client IdentityDirectoryClient) Handler {
	h.directory = client
	return h
}

func (c *RemoteManagementClient) ListUsers(ctx context.Context, audit ManagementAudit, teamID string, offset, limit int, includeDeleted bool, sortBy, sortOrder string) ([]DirectoryUser, int, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}, "include_deleted": {strconv.FormatBool(includeDeleted)}}
	if teamID != "" {
		q.Set("team_id", teamID)
	}
	if sortBy != "" {
		q.Set("sort_by", sortBy)
		q.Set("sort_order", sortOrder)
	}
	result, err := managementCall[struct{}, struct {
		Data  []DirectoryUser `json:"data"`
		Total int             `json:"total"`
	}](ctx, c, http.MethodGet, "/internal/v1/users?"+q.Encode(), audit, struct{}{})
	return result.Data, result.Total, err
}
func (c *RemoteManagementClient) GetUser(ctx context.Context, audit ManagementAudit, id string) (DirectoryUser, error) {
	return managementCall[struct{}, DirectoryUser](ctx, c, http.MethodGet, "/internal/v1/users/"+url.PathEscape(id), audit, struct{}{})
}
func (c *RemoteManagementClient) FindUser(ctx context.Context, audit ManagementAudit, attribute, value string) (DirectoryUser, bool, error) {
	query := url.Values{"attribute": {attribute}, "value": {value}}
	user, err := managementCall[struct{}, DirectoryUser](ctx, c, http.MethodGet, "/internal/v1/users:lookup?"+query.Encode(), audit, struct{}{})
	var managementErr *ManagementError
	if errors.As(err, &managementErr) && managementErr.Status == http.StatusNotFound {
		return DirectoryUser{}, false, nil
	}
	return user, err == nil, err
}
func (c *RemoteManagementClient) CreateUser(ctx context.Context, audit ManagementAudit, user DirectoryUser) (DirectoryUser, error) {
	return managementCall[DirectoryUser, DirectoryUser](ctx, c, http.MethodPost, "/internal/v1/users", audit, user)
}
func (c *RemoteManagementClient) DeleteUser(ctx context.Context, audit ManagementAudit, id string) (DirectoryUser, error) {
	return managementCall[struct{}, DirectoryUser](ctx, c, http.MethodDelete, "/internal/v1/provisioned-users/"+url.PathEscape(id), audit, struct{}{})
}
func (c *RemoteManagementClient) PutUser(ctx context.Context, audit ManagementAudit, id string, user DirectoryUser) (DirectoryUser, error) {
	return managementCall[DirectoryUser, DirectoryUser](ctx, c, http.MethodPut, "/internal/v1/users/"+url.PathEscape(id), audit, user)
}
func (c *RemoteManagementClient) ListTeams(ctx context.Context, audit ManagementAudit, teamID string, offset, limit int, includeDeleted bool, sortBy, sortOrder string) ([]DirectoryTeam, int, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}, "include_deleted": {strconv.FormatBool(includeDeleted)}}
	if teamID != "" {
		q.Set("team_id", teamID)
	}
	if sortBy != "" {
		q.Set("sort_by", sortBy)
		q.Set("sort_order", sortOrder)
	}
	result, err := managementCall[struct{}, struct {
		Data  []DirectoryTeam `json:"data"`
		Total int             `json:"total"`
	}](ctx, c, http.MethodGet, "/internal/v1/teams?"+q.Encode(), audit, struct{}{})
	return result.Data, result.Total, err
}
func (c *RemoteManagementClient) PutTeam(ctx context.Context, audit ManagementAudit, id string, team DirectoryTeam) (DirectoryTeam, error) {
	return managementCall[DirectoryTeam, DirectoryTeam](ctx, c, http.MethodPut, "/internal/v1/teams/"+url.PathEscape(id), audit, team)
}
func (c *RemoteManagementClient) PutMembership(ctx context.Context, audit ManagementAudit, teamID, userID string, m TeamMembership) (TeamMembership, error) {
	return managementCall[TeamMembership, TeamMembership](ctx, c, http.MethodPut, "/internal/v1/teams/"+url.PathEscape(teamID)+"/members/"+url.PathEscape(userID), audit, m)
}
func (c *RemoteManagementClient) ListMemberships(ctx context.Context, audit ManagementAudit, teamID string, limit int) ([]TeamMembership, error) {
	result, err := managementCall[struct{}, struct {
		Data []TeamMembership `json:"data"`
	}](ctx, c, http.MethodGet, "/internal/v1/teams/"+url.PathEscape(teamID)+"/members?limit="+strconv.Itoa(limit), audit, struct{}{})
	return result.Data, err
}
func (c *RemoteManagementClient) DeleteMembership(ctx context.Context, audit ManagementAudit, teamID, userID string) error {
	_, err := managementCall[struct{}, struct{}](ctx, c, http.MethodDelete, "/internal/v1/teams/"+url.PathEscape(teamID)+"/members/"+url.PathEscape(userID), audit, struct{}{})
	return err
}
func (c *RemoteManagementClient) GetGroup(ctx context.Context, audit ManagementAudit, id string) (DirectoryGroup, error) {
	return managementCall[struct{}, DirectoryGroup](ctx, c, http.MethodGet, "/internal/v1/provisioned-groups/"+url.PathEscape(id), audit, struct{}{})
}
func (c *RemoteManagementClient) FindGroup(ctx context.Context, audit ManagementAudit, attribute, value string) (DirectoryTeam, bool, error) {
	query := url.Values{"attribute": {attribute}, "value": {value}}
	team, err := managementCall[struct{}, DirectoryTeam](ctx, c, http.MethodGet, "/internal/v1/provisioned-groups:lookup?"+query.Encode(), audit, struct{}{})
	var managementErr *ManagementError
	if errors.As(err, &managementErr) && managementErr.Status == http.StatusNotFound {
		return DirectoryTeam{}, false, nil
	}
	return team, err == nil, err
}
func (c *RemoteManagementClient) SaveGroup(ctx context.Context, audit ManagementAudit, group DirectoryGroup, create bool) (DirectoryGroup, error) {
	method, path := http.MethodPost, "/internal/v1/provisioned-groups"
	if !create {
		method, path = http.MethodPut, "/internal/v1/provisioned-groups/"+url.PathEscape(group.Team.ID)
	}
	return managementCall[DirectoryGroup, DirectoryGroup](ctx, c, method, path, audit, group)
}

func (h Handler) ListDirectoryUsers(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "")
	if !ok {
		return
	}
	limit, ok := directoryLimit(w, r)
	if !ok {
		return
	}
	offset, ok := directoryOffset(w, r)
	if !ok {
		return
	}
	teamID := directoryScope(req, r.URL.Query().Get("team_id"))
	users, total, err := h.directory.ListUsers(r.Context(), managementAudit(req), teamID, offset, limit, true, "", "")
	if err != nil {
		writeDirectoryFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": users, "total": total})
}
func (h Handler) ListDirectoryTeams(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "")
	if !ok {
		return
	}
	limit, ok := directoryLimit(w, r)
	if !ok {
		return
	}
	offset, ok := directoryOffset(w, r)
	if !ok {
		return
	}
	teamID := directoryScope(req, r.URL.Query().Get("team_id"))
	teams, total, err := h.directory.ListTeams(r.Context(), managementAudit(req), teamID, offset, limit, true, "", "")
	if err != nil {
		writeDirectoryFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": teams, "total": total})
}
func (h Handler) PutDirectoryUser(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	var user DirectoryUser
	if !decodeDirectoryJSON(w, r, &user) {
		return
	}
	user.ID = r.PathValue("id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "user.upsert", TargetType: "user", TargetID: user.ID}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := h.directory.PutUser(r.Context(), managementAudit(req), user.ID, user)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}
func (h Handler) PutDirectoryTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	req, ok := h.authorizeDirectory(w, r, id)
	if !ok {
		return
	}
	var team DirectoryTeam
	if !decodeDirectoryJSON(w, r, &team) {
		return
	}
	team.ID = id
	audit := managementAudit(req)
	event := AuditEvent{Action: "team.upsert", TargetType: "team", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := h.directory.PutTeam(r.Context(), managementAudit(req), id, team)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}
func (h Handler) PutTeamMembership(w http.ResponseWriter, r *http.Request) {
	teamID, userID := r.PathValue("id"), r.PathValue("user_id")
	req, ok := h.authorizeDirectory(w, r, teamID)
	if !ok {
		return
	}
	var membership TeamMembership
	if !decodeDirectoryJSON(w, r, &membership) {
		return
	}
	membership.TeamID, membership.UserID = teamID, userID
	audit := managementAudit(req)
	event := AuditEvent{Action: "team.membership.upsert", TargetType: "team", TargetID: teamID, Details: map[string]any{"user_id": userID}}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := h.directory.PutMembership(r.Context(), managementAudit(req), teamID, userID, membership)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}

func (h Handler) ListTeamMemberships(w http.ResponseWriter, r *http.Request) {
	teamID := r.PathValue("id")
	req, ok := h.authorizeDirectory(w, r, teamID)
	if !ok {
		return
	}
	limit, ok := directoryLimit(w, r)
	if !ok {
		return
	}
	memberships, err := h.directory.ListMemberships(r.Context(), managementAudit(req), teamID, limit)
	if err != nil {
		writeDirectoryFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": memberships})
}

func (h Handler) DeleteTeamMembership(w http.ResponseWriter, r *http.Request) {
	teamID, userID := r.PathValue("id"), r.PathValue("user_id")
	req, ok := h.authorizeDirectory(w, r, teamID)
	if !ok {
		return
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "team.membership.delete", TargetType: "team", TargetID: teamID, Details: map[string]any{"user_id": userID}}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := h.directory.DeleteMembership(r.Context(), audit, teamID, userID); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}

func writeDirectoryFailure(w http.ResponseWriter, err error) {
	var managementErr *ManagementError
	if errors.As(err, &managementErr) {
		switch managementErr.Status {
		case http.StatusBadRequest:
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid identity directory entry")
			return
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, "not_found", "identity directory entry not found")
			return
		case http.StatusConflict:
			writeError(w, http.StatusConflict, "conflict", "identity directory entry conflicts with an existing assignment")
			return
		}
	}
	writeError(w, http.StatusServiceUnavailable, "management_unavailable", "identity directory is unavailable")
}

func (h Handler) authorizeDirectory(w http.ResponseWriter, r *http.Request, targetTeam string) (modules.RequestContext, bool) {
	if h.directory == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "identity directory is not configured")
		return modules.RequestContext{}, false
	}
	req := modules.RequestContext{APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: requestID(r)}
	if err := h.pipeline.Run(r.Context(), &req); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
		return req, false
	}
	req.APIKey = ""
	if hasRole(req.Roles, "admin") {
		return req, true
	}
	if targetTeam == "__global__" {
		writeError(w, http.StatusForbidden, "forbidden", "global admin role is required")
		return req, false
	}
	if !hasRole(req.Roles, "team_admin") || req.TeamID == "" || (targetTeam != "" && targetTeam != req.TeamID) {
		writeError(w, http.StatusForbidden, "forbidden", "admin or matching team_admin role is required")
		return req, false
	}
	return req, true
}
func directoryScope(req modules.RequestContext, requested string) string {
	if hasRole(req.Roles, "admin") {
		return strings.TrimSpace(requested)
	}
	return req.TeamID
}
func directoryLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 500")
			return 0, false
		}
		limit = value
	}
	return limit, true
}
func directoryOffset(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("offset"))
	if raw == "" {
		return 0, true
	}
	offset, err := strconv.Atoi(raw)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "offset must be a non-negative integer")
		return 0, false
	}
	return offset, true
}
func decodeDirectoryJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid identity directory entry")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid identity directory entry")
		return false
	}
	return true
}
