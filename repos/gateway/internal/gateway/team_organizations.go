package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type OrganizationMetadata struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type TeamOrganization struct {
	TeamID       string                `json:"team_id"`
	Organization *OrganizationMetadata `json:"organization"`
}
type OrganizationPageQuery struct {
	Search, Status string
	Offset, Limit  int
}
type OrganizationPage struct {
	Data   []OrganizationMetadata `json:"data"`
	Total  int                    `json:"total"`
	Offset int                    `json:"offset"`
	Limit  int                    `json:"limit"`
}
type TeamOrganizationClient interface {
	TeamOrganization(context.Context, ManagementAudit, string) (TeamOrganization, error)
	ChangeTeamOrganization(context.Context, ManagementAudit, string, string, bool) (TeamOrganization, error)
	OrganizationPage(context.Context, ManagementAudit, OrganizationPageQuery) (OrganizationPage, error)
	CreateTeamInOrganization(context.Context, ManagementAudit, DirectoryTeam, string) (DirectoryTeam, error)
}

func (c *RemoteManagementClient) TeamOrganization(ctx context.Context, audit ManagementAudit, id string) (TeamOrganization, error) {
	return managementCall[struct{}, TeamOrganization](ctx, c, http.MethodGet, "/internal/v1/teams/"+url.PathEscape(id)+"/organization", audit, struct{}{})
}
func (c *RemoteManagementClient) ChangeTeamOrganization(ctx context.Context, audit ManagementAudit, id, orgID string, remove bool) (TeamOrganization, error) {
	method := http.MethodPut
	if remove {
		method = http.MethodDelete
	}
	return managementCall[map[string]string, TeamOrganization](ctx, c, method, "/internal/v1/teams/"+url.PathEscape(id)+"/organization", audit, map[string]string{"organization_id": orgID})
}
func (c *RemoteManagementClient) OrganizationPage(ctx context.Context, audit ManagementAudit, q OrganizationPageQuery) (OrganizationPage, error) {
	values := url.Values{"search": {q.Search}, "status": {q.Status}, "offset": {strconv.Itoa(q.Offset)}, "limit": {strconv.Itoa(q.Limit)}}
	return managementCall[struct{}, OrganizationPage](ctx, c, http.MethodGet, "/internal/v1/organizations/page?"+values.Encode(), audit, struct{}{})
}
func (c *RemoteManagementClient) CreateTeamInOrganization(ctx context.Context, audit ManagementAudit, team DirectoryTeam, orgID string) (DirectoryTeam, error) {
	input := struct {
		Team           DirectoryTeam `json:"team"`
		OrganizationID string        `json:"organization_id"`
	}{team, orgID}
	return managementCall[any, DirectoryTeam](ctx, c, http.MethodPost, "/internal/v1/teams", audit, input)
}

func (h Handler) ListOrganizationPage(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.organizationAdmin(w, r)
	if !ok {
		return
	}
	client, ok := h.organizations.(TeamOrganizationClient)
	if !ok {
		writeError(w, 503, "management_unavailable", "organization pages are unavailable")
		return
	}
	values := r.URL.Query()
	q := OrganizationPageQuery{Search: values.Get("search"), Status: values.Get("status"), Limit: 25}
	var err error
	if values.Has("offset") {
		q.Offset, err = strconv.Atoi(values.Get("offset"))
	}
	if err == nil && values.Has("limit") {
		q.Limit, err = strconv.Atoi(values.Get("limit"))
	}
	if err != nil || q.Offset < 0 || q.Limit < 1 || q.Limit > 100 || len(q.Search) > 256 || strings.ContainsRune(q.Search, 0) || (q.Status != "" && q.Status != "active" && q.Status != "disabled") {
		writeError(w, 400, "invalid_request", "invalid organization page")
		return
	}
	page, err := client.OrganizationPage(r.Context(), managementAudit(actor), q)
	if err != nil {
		writeDirectoryFailure(w, err)
		return
	}
	if page.Offset != q.Offset || page.Limit != q.Limit || page.Total < 0 || len(page.Data) > q.Limit {
		writeError(w, 502, "invalid_management_response", "invalid organization page")
		return
	}
	for _, row := range page.Data {
		if q.Status != "" && row.Status != q.Status {
			writeError(w, 502, "invalid_management_response", "organization filter was not applied")
			return
		}
	}
	writeJSON(w, 200, page)
}

func (h Handler) TeamOrganizationSettings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var actor modules.RequestContext
	var ok bool
	if r.Method == http.MethodGet {
		actor, ok = h.authorizeDirectory(w, r, id)
	} else {
		actor, ok = h.authorizeAdmin(w, r)
	}
	if !ok {
		return
	}
	client, ok := h.organizations.(TeamOrganizationClient)
	if !ok {
		writeError(w, 503, "management_unavailable", "team organization management is unavailable")
		return
	}
	audit := managementAudit(actor)
	var result TeamOrganization
	var err error
	if r.Method == http.MethodGet {
		result, err = client.TeamOrganization(r.Context(), audit, id)
	} else {
		var input struct {
			OrganizationID string `json:"organization_id"`
		}
		if !decodeDirectoryJSON(w, r, &input) {
			return
		}
		if input.OrganizationID == "" || len(input.OrganizationID) > 256 {
			writeError(w, 400, "invalid_request", "organization_id is required")
			return
		}
		event := AuditEvent{Action: "team.organization.update", TargetType: "team", TargetID: id, Details: map[string]any{"organization_id": input.OrganizationID, "remove": r.Method == http.MethodDelete}}
		if !h.auditMutation(r.Context(), audit, event) {
			writeError(w, 503, "audit_unavailable", "audit service is unavailable")
			return
		}
		result, err = client.ChangeTeamOrganization(r.Context(), audit, id, input.OrganizationID, r.Method == http.MethodDelete)
		if err != nil {
			h.auditOutcome(r.Context(), audit, event, "failed")
		} else {
			h.auditOutcome(r.Context(), audit, event, "succeeded")
		}
	}
	if err != nil {
		writeDirectoryFailure(w, err)
		return
	}
	if result.TeamID != id {
		writeError(w, 502, "invalid_management_response", "organization assignment does not match this team")
		return
	}
	writeJSON(w, 200, result)
}
