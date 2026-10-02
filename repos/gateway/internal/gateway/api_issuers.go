package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

type APIIssuer struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	OrganizationID string `json:"organization_id,omitempty"`
	Issuer         string `json:"issuer"`
	Audience       string `json:"audience"`
}
type APIIssuerConfig struct {
	JWKSURL      string            `json:"jwks_url"`
	RolesClaim   string            `json:"roles_claim"`
	RoleMappings map[string]string `json:"role_mappings"`
}
type APIIssuerProfile struct {
	APIIssuerConfig
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}
type APIIssuerView struct {
	APIIssuer
	Revision       int64             `json:"revision"`
	Active         *APIIssuerProfile `json:"active"`
	Draft          *APIIssuerProfile `json:"draft"`
	CanRollback    bool              `json:"can_rollback"`
	LastTestAt     int64             `json:"last_test_at,omitempty"`
	LastTestStatus string            `json:"last_test_status,omitempty"`
	TestExpiresAt  int64             `json:"test_expires_at,omitempty"`
}
type apiIssuerCreate struct {
	APIIssuer
	APIIssuerConfig
}
type apiIssuerDraft struct {
	APIIssuerConfig
	ExpectedRevision int64 `json:"expected_revision"`
}
type apiIssuerTest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Token            string `json:"token"`
}
type APIIssuerManagementClient interface {
	APIIssuers(context.Context, ManagementAudit) ([]APIIssuerView, error)
	MutateAPIIssuer(context.Context, ManagementAudit, string, string, any) (APIIssuerView, error)
}

func (c *RemoteManagementClient) APIIssuers(ctx context.Context, audit ManagementAudit) ([]APIIssuerView, error) {
	result, err := managementCall[struct{}, struct {
		Data []APIIssuerView `json:"data"`
	}](ctx, c, http.MethodGet, "/internal/v1/api-issuers", audit, struct{}{})
	return result.Data, err
}
func (c *RemoteManagementClient) MutateAPIIssuer(ctx context.Context, audit ManagementAudit, path, method string, input any) (APIIssuerView, error) {
	return managementCall[any, APIIssuerView](ctx, c, method, path, audit, input)
}
func (h Handler) ListAPIIssuers(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	client, ok := h.ssoManagement.(APIIssuerManagementClient)
	if !ok {
		writeSSOFailure(w, nil)
		return
	}
	views, err := client.APIIssuers(r.Context(), managementAudit(req))
	if err != nil {
		writeSSOFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": views, "key_session": ssoKeySession(req)})
}
func (h Handler) MutateAPIIssuer(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !ssoKeySession(req) {
		writeError(w, 403, "key_session_required", "Sign in with an administrator virtual key to manage API issuer trust.")
		return
	}
	client, ok := h.ssoManagement.(APIIssuerManagementClient)
	if !ok {
		writeSSOFailure(w, nil)
		return
	}
	id := r.PathValue("id")
	path := "/internal/v1/api-issuers"
	action := "create"
	var input any
	if id == "" {
		input = &apiIssuerCreate{}
	} else {
		if !validSSOConnectionID(id) || id == "default" {
			writeError(w, 400, "invalid_api_issuer", "Invalid API issuer ID.")
			return
		}
		path += "/" + url.PathEscape(id)
		if r.Method == http.MethodPut {
			action = "draft.save"
			input = &apiIssuerDraft{}
		} else if strings.HasSuffix(r.URL.Path, "/test") {
			action = "test"
			path += "/test"
			input = &apiIssuerTest{}
		} else {
			path += "/action"
			input = &SSOActionInput{}
		}
	}
	if !decodeDirectoryJSON(w, r, input) {
		return
	}
	if test, ok := input.(*apiIssuerTest); ok && (len(test.Token) == 0 || len(test.Token) > 32768) {
		writeError(w, 400, "invalid_api_token", "A bounded resource access token is required.")
		return
	}
	if change, ok := input.(*SSOActionInput); ok {
		if change.Action != "activate" && change.Action != "disable" && change.Action != "rollback" {
			writeError(w, 400, "invalid_api_action", "Invalid API issuer action.")
			return
		}
		action = change.Action
	}
	if create, ok := input.(*apiIssuerCreate); ok {
		id = create.ID
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "api-issuer." + action, TargetType: "api-issuer", TargetID: id}
	if h.audit == nil || !h.auditMutation(r.Context(), audit, event) {
		writeError(w, 503, "audit_unavailable", "Audit service is unavailable.")
		return
	}
	view, err := client.MutateAPIIssuer(r.Context(), audit, path, r.Method, input)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeSSOFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, 200, view)
}
