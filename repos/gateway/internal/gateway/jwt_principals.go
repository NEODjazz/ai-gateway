package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

type JWTPrincipalPolicy struct {
	Issuer         string   `json:"issuer"`
	Subject        string   `json:"subject"`
	Audience       string   `json:"audience"`
	UserID         string   `json:"user_id"`
	OrganizationID string   `json:"organization_id,omitempty"`
	TeamID         string   `json:"team_id,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	AccessGroupIDs []string `json:"access_group_ids,omitempty"`
	AllowedModels  []string `json:"allowed_models,omitempty"`
	AllowedTools   []string `json:"allowed_tools,omitempty"`
	RateLimitRPM   int      `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM   int      `json:"rate_limit_tpm,omitempty"`
	Enabled        bool     `json:"enabled"`
}
type JWTPrincipalPage struct {
	Data   []JWTPrincipalPolicy `json:"data"`
	Total  int                  `json:"total"`
	Limit  int                  `json:"limit"`
	Offset int                  `json:"offset"`
}
type JWTPrincipalClient interface {
	ListJWTPrincipals(context.Context, ManagementAudit, string, int, int) (JWTPrincipalPage, error)
	PutJWTPrincipal(context.Context, ManagementAudit, JWTPrincipalPolicy) (JWTPrincipalPolicy, error)
}

func (c *RemoteManagementClient) ListJWTPrincipals(ctx context.Context, audit ManagementAudit, user string, offset, limit int) (JWTPrincipalPage, error) {
	query := url.Values{"user_id": {user}, "offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}}
	return managementCall[struct{}, JWTPrincipalPage](ctx, c, http.MethodGet, "/internal/v1/jwt-principals?"+query.Encode(), audit, struct{}{})
}
func (c *RemoteManagementClient) PutJWTPrincipal(ctx context.Context, audit ManagementAudit, p JWTPrincipalPolicy) (JWTPrincipalPolicy, error) {
	return managementCall[JWTPrincipalPolicy, JWTPrincipalPolicy](ctx, c, http.MethodPut, "/internal/v1/jwt-principals", audit, p)
}
func (h Handler) ListJWTPrincipals(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	client, ok := h.directory.(JWTPrincipalClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "principal management is not configured")
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
	page, err := client.ListJWTPrincipals(r.Context(), managementAudit(req), r.URL.Query().Get("user_id"), offset, limit)
	if err != nil {
		writeDirectoryFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
func (h Handler) PutJWTPrincipal(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	client, ok := h.directory.(JWTPrincipalClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "principal management is not configured")
		return
	}
	var p JWTPrincipalPolicy
	if !decodeDirectoryJSON(w, r, &p) {
		return
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "jwt_principal.upsert", TargetType: "user", TargetID: p.UserID}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := client.PutJWTPrincipal(r.Context(), audit, p)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}
