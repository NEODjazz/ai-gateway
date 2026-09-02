package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type Organization struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	TeamIDs     []string  `json:"team_ids"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type OrganizationDirectoryClient interface {
	ListOrganizations(context.Context, ManagementAudit, int) ([]Organization, error)
	PutOrganization(context.Context, ManagementAudit, string, Organization) (Organization, error)
	PutOrganizationTeam(context.Context, ManagementAudit, string, string) (Organization, error)
	DeleteOrganizationTeam(context.Context, ManagementAudit, string, string) error
}

func (h Handler) WithOrganizations(client OrganizationDirectoryClient) Handler {
	h.organizations = client
	return h
}
func (c *RemoteManagementClient) ListOrganizations(ctx context.Context, audit ManagementAudit, limit int) ([]Organization, error) {
	result, err := managementCall[struct{}, struct {
		Data []Organization `json:"data"`
	}](ctx, c, http.MethodGet, "/internal/v1/organizations?limit="+strconv.Itoa(limit), audit, struct{}{})
	return result.Data, err
}
func (c *RemoteManagementClient) PutOrganization(ctx context.Context, audit ManagementAudit, id string, organization Organization) (Organization, error) {
	return managementCall[Organization, Organization](ctx, c, http.MethodPut, "/internal/v1/organizations/"+url.PathEscape(id), audit, organization)
}
func (c *RemoteManagementClient) PutOrganizationTeam(ctx context.Context, audit ManagementAudit, id, teamID string) (Organization, error) {
	return managementCall[struct{}, Organization](ctx, c, http.MethodPut, "/internal/v1/organizations/"+url.PathEscape(id)+"/teams/"+url.PathEscape(teamID), audit, struct{}{})
}
func (c *RemoteManagementClient) DeleteOrganizationTeam(ctx context.Context, audit ManagementAudit, id, teamID string) error {
	_, err := managementCall[struct{}, struct{}](ctx, c, http.MethodDelete, "/internal/v1/organizations/"+url.PathEscape(id)+"/teams/"+url.PathEscape(teamID), audit, struct{}{})
	return err
}

func (h Handler) ListOrganizations(w http.ResponseWriter, r *http.Request) {
	req, ok := h.organizationAdmin(w, r)
	if !ok {
		return
	}
	limit, ok := directoryLimit(w, r)
	if !ok {
		return
	}
	items, err := h.organizations.ListOrganizations(r.Context(), managementAudit(req), limit)
	if err != nil {
		writeDirectoryFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}
func (h Handler) PutOrganization(w http.ResponseWriter, r *http.Request) {
	req, ok := h.organizationAdmin(w, r)
	if !ok {
		return
	}
	var organization Organization
	if !decodeDirectoryJSON(w, r, &organization) {
		return
	}
	id := r.PathValue("id")
	organization.ID = id
	audit := managementAudit(req)
	event := AuditEvent{Action: "organization.upsert", TargetType: "organization", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := h.organizations.PutOrganization(r.Context(), audit, id, organization)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}
func (h Handler) PutOrganizationTeam(w http.ResponseWriter, r *http.Request) {
	req, ok := h.organizationAdmin(w, r)
	if !ok {
		return
	}
	id, teamID := r.PathValue("id"), r.PathValue("team_id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "organization.team.upsert", TargetType: "organization", TargetID: id, Details: map[string]any{"team_id": teamID}}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := h.organizations.PutOrganizationTeam(r.Context(), audit, id, teamID)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}
func (h Handler) DeleteOrganizationTeam(w http.ResponseWriter, r *http.Request) {
	req, ok := h.organizationAdmin(w, r)
	if !ok {
		return
	}
	id, teamID := r.PathValue("id"), r.PathValue("team_id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "organization.team.delete", TargetType: "organization", TargetID: id, Details: map[string]any{"team_id": teamID}}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := h.organizations.DeleteOrganizationTeam(r.Context(), audit, id, teamID); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}
func (h Handler) organizationAdmin(w http.ResponseWriter, r *http.Request) (modules.RequestContext, bool) {
	if h.organizations == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "organization directory is not configured")
		return modules.RequestContext{}, false
	}
	req, ok := h.authorizeAdmin(w, r)
	return req, ok
}
