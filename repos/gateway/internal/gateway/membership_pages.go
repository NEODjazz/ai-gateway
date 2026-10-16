package gateway

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type DirectoryMembership struct {
	TeamMembership
	UserName   string `json:"user_name"`
	UserEmail  string `json:"user_email"`
	UserStatus string `json:"user_status"`
	TeamName   string `json:"team_name"`
	TeamStatus string `json:"team_status"`
}

type MembershipPageFilter struct {
	TeamID    string
	UserID    string
	Search    string
	SortBy    string
	SortOrder string
	Offset    int
	Limit     int
}

type MembershipPage struct {
	Data   []DirectoryMembership `json:"data"`
	Total  int                   `json:"total"`
	Offset int                   `json:"offset"`
	Limit  int                   `json:"limit"`
}

type MembershipPageClient interface {
	ListMembershipsPage(context.Context, ManagementAudit, MembershipPageFilter) (MembershipPage, error)
	CreateMembership(context.Context, ManagementAudit, TeamMembership) (TeamMembership, error)
}

type TeamMetadataClient interface {
	GetTeamMetadata(context.Context, ManagementAudit, string) (DirectoryTeam, error)
}

func (c *RemoteManagementClient) GetTeamMetadata(ctx context.Context, audit ManagementAudit, id string) (DirectoryTeam, error) {
	return managementCall[struct{}, DirectoryTeam](ctx, c, http.MethodGet, "/internal/v1/teams/"+url.PathEscape(id)+"/metadata", audit, struct{}{})
}

func (h Handler) GetDirectoryTeamMetadata(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	actor, ok := h.authorizeDirectory(w, r, id)
	if !ok {
		return
	}
	client, ok := h.directory.(TeamMetadataClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "team metadata is unavailable")
		return
	}
	team, err := client.GetTeamMetadata(r.Context(), managementAudit(actor), id)
	if err != nil {
		writeDirectoryFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, team)
}

func (c *RemoteManagementClient) ListMembershipsPage(ctx context.Context, audit ManagementAudit, filter MembershipPageFilter) (MembershipPage, error) {
	q := url.Values{"team_id": {filter.TeamID}, "user_id": {filter.UserID}, "search": {filter.Search}, "sort_by": {filter.SortBy}, "sort_order": {filter.SortOrder}, "offset": {strconv.Itoa(filter.Offset)}, "limit": {strconv.Itoa(filter.Limit)}}
	return managementCall[struct{}, MembershipPage](ctx, c, http.MethodGet, "/internal/v1/team-memberships?"+q.Encode(), audit, struct{}{})
}
func (c *RemoteManagementClient) CreateMembership(ctx context.Context, audit ManagementAudit, member TeamMembership) (TeamMembership, error) {
	return managementCall[TeamMembership, TeamMembership](ctx, c, http.MethodPost, "/internal/v1/teams/"+url.PathEscape(member.TeamID)+"/members", audit, member)
}

func (h Handler) ListMembershipPages(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.authorizeDirectory(w, r, "")
	if !ok {
		return
	}
	q := r.URL.Query()
	filter := MembershipPageFilter{TeamID: directoryScope(actor, q.Get("team_id")), UserID: q.Get("user_id"), Search: q.Get("search"), SortBy: q.Get("sort_by"), SortOrder: q.Get("sort_order")}
	if filter.Limit, ok = directoryLimit(w, r); !ok {
		return
	}
	if filter.Offset, ok = directoryOffset(w, r); !ok {
		return
	}
	if (filter.TeamID == "" && filter.UserID == "") || len(filter.TeamID) > 256 || len(filter.UserID) > 256 || len(filter.Search) > 256 || strings.ContainsRune(filter.Search, 0) || (filter.SortBy != "" && !slices.Contains([]string{"user", "team", "joined"}, filter.SortBy)) || (filter.SortOrder != "" && filter.SortOrder != "asc" && filter.SortOrder != "desc") {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid membership filters; team_id or user_id is required")
		return
	}
	client, ok := h.directory.(MembershipPageClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "paged memberships are unavailable")
		return
	}
	page, err := client.ListMembershipsPage(r.Context(), managementAudit(actor), filter)
	if err != nil {
		writeDirectoryFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h Handler) CreateMembership(w http.ResponseWriter, r *http.Request) {
	teamID := r.PathValue("id")
	actor, ok := h.authorizeDirectory(w, r, teamID)
	if !ok {
		return
	}
	var input struct {
		UserID string   `json:"user_id"`
		Roles  []string `json:"roles"`
	}
	if !decodeDirectoryJSON(w, r, &input) {
		return
	}
	client, ok := h.directory.(MembershipPageClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "membership creation is unavailable")
		return
	}
	audit := managementAudit(actor)
	event := AuditEvent{Action: "team.membership.create", TargetType: "team", TargetID: teamID, Details: map[string]any{"user_id": input.UserID}}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	member, err := client.CreateMembership(r.Context(), audit, TeamMembership{TeamID: teamID, UserID: input.UserID, Roles: input.Roles})
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusCreated, member)
}
