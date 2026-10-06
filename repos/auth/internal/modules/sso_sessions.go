package modules

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const ssoSessionPrefix = "agsso_"

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

type storedSSOSession struct {
	Epoch          string      `json:"epoch"`
	ProfileID      string      `json:"profile_id"`
	Identity       JWTIdentity `json:"identity"`
	UserID         string      `json:"user_id"`
	OrganizationID string      `json:"organization_id,omitempty"`
	Roles          []string    `json:"roles"`
	ExpiresAt      int64       `json:"expires_at"`
}

type ssoSessionStore interface {
	CreateSSOSession(context.Context, string, string, string, string, int64, []byte) error
	LoadSSOSession(context.Context, string, int64) ([]byte, error)
	RevokeSSOSession(context.Context, string) error
}

// Verify browser tokens separately from directory approval so an administrator
// can inspect bounded verified identity metadata without granting access.
func (m AuthModule) verifySSOIdentity(ctx context.Context, profile *SSOProfile, login SSOBrowserLogin) (RequestContext, error) {
	claims, err := m.verifySSOClaims(ctx, profile, login)
	if err != nil {
		return RequestContext{}, err
	}
	return m.authorizeSSOClaims(ctx, profile, claims)
}

// Verify the browser client's ID token independently of API resource trust.
func (m AuthModule) verifySSOClaims(ctx context.Context, profile *SSOProfile, login SSOBrowserLogin) (jwtClaims, error) {
	if profile == nil || profile.ID != login.ProfileID || len(login.Nonce) != 43 {
		return jwtClaims{}, ErrUnauthorized
	}
	cfg := profile.browserJWTConfig()
	cfg.Audience = profile.ClientID
	verifier, err := newJWTVerifier(cfg)
	if err != nil {
		return jwtClaims{}, ErrSSOConfiguration
	}
	claims, err := verifier.Verify(ctx, login.Token)
	if err != nil {
		if errors.Is(err, ErrJWTUnavailable) {
			return jwtClaims{}, ErrSSOUnavailable
		}
		return jwtClaims{}, ErrUnauthorized
	}
	subject, ok := claims.Raw["sub"].(string)
	if !ok || !validJWTIdentityValue(subject, 256) || subject != claims.Subject {
		return jwtClaims{}, ErrUnauthorized
	}
	nonce, validNonce := claims.Raw["nonce"].(string)
	if !validNonce || subtle.ConstantTimeCompare([]byte(nonce), []byte(login.Nonce)) != 1 {
		return jwtClaims{}, ErrUnauthorized
	}
	issued, ok := claims.Raw["iat"].(float64)
	now := m.sso.now().Unix()
	if !ok || issued != float64(int64(issued)) || issued < float64(now-300) || issued > float64(now+30) {
		return jwtClaims{}, ErrUnauthorized
	}
	azp := ""
	if raw, exists := claims.Raw["azp"]; exists {
		value, valid := raw.(string)
		if !valid || !validJWTIdentityValue(value, 512) {
			return jwtClaims{}, ErrUnauthorized
		}
		azp = value
	}
	audiences, _ := claims.Audience.([]any)
	if azp != "" && azp != profile.ClientID || len(audiences) > 1 && azp != profile.ClientID {
		return jwtClaims{}, ErrUnauthorized
	}
	if raw, exists := claims.Raw["at_hash"]; exists {
		hash, valid := raw.(string)
		if !valid || hash == "" {
			return jwtClaims{}, ErrUnauthorized
		}
		digest := sha256.Sum256([]byte(login.AccessToken))
		expected := base64.RawURLEncoding.EncodeToString(digest[:16])
		if login.AccessToken == "" || subtle.ConstantTimeCompare([]byte(hash), []byte(expected)) != 1 {
			return jwtClaims{}, ErrUnauthorized
		}
	}
	return claims, nil
}
func (m AuthModule) authorizeSSOClaims(ctx context.Context, profile *SSOProfile, claims jwtClaims) (RequestContext, error) {
	req := RequestContext{}
	cfg := profile.browserJWTConfig()
	cfg.Audience = profile.ClientID
	mapped, err := profile.mappedBrowserRoles(claims.Raw)
	if err != nil {
		return req, err
	}
	cfg.RolesClaim = "gateway_verified_browser_roles"
	cfg.RoleMappings = map[string]string{}
	for _, value := range mapped {
		role := value.(string)
		cfg.RoleMappings[role] = role
	}
	// This internal claim is overwritten after signature/nonce/audience checks.
	claims.Raw[cfg.RolesClaim] = mapped
	candidate := m
	candidate.jwtConfig, candidate.jwtVerifier, candidate.sso = cfg, nil, nil
	candidate.apiIssuers = nil
	if err := candidate.authorizeJWTPrincipal(ctx, &req, claims); err != nil {
		return req, err
	}
	if profile.OrganizationID != "" && req.OrganizationID != profile.OrganizationID {
		return req, ErrUnauthorized
	}
	return req, nil
}

func ssoSessionHash(token string) (string, bool) {
	if !strings.HasPrefix(token, ssoSessionPrefix) || len(token) != len(ssoSessionPrefix)+43 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, ssoSessionPrefix))
	if err != nil || len(raw) != 32 {
		return "", false
	}
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:]), true
}

func (m AuthModule) CreateSSOBrowserSession(ctx context.Context, login SSOBrowserLogin) (SSOBrowserSession, error) {
	if m.sso == nil || m.sso.aead == nil {
		return SSOBrowserSession{}, ErrSSOUnavailable
	}
	profile, err := m.sso.activeProfile(ctx, login.ProfileID)
	if err != nil {
		return SSOBrowserSession{}, err
	}
	if profile == nil || !profile.Enabled {
		return SSOBrowserSession{}, ErrUnauthorized
	}
	req, err := m.verifySSOIdentity(ctx, profile, login)
	if err != nil {
		return SSOBrowserSession{}, err
	}
	store := m.sso.sessionStore
	if store == nil {
		return SSOBrowserSession{}, ErrSSOUnavailable
	}
	random, err := ssoRandom()
	if err != nil {
		return SSOBrowserSession{}, err
	}
	token := ssoSessionPrefix + random
	hash, _ := ssoSessionHash(token)
	epoch := sha256.Sum256([]byte(profile.SessionKey))
	session := storedSSOSession{Epoch: hex.EncodeToString(epoch[:]), ProfileID: profile.ID, Identity: *req.JWTIdentity, UserID: req.UserID, OrganizationID: req.OrganizationID, Roles: req.Roles, ExpiresAt: m.sso.now().Add(time.Duration(profile.SessionTTLSeconds) * time.Second).Unix()}
	plain, err := json.Marshal(session)
	if err != nil {
		return SSOBrowserSession{}, ErrSSOUnavailable
	}
	nonce := make([]byte, m.sso.sessionAEAD.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return SSOBrowserSession{}, ErrSSOUnavailable
	}
	payload := m.sso.sessionAEAD.Seal(nonce, nonce, plain, []byte("sso-session/v1\x00"+hash))
	loginDigest := sha256.Sum256([]byte(profile.Issuer + "\x00" + profile.ClientID + "\x00" + login.Nonce))
	if err := store.CreateSSOSession(ctx, hash, hex.EncodeToString(loginDigest[:]), session.ProfileID, session.UserID, session.ExpiresAt, payload); err != nil {
		return SSOBrowserSession{}, err
	}
	return SSOBrowserSession{Token: token, ExpiresAt: session.ExpiresAt}, nil
}

func (m AuthModule) authorizeSSOBrowserSession(ctx context.Context, req *RequestContext) error {
	hash, ok := ssoSessionHash(req.APIKey)
	if !ok {
		return ErrUnauthorized
	}
	return m.authorizeSSOSessionHash(ctx, req, hash)
}

func (m AuthModule) authorizeSSOSessionHash(ctx context.Context, req *RequestContext, hash string) error {
	if m.sso == nil || m.sso.aead == nil {
		return ErrSSOUnavailable
	}
	store := m.sso.sessionStore
	if store == nil {
		return ErrSSOUnavailable
	}
	payload, err := store.LoadSSOSession(ctx, hash, m.sso.now().Unix())
	if err != nil {
		return err
	}
	if len(payload) < m.sso.sessionAEAD.NonceSize() || len(payload) > 8192 {
		return ErrUnauthorized
	}
	plain, err := m.sso.sessionAEAD.Open(nil, payload[:m.sso.sessionAEAD.NonceSize()], payload[m.sso.sessionAEAD.NonceSize():], []byte("sso-session/v1\x00"+hash))
	if err != nil {
		return ErrUnauthorized
	}
	var session storedSSOSession
	if json.Unmarshal(plain, &session) != nil || session.ExpiresAt <= m.sso.now().Unix() {
		return ErrUnauthorized
	}
	profile, err := m.sso.activeProfile(ctx, session.ProfileID)
	if err != nil {
		return err
	}
	if profile == nil || !profile.Enabled || profile.ID != session.ProfileID || profile.Issuer != session.Identity.Issuer || profile.ClientID != session.Identity.Audience {
		return ErrUnauthorized
	}
	epoch := sha256.Sum256([]byte(profile.SessionKey))
	if session.Epoch != hex.EncodeToString(epoch[:]) {
		return ErrUnauthorized
	}
	directory, ok := m.store.(jwtPrincipalStore)
	if !ok {
		return ErrJWTDirectoryUnavailable
	}
	principal, found, err := directory.LookupJWTPrincipal(ctx, session.Identity.Issuer, session.Identity.Subject, session.Identity.Audience)
	if err != nil {
		return ErrJWTDirectoryUnavailable
	}
	if !found || !principal.Enabled || principal.UserID != session.UserID || principal.OrganizationID != session.OrganizationID || profile.OrganizationID != "" && principal.OrganizationID != profile.OrganizationID || principal.Issuer != session.Identity.Issuer || principal.Subject != session.Identity.Subject || principal.Audience != session.Identity.Audience {
		return ErrUnauthorized
	}
	roles := []string{}
	for _, role := range session.Roles {
		if profile.allowsBrowserRole(role) && principalRoleApproved(principal, role) {
			roles = append(roles, role)
		}
	}
	if len(roles) == 0 {
		return ErrUnauthorized
	}
	if err := applyJWTPrincipal(req, principal, roles); err != nil {
		return err
	}
	req.JWTIdentity.ConnectionID, req.JWTIdentity.SessionHash = profile.ID, hash
	req.Metadata["auth.method"] = "browser_sso"
	req.APIKey = ""
	return nil
}

func (m AuthModule) RevokeSSOBrowserSession(ctx context.Context, token string) error {
	hash, ok := ssoSessionHash(token)
	if !ok {
		return ErrUnauthorized
	}
	if m.sso == nil {
		return ErrSSOUnavailable
	}
	store := m.sso.sessionStore
	if store == nil {
		return ErrSSOUnavailable
	}
	if err := store.RevokeSSOSession(ctx, hash); err != nil {
		return errors.Join(ErrSSOUnavailable, err)
	}
	return nil
}
