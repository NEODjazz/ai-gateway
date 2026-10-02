package modules

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	ErrSSOConfiguration = errors.New("invalid SSO configuration or verification")
	ErrSSOConflict      = errors.New("SSO settings revision conflict")
	ErrSSOUnavailable   = errors.New("managed SSO settings unavailable")
)

// SSOProfileConfig contains public configuration, never client/session secrets.
type SSOProfileConfig struct {
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
	SessionTTLSeconds int               `json:"session_ttl_seconds"`
}

// SSOProfile only crosses the authenticated service channel. It is encrypted
// before persistence and must not be returned by the public management API.
type SSOProfile struct {
	SSOProfileConfig
	ID           string `json:"id"`
	Enabled      bool   `json:"enabled"`
	ClientSecret string `json:"client_secret,omitempty"`
	SessionKey   string `json:"session_key"`
}

type SSOProfileView struct {
	SSOProfileConfig
	ID                     string `json:"id"`
	Enabled                bool   `json:"enabled"`
	ClientSecretConfigured bool   `json:"client_secret_configured"`
}

type SSODraftInput struct {
	SSOProfileConfig
	ExpectedRevision int64   `json:"expected_revision"`
	ClientSecret     *string `json:"client_secret,omitempty"`
}

type SSOSettingsView struct {
	Revision      int64           `json:"revision"`
	Active        *SSOProfileView `json:"active"`
	Draft         *SSOProfileView `json:"draft"`
	CanRollback   bool            `json:"can_rollback"`
	TestStatus    string          `json:"test_status"`
	TestExpiresAt int64           `json:"test_expires_at,omitempty"`
}

type SSOAttempt struct {
	TicketHash   string       `json:"ticket_hash"`
	ActorID      string       `json:"actor_id"`
	ExpiresAt    int64        `json:"expires_at"`
	Status       string       `json:"status"`
	UserID       string       `json:"user_id,omitempty"`
	CredentialID string       `json:"credential_id,omitempty"`
	Roles        []string     `json:"roles,omitempty"`
	Identity     *JWTIdentity `json:"identity,omitempty"`
}

type SSOSettingsState struct {
	SchemaVersion int `json:"schema_version"`
	// Existing managed API trust is captured once when upgrading an old document.
	// Browser lifecycle operations never modify this independent configuration.
	APITrustSeparated bool              `json:"api_trust_separated"`
	APITrust          *SSOProfileConfig `json:"api_trust,omitempty"`
	Active            *SSOProfile       `json:"active,omitempty"`
	Previous          *SSOProfile       `json:"previous,omitempty"`
	Draft             *SSOProfile       `json:"draft,omitempty"`
	CanRollback       bool              `json:"can_rollback"`
	Attempt           *SSOAttempt       `json:"attempt,omitempty"`
}

type SSOSettingsStore interface {
	LoadSSOSettings(context.Context) (int64, []byte, error)
	SaveSSOSettings(context.Context, int64, []byte) error
}

type SSOManager struct {
	store      SSOSettingsStore
	aead       cipher.AEAD
	legacyAEAD cipher.AEAD
	now        func() time.Time
	mu         sync.Mutex
	// Only the active verifier is retained. Candidate verifiers are request local.
	profileID string
	verifier  *jwtVerifier
}

func NewSSOManager(store SSOSettingsStore, key string) (*SSOManager, error) {
	if len(key) < 32 {
		return nil, ErrSSOUnavailable
	}
	return newRuntimeSSOManager(store, key, "")
}

// Legacy hash-key encryption is read only. Rewrap existing settings using CAS
// when the shared configuration key becomes available; never change identities
// or session keys and never overwrite a concurrent administrator's edit.
func newRuntimeSSOManager(store SSOSettingsStore, key, legacyKey string) (*SSOManager, error) {
	if store == nil || key != "" && len(key) < 32 {
		return nil, ErrSSOUnavailable
	}
	manager := &SSOManager{store: store, now: time.Now}
	var err error
	if key != "" {
		manager.aead, err = ssoSettingsCipher(key)
		if err != nil {
			return nil, err
		}
	}
	if len(legacyKey) >= 32 && legacyKey != key {
		manager.legacyAEAD, err = ssoSettingsCipher(legacyKey)
		if err != nil {
			return nil, err
		}
	}
	return manager, nil
}

func ssoSettingsCipher(key string) (cipher.AEAD, error) {
	digest := sha256.Sum256([]byte("ai-gateway/sso-settings/v1\x00" + key))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func decryptSSOSettings(aead cipher.AEAD, payload []byte) ([]byte, error) {
	if aead == nil || len(payload) < aead.NonceSize() {
		return nil, ErrSSOUnavailable
	}
	return aead.Open(nil, payload[:aead.NonceSize()], payload[aead.NonceSize():], []byte("sso-settings/v1"))
}

func (m *SSOManager) Load(ctx context.Context) (SSOSettingsState, int64, error) {
	if m == nil {
		return SSOSettingsState{}, 0, ErrSSOUnavailable
	}
	for attempt := 0; attempt < 3; attempt++ {
		revision, payload, err := m.store.LoadSSOSettings(ctx)
		if err != nil {
			return SSOSettingsState{}, 0, ErrSSOUnavailable
		}
		if revision == 0 && len(payload) == 0 {
			return SSOSettingsState{SchemaVersion: 1, APITrustSeparated: true}, 0, nil
		}
		if revision < 1 || len(payload) > 64<<10 {
			return SSOSettingsState{}, 0, ErrSSOUnavailable
		}
		plain, err := decryptSSOSettings(m.aead, payload)
		legacy := err != nil
		if legacy {
			plain, err = decryptSSOSettings(m.legacyAEAD, payload)
		}
		if err != nil {
			return SSOSettingsState{}, 0, ErrSSOUnavailable
		}
		var state SSOSettingsState
		if json.Unmarshal(plain, &state) != nil || state.SchemaVersion != 1 {
			return SSOSettingsState{}, 0, ErrSSOUnavailable
		}
		migrateTrust := !state.APITrustSeparated
		if migrateTrust {
			if state.Active != nil {
				config := state.Active.SSOProfileConfig
				state.APITrust = &config
			}
			state.APITrustSeparated = true
		}
		if (legacy || migrateTrust) && m.aead != nil {
			if err := m.save(ctx, revision, state); errors.Is(err, ErrSSOConflict) {
				continue
			} else if err != nil {
				return SSOSettingsState{}, 0, ErrSSOUnavailable
			}
			revision++
		}
		return state, revision, nil
	}
	return SSOSettingsState{}, 0, ErrSSOUnavailable
}

func (m *SSOManager) save(ctx context.Context, revision int64, state SSOSettingsState) error {
	if m == nil || m.aead == nil {
		return ErrSSOUnavailable
	}
	if revision < 0 || revision == math.MaxInt64 {
		return ErrSSOConfiguration
	}
	plain, err := json.Marshal(state)
	if err != nil || len(plain) > 60<<10 {
		return ErrSSOConfiguration
	}
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ErrSSOUnavailable
	}
	payload := m.aead.Seal(nonce, nonce, plain, []byte("sso-settings/v1"))
	return m.store.SaveSSOSettings(ctx, revision, payload)
}

func publicSSOProfile(profile *SSOProfile) *SSOProfileView {
	if profile == nil {
		return nil
	}
	return &SSOProfileView{SSOProfileConfig: profile.SSOProfileConfig, ID: profile.ID, Enabled: profile.Enabled, ClientSecretConfigured: profile.ClientSecret != ""}
}

func (m *SSOManager) View(ctx context.Context) (SSOSettingsView, error) {
	if m == nil || m.aead == nil {
		return SSOSettingsView{}, ErrSSOUnavailable
	}
	state, revision, err := m.Load(ctx)
	if err != nil {
		return SSOSettingsView{}, err
	}
	view := SSOSettingsView{Revision: revision, Active: publicSSOProfile(state.Active), Draft: publicSSOProfile(state.Draft), CanRollback: state.CanRollback, TestStatus: "not_started"}
	if state.Attempt != nil {
		view.TestStatus, view.TestExpiresAt = state.Attempt.Status, state.Attempt.ExpiresAt
		if state.Attempt.ExpiresAt <= m.now().Unix() {
			view.TestStatus = "expired"
		}
	}
	return view, nil
}

func ssoEndpoint(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Fragment != "" || u.RawQuery != "" || !validJWTIdentityValue(raw, 2048) {
		return nil, false
	}
	loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
	return u, u.Scheme == "https" || u.Scheme == "http" && loopback
}

func (p SSOProfileConfig) Validate() error {
	issuer, ok := ssoEndpoint(p.Issuer)
	if !ok || !validJWTIdentityValue(p.Audience, 256) || !validJWTIdentityValue(p.ClientID, 512) {
		return ErrSSOConfiguration
	}
	if !validSSOEndpointOrigins(p.EndpointOrigins) {
		return ErrSSOConfiguration
	}
	for _, endpoint := range []string{p.JWKSURL, p.AuthorizationURL, p.TokenURL} {
		u, valid := ssoEndpoint(endpoint)
		if !valid || !trustedSSOEndpoint(issuer, u, p.EndpointOrigins) {
			return ErrSSOConfiguration
		}
	}
	redirect, valid := ssoEndpoint(p.RedirectURL)
	if !valid {
		redirect, _ = url.Parse(p.RedirectURL)
		valid = redirect != nil && redirect.Scheme == "http" && strings.HasSuffix(redirect.Hostname(), ".localhost") && redirect.User == nil && redirect.Fragment == "" && redirect.RawQuery == "" && validJWTIdentityValue(p.RedirectURL, 2048)
	}
	if !valid || redirect.Path != "/auth/sso/callback" || p.SessionTTLSeconds < 60 || p.SessionTTLSeconds > 86400 || len(p.Scopes) > 16 || !slices.Contains(p.Scopes, "openid") {
		return ErrSSOConfiguration
	}
	for _, scope := range p.Scopes {
		if !validJWTIdentityValue(scope, 128) || strings.ContainsAny(scope, " \t\r\n") {
			return ErrSSOConfiguration
		}
	}
	if !validJWTIdentityValue(p.RolesClaim, 256) || len(p.RoleMappings) == 0 {
		return ErrSSOConfiguration
	}
	return p.jwtConfig().validate()
}

func validSSOEndpointOrigins(origins []string) bool {
	if len(origins) > 8 {
		return false
	}
	for _, raw := range origins {
		u, ok := ssoEndpoint(raw)
		if !ok || u.Path != "" && u.Path != "/" {
			return false
		}
	}
	return true
}

func trustedSSOEndpoint(issuer, endpoint *url.URL, origins []string) bool {
	if issuer.Scheme == endpoint.Scheme && strings.EqualFold(issuer.Host, endpoint.Host) {
		return true
	}
	for _, raw := range origins {
		u, ok := ssoEndpoint(raw)
		if ok && u.Scheme == endpoint.Scheme && strings.EqualFold(u.Host, endpoint.Host) {
			return true
		}
	}
	return false
}

func (p SSOProfileConfig) jwtConfig() JWTAuthConfig {
	return JWTAuthConfig{Issuer: p.Issuer, Audience: p.Audience, JWKSURL: p.JWKSURL, JWKSCacheTTL: 5 * time.Minute, ClockSkew: 30 * time.Second, UserIDClaim: "sub", RolesClaim: p.RolesClaim, IdentityMode: "directory", RoleMappings: p.RoleMappings}
}

func ssoRandom() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", ErrSSOUnavailable
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (m *SSOManager) SaveDraft(ctx context.Context, input SSODraftInput) (SSOSettingsView, error) {
	if err := input.SSOProfileConfig.Validate(); err != nil {
		return SSOSettingsView{}, err
	}
	if input.Audience != input.ClientID {
		return SSOSettingsView{}, ErrSSOConfiguration
	}
	state, revision, err := m.Load(ctx)
	if err != nil {
		return SSOSettingsView{}, err
	}
	if input.ExpectedRevision != revision {
		return SSOSettingsView{}, ErrSSOConflict
	}
	secret := ""
	var previous *SSOProfile
	if state.Draft != nil {
		previous = state.Draft
	} else if state.Active != nil {
		previous = state.Active
	}
	if previous != nil {
		secret = previous.ClientSecret
		if secret != "" && input.ClientSecret == nil && (previous.Issuer != input.Issuer || previous.ClientID != input.ClientID) {
			// A write-only secret must never be silently transferred to another IdP/client.
			return SSOSettingsView{}, ErrSSOConfiguration
		}
	}
	if input.ClientSecret != nil {
		secret = *input.ClientSecret
	}
	if len(secret) > 4096 {
		return SSOSettingsView{}, ErrSSOConfiguration
	}
	id, err := ssoRandom()
	if err != nil {
		return SSOSettingsView{}, err
	}
	key, err := ssoRandom()
	if err != nil {
		return SSOSettingsView{}, err
	}
	state.Draft = &SSOProfile{SSOProfileConfig: input.SSOProfileConfig, ID: id, Enabled: true, ClientSecret: secret, SessionKey: key}
	state.Attempt = nil
	if err := m.save(ctx, revision, state); err != nil {
		return SSOSettingsView{}, err
	}
	return m.View(ctx)
}

func (m *SSOManager) StartTest(ctx context.Context, expectedRevision int64, actor string) (string, error) {
	state, revision, err := m.Load(ctx)
	if err != nil {
		return "", err
	}
	if revision != expectedRevision {
		return "", ErrSSOConflict
	}
	if state.Draft == nil || !validJWTIdentityValue(actor, 256) {
		return "", ErrSSOConfiguration
	}
	ticket, err := ssoRandom()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(ticket))
	state.Attempt = &SSOAttempt{TicketHash: hex.EncodeToString(digest[:]), ActorID: actor, ExpiresAt: m.now().Add(5 * time.Minute).Unix(), Status: "running"}
	return ticket, m.save(ctx, revision, state)
}

func validSSOTicket(attempt *SSOAttempt, ticket string, now time.Time) bool {
	if attempt == nil || len(ticket) != 43 || attempt.ExpiresAt <= now.Unix() || attempt.Status != "running" {
		return false
	}
	digest := sha256.Sum256([]byte(ticket))
	return subtle.ConstantTimeCompare([]byte(attempt.TicketHash), []byte(hex.EncodeToString(digest[:]))) == 1
}

func (m *SSOManager) JWTModule(ctx context.Context, base AuthModule) (AuthModule, error) {
	state, _, err := m.Load(ctx)
	if err != nil {
		return base, ErrJWTUnavailable
	}
	if state.APITrust == nil {
		base.sso = nil
		return base, nil
	}
	if state.APITrust.Validate() != nil {
		return base, ErrJWTUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	payload, err := json.Marshal(state.APITrust)
	if err != nil {
		return base, ErrJWTUnavailable
	}
	digest := sha256.Sum256(payload)
	trustID := hex.EncodeToString(digest[:])
	if m.profileID != trustID || m.verifier == nil {
		verifier, err := newJWTVerifier(state.APITrust.jwtConfig())
		if err != nil {
			return base, ErrJWTUnavailable
		}
		m.profileID, m.verifier = trustID, verifier
	}
	base.jwtConfig, base.jwtVerifier, base.sso = state.APITrust.jwtConfig(), m.verifier, nil
	return base, nil
}
