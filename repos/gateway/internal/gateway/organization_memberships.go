package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type OrganizationMembership struct {
	OrganizationID string    `json:"organization_id"`
	UserID         string    `json:"user_id"`
	Roles          []string  `json:"roles"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type OrganizationMembershipPage struct {
	Data   []OrganizationMembership `json:"data"`
	Total  int                      `json:"total"`
	Offset int                      `json:"offset"`
	Limit  int                      `json:"limit"`
}
type OrganizationMembershipClient interface {
	ListOrganizationMemberships(context.Context, ManagementAudit, string, int, int) (OrganizationMembershipPage, error)
	PutOrganizationMembership(context.Context, ManagementAudit, OrganizationMembership) (OrganizationMembership, error)
}

func (c *RemoteManagementClient) ListOrganizationMemberships(ctx context.Context, audit ManagementAudit, org string, offset, limit int) (OrganizationMembershipPage, error) {
	query := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}}
	return managementCall[struct{}, OrganizationMembershipPage](ctx, c, http.MethodGet, "/internal/v1/organizations/"+url.PathEscape(org)+"/members?"+query.Encode(), audit, struct{}{})
}
func (c *RemoteManagementClient) PutOrganizationMembership(ctx context.Context, audit ManagementAudit, member OrganizationMembership) (OrganizationMembership, error) {
	return managementCall[OrganizationMembership, OrganizationMembership](ctx, c, http.MethodPut, "/internal/v1/organizations/"+url.PathEscape(member.OrganizationID)+"/members/"+url.PathEscape(member.UserID), audit, member)
}
func (h Handler) ListOrganizationMemberships(w http.ResponseWriter, r *http.Request) {
	req, ok := h.organizationAdmin(w, r)
	if !ok {
		return
	}
	client, ok := h.organizations.(OrganizationMembershipClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "organization membership management is not configured")
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
	page, err := client.ListOrganizationMemberships(r.Context(), managementAudit(req), r.PathValue("id"), offset, limit)
	if err != nil {
		writeDirectoryFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
func (h Handler) PutOrganizationMembership(w http.ResponseWriter, r *http.Request) {
	req, ok := h.organizationAdmin(w, r)
	if !ok {
		return
	}
	client, ok := h.organizations.(OrganizationMembershipClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "organization membership management is not configured")
		return
	}
	var member OrganizationMembership
	if !decodeDirectoryJSON(w, r, &member) {
		return
	}
	member.OrganizationID, member.UserID = r.PathValue("id"), r.PathValue("user_id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "organization.member.upsert", TargetType: "organization", TargetID: member.OrganizationID, Details: map[string]any{"user_id": member.UserID}}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := client.PutOrganizationMembership(r.Context(), audit, member)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}
