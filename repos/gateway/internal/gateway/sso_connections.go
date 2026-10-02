package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type SSOConnection struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	OrganizationID string `json:"organization_id,omitempty"`
}
type SSOConnectionView struct {
	SSOConnection
	SSOSettingsView
}
type SSOConnectionClient interface {
	ListSSOConnections(context.Context, ManagementAudit) ([]SSOConnectionView, error)
	CreateSSOConnection(context.Context, ManagementAudit, SSOConnection) error
	ForSSOConnection(string) SSOManagementClient
}
type SSOLoginConnectionClient interface {
	LoginSSOConnections(context.Context, ManagementAudit) ([]SSOConnection, error)
}

func (c *RemoteManagementClient) LoginSSOConnections(ctx context.Context, audit ManagementAudit) ([]SSOConnection, error) {
	response, err := managementCall[struct{}, struct {
		Data []SSOConnection `json:"data"`
	}](ctx, c, http.MethodGet, "/internal/v1/sso/login-connections", audit, struct{}{})
	return response.Data, err
}

func (c *RemoteManagementClient) ForSSOConnection(id string) SSOManagementClient {
	copy := *c
	copy.ssoConnection = id
	return &copy
}
func (c *RemoteManagementClient) ssoPath(path string) string {
	if c.ssoConnection == "" || c.ssoConnection == "default" {
		return path
	}
	return path + "?connection=" + url.QueryEscape(c.ssoConnection)
}
func (c *RemoteManagementClient) ListSSOConnections(ctx context.Context, audit ManagementAudit) ([]SSOConnectionView, error) {
	response, err := managementCall[struct{}, struct {
		Data []SSOConnectionView `json:"data"`
	}](ctx, c, http.MethodGet, "/internal/v1/sso/connections", audit, struct{}{})
	return response.Data, err
}
func (c *RemoteManagementClient) CreateSSOConnection(ctx context.Context, audit ManagementAudit, connection SSOConnection) error {
	_, err := managementCall[SSOConnection, SSOConnection](ctx, c, http.MethodPost, "/internal/v1/sso/connections", audit, connection)
	return err
}

func validSSOConnectionID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func requestSSOConnection(r *http.Request) (string, error) {
	if strings.HasPrefix(r.URL.Path, "/admin/v1/sso/") || r.URL.Path == "/auth/sso/start" || r.URL.Path == "/auth/sso/test/start" {
		values, exists := r.URL.Query()["connection"]
		if !exists {
			return "", nil
		}
		if len(values) != 1 || !validSSOConnectionID(values[0]) {
			return "", errors.New("invalid SSO connection")
		}
		return values[0], nil
	}
	name := browserSSOSessionCookie
	if r.URL.Path == "/auth/sso/callback" {
		name = browserSSOStateCookie
	}
	if r.URL.Path == "/auth/sso/test/callback" {
		name = browserSSOTestStateCookie
	}
	cookie, err := r.Cookie(name)
	if err != nil {
		return "", nil
	}
	id, _, prefixed := strings.Cut(cookie.Value, ".")
	if !prefixed {
		return "", nil
	}
	if !validSSOConnectionID(id) {
		return "", errors.New("invalid SSO connection")
	}
	// This is only a routing hint. The selected connection must authenticate
	// the sealed cookie before any code exchange, identity or tenant is accepted.
	return id, nil
}

func (h Handler) withRequestSSOConnection(r *http.Request) (Handler, error) {
	id, err := requestSSOConnection(r)
	if err != nil {
		return h, err
	}
	if id == "" || id == "default" {
		return h, nil
	}
	client, ok := h.ssoManagement.(SSOConnectionClient)
	if !ok {
		return h, errors.New("SSO connections unavailable")
	}
	h.ssoManagement = client.ForSSOConnection(id)
	return h, nil
}

func (h Handler) ListSSOConnections(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	client, ok := h.ssoManagement.(SSOConnectionClient)
	if !ok {
		writeSSOFailure(w, nil)
		return
	}
	views, err := client.ListSSOConnections(r.Context(), managementAudit(req))
	if err != nil {
		writeSSOFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": views})
}
func (h Handler) CreateSSOConnection(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var input SSOConnection
	if !decodeDirectoryJSON(w, r, &input) {
		return
	}
	client, ok := h.ssoManagement.(SSOConnectionClient)
	if !ok {
		writeSSOFailure(w, nil)
		return
	}
	event := AuditEvent{Action: "sso.connection.create", TargetType: "sso", TargetID: input.ID}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, 503, "audit_unavailable", "Audit service is unavailable.")
		return
	}
	if err := client.CreateSSOConnection(r.Context(), audit, input); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeSSOFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, 201, input)
}

func ssoConnectionQuery(r *http.Request) string {
	id, err := requestSSOConnection(r)
	if err != nil || id == "" || id == "default" {
		return ""
	}
	return "&connection=" + url.QueryEscape(id)
}
