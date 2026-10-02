package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type SSOProfileConfig struct {
	OrganizationID    string            `json:"organization_id,omitempty"`
	EndpointOrigins   []string          `json:"endpoint_origins,omitempty"`
	Issuer            string            `json:"issuer"`
	Audience          string            `json:"audience"`
	JWKSURL           string            `json:"jwks_url"`
	AuthorizationURL  string            `json:"authorization_url"`
	TokenURL          string            `json:"token_url"`
	ClientID          string            `json:"client_id"`
	RedirectURL       string            `json:"redirect_url"`
	Scopes            []string          `json:"scopes"`
	RolesClaim        string            `json:"roles_claim"`
	RoleMappings      map[string]string `json:"role_mappings"`
	GroupsClaim       string            `json:"groups_claim,omitempty"`
	GroupMappings     map[string]string `json:"group_mappings,omitempty"`
	SessionTTLSeconds int               `json:"session_ttl_seconds"`
}
type SSOProfileView struct {
	SSOProfileConfig
	ID                     string `json:"id"`
	Enabled                bool   `json:"enabled"`
	ClientSecretConfigured bool   `json:"client_secret_configured"`
}
type SSOSettingsView struct {
	LastTestAt       int64                `json:"last_test_at,omitempty"`
	LastTestStatus   string               `json:"last_test_status,omitempty"`
	VerifiedIdentity *SSOVerifiedIdentity `json:"verified_identity,omitempty"`
	Revision         int64                `json:"revision"`
	Active           *SSOProfileView      `json:"active"`
	Draft            *SSOProfileView      `json:"draft"`
	CanRollback      bool                 `json:"can_rollback"`
	TestStatus       string               `json:"test_status"`
	TestExpiresAt    int64                `json:"test_expires_at,omitempty"`
	KeySession       bool                 `json:"key_session"`
}
type SSODraftInput struct {
	SSOProfileConfig
	ExpectedRevision int64   `json:"expected_revision"`
	ClientSecret     *string `json:"client_secret,omitempty"`
}
type SSORevisionInput struct {
	ExpectedRevision int64 `json:"expected_revision"`
}
type SSOActionInput struct {
	SSORevisionInput
	Action string `json:"action"`
}
type SSODiscovery struct {
	Issuer           string `json:"issuer"`
	AuthorizationURL string `json:"authorization_url"`
	TokenURL         string `json:"token_url"`
	JWKSURL          string `json:"jwks_url"`
}

// PrivateSSOProfile is never returned through the public admin API.
type PrivateSSOProfile struct {
	SSOProfileConfig
	ID           string `json:"id"`
	Enabled      bool   `json:"enabled"`
	ClientSecret string `json:"client_secret"`
	SessionKey   string `json:"session_key"`
}
type PrivateSSOTest struct {
	Profile *PrivateSSOProfile `json:"profile"`
	Attempt *struct {
		TicketHash string `json:"ticket_hash"`
		ExpiresAt  int64  `json:"expires_at"`
		Status     string `json:"status"`
	} `json:"attempt"`
}
type SSOTestVerification struct {
	ProfileID   string `json:"profile_id"`
	Ticket      string `json:"ticket"`
	Token       string `json:"token"`
	Nonce       string `json:"nonce"`
	AccessToken string `json:"access_token,omitempty"`
}
type SSOBrowserLogin struct {
	ProfileID   string `json:"profile_id"`
	Token       string `json:"token"`
	Nonce       string `json:"nonce"`
	AccessToken string `json:"access_token,omitempty"`
}
type SSOBrowserSession struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}
type SSOSessionManagementClient interface {
	CreateSSOBrowserSession(context.Context, ManagementAudit, SSOBrowserLogin) (SSOBrowserSession, error)
	RevokeSSOBrowserSession(context.Context, ManagementAudit, string) error
}

func (c *RemoteManagementClient) CreateSSOBrowserSession(ctx context.Context, audit ManagementAudit, input SSOBrowserLogin) (SSOBrowserSession, error) {
	return managementCall[SSOBrowserLogin, SSOBrowserSession](ctx, c, http.MethodPost, "/internal/v1/sso/sessions", audit, input)
}
func (c *RemoteManagementClient) RevokeSSOBrowserSession(ctx context.Context, audit ManagementAudit, token string) error {
	_, err := managementCall[map[string]string, struct {
		Revoked bool `json:"revoked"`
	}](ctx, c, http.MethodPost, "/internal/v1/sso/sessions/revoke", audit, map[string]string{"token": token})
	return err
}

type SSOManagementClient interface {
	GetSSOSettings(context.Context, ManagementAudit) (SSOSettingsView, error)
	SaveSSODraft(context.Context, ManagementAudit, SSODraftInput) (SSOSettingsView, error)
	DiscoverSSO(context.Context, ManagementAudit, string) (SSODiscovery, error)
	StartSSOTest(context.Context, ManagementAudit, SSORevisionInput) (string, error)
	ChangeSSO(context.Context, ManagementAudit, SSOActionInput) (SSOSettingsView, error)
	ActiveSSO(context.Context, ManagementAudit) (*PrivateSSOProfile, error)
	TestSSOProfile(context.Context, ManagementAudit) (PrivateSSOTest, error)
	VerifySSOTest(context.Context, ManagementAudit, SSOTestVerification) error
}

func (c *RemoteManagementClient) GetSSOSettings(ctx context.Context, audit ManagementAudit) (SSOSettingsView, error) {
	return managementCall[struct{}, SSOSettingsView](ctx, c, http.MethodGet, c.ssoPath("/internal/v1/sso/settings"), audit, struct{}{})
}
func (c *RemoteManagementClient) SaveSSODraft(ctx context.Context, audit ManagementAudit, input SSODraftInput) (SSOSettingsView, error) {
	return managementCall[SSODraftInput, SSOSettingsView](ctx, c, http.MethodPut, c.ssoPath("/internal/v1/sso/settings"), audit, input)
}
func (c *RemoteManagementClient) DiscoverSSO(ctx context.Context, audit ManagementAudit, issuer string) (SSODiscovery, error) {
	return managementCall[map[string]string, SSODiscovery](ctx, c, http.MethodPost, "/internal/v1/sso/discover", audit, map[string]string{"issuer": issuer})
}
func (c *RemoteManagementClient) StartSSOTest(ctx context.Context, audit ManagementAudit, input SSORevisionInput) (string, error) {
	out, err := managementCall[SSORevisionInput, struct {
		Ticket string `json:"ticket"`
	}](ctx, c, http.MethodPost, c.ssoPath("/internal/v1/sso/test"), audit, input)
	return out.Ticket, err
}
func (c *RemoteManagementClient) ChangeSSO(ctx context.Context, audit ManagementAudit, input SSOActionInput) (SSOSettingsView, error) {
	return managementCall[SSOActionInput, SSOSettingsView](ctx, c, http.MethodPost, c.ssoPath("/internal/v1/sso/action"), audit, input)
}
func (c *RemoteManagementClient) ActiveSSO(ctx context.Context, audit ManagementAudit) (*PrivateSSOProfile, error) {
	return managementCall[struct{}, *PrivateSSOProfile](ctx, c, http.MethodGet, c.ssoPath("/internal/v1/sso/active"), audit, struct{}{})
}
func (c *RemoteManagementClient) TestSSOProfile(ctx context.Context, audit ManagementAudit) (PrivateSSOTest, error) {
	return managementCall[struct{}, PrivateSSOTest](ctx, c, http.MethodGet, c.ssoPath("/internal/v1/sso/test-profile"), audit, struct{}{})
}
func (c *RemoteManagementClient) VerifySSOTest(ctx context.Context, audit ManagementAudit, input SSOTestVerification) error {
	_, err := managementCall[SSOTestVerification, struct {
		Verified bool `json:"verified"`
	}](ctx, c, http.MethodPost, c.ssoPath("/internal/v1/sso/verify-test"), audit, input)
	return err
}
func (h Handler) WithSSOManagement(client SSOManagementClient) Handler {
	h.ssoManagement = client
	return h
}

func ssoKeySession(req modules.RequestContext) bool {
	return req.JWTIdentity == nil && !strings.HasPrefix(req.Metadata["auth.method"], "jwt")
}
func writeSSOFailure(w http.ResponseWriter, err error) {
	var managed *ManagementError
	if errors.As(err, &managed) {
		switch managed.Status {
		case 400:
			writeError(w, 400, "invalid_sso_settings", "Invalid SSO configuration or test. Check endpoints, principal binding and admin role.")
			return
		case 409:
			writeError(w, 409, "revision_conflict", "SSO settings changed. Reload before retrying.")
			return
		case 403:
			writeError(w, 403, "sso_forbidden", "SSO verification requires the same provisioned administrator.")
			return
		}
	}
	writeError(w, 503, "sso_unavailable", "Managed SSO requires PostgreSQL migrations 014–017 and CREDENTIAL_ENCRYPTION_KEY of at least 32 bytes; check Auth availability.")
}
func (h Handler) GetSSOSettings(w http.ResponseWriter, r *http.Request) {
	h, err := h.withRequestSSOConnection(r)
	if err != nil {
		writeSSOFailure(w, err)
		return
	}
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.ssoManagement == nil {
		writeSSOFailure(w, nil)
		return
	}
	view, err := h.ssoManagement.GetSSOSettings(r.Context(), managementAudit(req))
	if err != nil {
		writeSSOFailure(w, err)
		return
	}
	view.KeySession = ssoKeySession(req)
	writeJSON(w, 200, view)
}
func (h Handler) SaveSSODraft(w http.ResponseWriter, r *http.Request) {
	h, err := h.withRequestSSOConnection(r)
	if err != nil {
		writeSSOFailure(w, err)
		return
	}
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	var input SSODraftInput
	if !decodeDirectoryJSON(w, r, &input) {
		return
	}
	h.ssoMutation(w, r, req, "draft.save", func(audit ManagementAudit) (any, error) {
		view, err := h.ssoManagement.SaveSSODraft(r.Context(), audit, input)
		view.KeySession = ssoKeySession(req)
		return view, err
	})
}
func (h Handler) DiscoverSSO(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	var input struct {
		Issuer          string   `json:"issuer"`
		EndpointOrigins []string `json:"endpoint_origins,omitempty"`
	}
	if !decodeDirectoryJSON(w, r, &input) {
		return
	}
	if h.ssoManagement == nil {
		writeSSOFailure(w, nil)
		return
	}
	var metadata SSODiscovery
	var err error
	if len(input.EndpointOrigins) > 0 {
		client, ok := h.ssoManagement.(interface {
			DiscoverSSOWithOrigins(context.Context, ManagementAudit, string, []string) (SSODiscovery, error)
		})
		if !ok {
			writeSSOFailure(w, nil)
			return
		}
		metadata, err = client.DiscoverSSOWithOrigins(r.Context(), managementAudit(req), input.Issuer, input.EndpointOrigins)
	} else {
		metadata, err = h.ssoManagement.DiscoverSSO(r.Context(), managementAudit(req), input.Issuer)
	}
	if err != nil {
		writeSSOFailure(w, err)
		return
	}
	writeJSON(w, 200, metadata)
}

func (c *RemoteManagementClient) DiscoverSSOWithOrigins(ctx context.Context, audit ManagementAudit, issuer string, origins []string) (SSODiscovery, error) {
	return managementCall[map[string]any, SSODiscovery](ctx, c, http.MethodPost, "/internal/v1/sso/discover", audit, map[string]any{"issuer": issuer, "endpoint_origins": origins})
}
func (h Handler) StartSSOTest(w http.ResponseWriter, r *http.Request) {
	h, err := h.withRequestSSOConnection(r)
	if err != nil {
		writeSSOFailure(w, err)
		return
	}
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	var input SSORevisionInput
	if !decodeDirectoryJSON(w, r, &input) {
		return
	}
	h.ssoMutation(w, r, req, "test.start", func(audit ManagementAudit) (any, error) {
		ticket, err := h.ssoManagement.StartSSOTest(r.Context(), audit, input)
		return map[string]string{"start_url": "/auth/sso/test/start?ticket=" + ticket + ssoConnectionQuery(r)}, err
	})
}
func (h Handler) ChangeSSO(w http.ResponseWriter, r *http.Request) {
	h, err := h.withRequestSSOConnection(r)
	if err != nil {
		writeSSOFailure(w, err)
		return
	}
	req, ok := h.authorizeDirectory(w, r, "__global__")
	if !ok {
		return
	}
	if !ssoKeySession(req) {
		writeError(w, 403, "key_session_required", "Sign in with an administrator virtual key to activate, disable or roll back SSO.")
		return
	}
	var input SSOActionInput
	if !decodeDirectoryJSON(w, r, &input) {
		return
	}
	if input.Action != "activate" && input.Action != "disable" && input.Action != "rollback" {
		writeError(w, 400, "invalid_request", "Invalid SSO action.")
		return
	}
	h.ssoMutation(w, r, req, input.Action, func(audit ManagementAudit) (any, error) {
		view, err := h.ssoManagement.ChangeSSO(r.Context(), audit, input)
		view.KeySession = true
		return view, err
	})
}
func (h Handler) ssoMutation(w http.ResponseWriter, r *http.Request, req modules.RequestContext, action string, mutate func(ManagementAudit) (any, error)) {
	w.Header().Set("Cache-Control", "no-store")
	if h.ssoManagement == nil {
		writeSSOFailure(w, nil)
		return
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "sso." + action, TargetType: "sso", TargetID: ssoAuditTarget(r)}
	if h.audit == nil || !h.auditMutation(r.Context(), audit, event) {
		writeError(w, 503, "audit_unavailable", "Audit service is unavailable.")
		return
	}
	view, err := mutate(audit)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeSSOFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, 200, view)
}
func ssoServiceAudit(r *http.Request) ManagementAudit {
	return ManagementAudit{RequestID: requestID(r), ActorID: "gateway-sso-service", CredentialID: "internal-sso", Roles: []string{"service"}}
}
func (p *PrivateSSOProfile) browser(test bool) (*BrowserSSO, error) {
	key, err := base64.RawURLEncoding.DecodeString(p.SessionKey)
	if err != nil {
		return nil, err
	}
	redirect := p.RedirectURL
	if test {
		redirect = strings.TrimSuffix(redirect, "/auth/sso/callback") + "/auth/sso/test/callback"
	}
	return NewBrowserSSO(BrowserSSOConfig{ProfileID: p.ID, AuthorizationURL: p.AuthorizationURL, TokenURL: p.TokenURL, ClientID: p.ClientID, ClientSecret: p.ClientSecret, RedirectURL: redirect, Scopes: p.Scopes, SessionKey: key, SessionTTL: time.Duration(p.SessionTTLSeconds) * time.Second})
}
func (h Handler) resolveBrowserSSO(r *http.Request) (*BrowserSSO, error) {
	id, err := requestSSOConnection(r)
	if err != nil {
		return nil, err
	}
	return h.resolveBrowserSSOConnection(r, id)
}
func (h Handler) resolveBrowserSSOConnection(r *http.Request, id string) (*BrowserSSO, error) {
	if h.ssoManagement == nil {
		return nil, nil
	}
	selected, err := h.withSSOConnection(id)
	if err != nil {
		return nil, err
	}
	profile, err := selected.ssoManagement.ActiveSSO(r.Context(), ssoServiceAudit(r))
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, nil
	}
	if !profile.Enabled {
		return nil, nil
	}
	if profile.ID == "" {
		return nil, errors.New("browser SSO profile identity is missing")
	}
	browser, err := profile.browser(false)
	if browser != nil && id != "" && id != "default" {
		browser.config.ConnectionID = id
	}
	return browser, err
}
func (h Handler) browserSSOMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			if _, err := r.Cookie(browserSSOSessionCookie); err == nil && !strings.HasPrefix(r.URL.Path, "/auth/sso/") {
				// The authentication connection comes from the sealed session,
				// independently of the management connection selected in the query.
				id, err := requestSSOCookieConnection(r, browserSSOSessionCookie)
				if err != nil {
					writeError(w, 503, "sso_unavailable", "Browser session verification is unavailable.")
					return
				}
				sso, err := h.resolveBrowserSSOConnection(r, id)
				if err != nil {
					writeError(w, 503, "sso_unavailable", "Browser session verification is unavailable.")
					return
				}
				if sso != nil {
					r = sso.authorizeRequest(r)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func ssoAuditTarget(r *http.Request) string {
	id, err := requestSSOConnection(r)
	if err != nil || id == "" || id == "default" {
		return "browser-sso"
	}
	return "browser-sso/" + id
}

type SSOVerifiedIdentity struct {
	Issuer         string   `json:"issuer"`
	Subject        string   `json:"subject"`
	Audience       string   `json:"audience"`
	UserID         string   `json:"user_id"`
	OrganizationID string   `json:"organization_id,omitempty"`
	Roles          []string `json:"roles"`
	VerifiedAt     int64    `json:"verified_at"`
	Approved       bool     `json:"approved"`
}
