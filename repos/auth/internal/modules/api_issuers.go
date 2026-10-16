package modules

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

const maxAPIIssuers = 16

// APIIssuer pins an API identity namespace independently of browser connections.
// To move a principal to another namespace, create a separate issuer entry.
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
type APIIssuerInput struct {
	APIIssuer
	APIIssuerConfig
}
type APIIssuerDraftInput struct {
	APIIssuerConfig
	ExpectedRevision int64 `json:"expected_revision"`
}
type apiIssuerProof struct {
	Actor     string         `json:"actor"`
	ExpiresAt int64          `json:"expires_at"`
	Request   RequestContext `json:"request"`
}
type apiIssuerState struct {
	Active         *APIIssuerProfile `json:"active"`
	Draft          *APIIssuerProfile `json:"draft"`
	Previous       *APIIssuerProfile `json:"previous"`
	CanRollback    bool              `json:"can_rollback"`
	Proof          *apiIssuerProof   `json:"proof"`
	LastTestAt     int64             `json:"last_test_at,omitempty"`
	LastTestStatus string            `json:"last_test_status,omitempty"`
}
type apiIssuerRow struct {
	APIIssuer
	Revision int64
	Payload  []byte
}
type apiIssuerStore interface {
	ListAPIIssuers(context.Context) ([]apiIssuerRow, error)
	CreateAPIIssuer(context.Context, APIIssuer, []byte) error
	SaveAPIIssuer(context.Context, string, int64, []byte) error
}
type apiIssuerVerifier struct {
	profileID string
	verifier  *jwtVerifier
}
type APIIssuerManager struct {
	store     apiIssuerStore
	aead      cipher.AEAD
	now       func() time.Time
	mu        sync.Mutex
	verifiers map[string]apiIssuerVerifier
}

func newAPIIssuerManager(store apiIssuerStore, key string) (*APIIssuerManager, error) {
	if store == nil || len(key) < 32 {
		return nil, ErrSSOUnavailable
	}
	digest := sha256.Sum256([]byte("ai-gateway/api-issuers/v1\x00" + key))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &APIIssuerManager{store: store, aead: aead, now: time.Now, verifiers: map[string]apiIssuerVerifier{}}, nil
}
func validAPIIssuer(issuer APIIssuer) bool {
	if !validSSOConnection(SSOConnection{ID: issuer.ID, Name: issuer.Name, Provider: "oidc", OrganizationID: issuer.OrganizationID}) {
		return false
	}
	_, ok := ssoEndpoint(issuer.Issuer)
	return ok && validJWTIdentityValue(issuer.Audience, 256)
}
func (c APIIssuerConfig) jwtConfig(issuer APIIssuer) JWTAuthConfig {
	return JWTAuthConfig{Issuer: issuer.Issuer, Audience: issuer.Audience, JWKSURL: c.JWKSURL, UserIDClaim: "sub", RolesClaim: c.RolesClaim, IdentityMode: "directory", RoleMappings: c.RoleMappings, JWKSCacheTTL: 5 * time.Minute, ClockSkew: 30 * time.Second}
}
func (c APIIssuerConfig) validate(issuer APIIssuer) error {
	if !validAPIIssuer(issuer) || !validJWTIdentityValue(c.RolesClaim, 256) {
		return ErrSSOConfiguration
	}
	issuerURL, _ := ssoEndpoint(issuer.Issuer)
	jwks, ok := ssoEndpoint(c.JWKSURL)
	if !ok || issuerURL.Scheme != jwks.Scheme || issuerURL.Host != jwks.Host {
		return ErrSSOConfiguration
	}
	if issuer.OrganizationID != "" {
		for _, role := range c.RoleMappings {
			if role == "admin" || role == "team_admin" {
				return ErrSSOConfiguration
			}
		}
	}
	if c.jwtConfig(issuer).validate() != nil {
		return ErrSSOConfiguration
	}
	return nil
}
func apiIssuerAAD(issuer APIIssuer) []byte {
	binding, _ := json.Marshal(issuer)
	return append([]byte("api-issuer/v1\x00"), binding...)
}
func (m *APIIssuerManager) seal(issuer APIIssuer, state apiIssuerState) ([]byte, error) {
	raw, err := json.Marshal(state)
	if err != nil || len(raw) > 60000 {
		return nil, ErrSSOConfiguration
	}
	nonce := make([]byte, m.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, ErrSSOUnavailable
	}
	return m.aead.Seal(nonce, nonce, raw, apiIssuerAAD(issuer)), nil
}
func (m *APIIssuerManager) rows(ctx context.Context) ([]apiIssuerRow, []apiIssuerState, error) {
	if m == nil {
		return nil, nil, ErrSSOUnavailable
	}
	rows, err := m.store.ListAPIIssuers(ctx)
	if err != nil || len(rows) > maxAPIIssuers {
		return nil, nil, ErrSSOUnavailable
	}
	states := make([]apiIssuerState, len(rows))
	seen := map[string]bool{}
	for i, row := range rows {
		if !validAPIIssuer(row.APIIssuer) || seen[row.ID] || len(row.Payload) > 65536 || len(row.Payload) < m.aead.NonceSize() {
			return nil, nil, ErrSSOUnavailable
		}
		seen[row.ID] = true
		raw, err := m.aead.Open(nil, row.Payload[:m.aead.NonceSize()], row.Payload[m.aead.NonceSize():], apiIssuerAAD(row.APIIssuer))
		if err != nil || json.Unmarshal(raw, &states[i]) != nil {
			return nil, nil, ErrSSOUnavailable
		}
		for _, p := range []*APIIssuerProfile{states[i].Active, states[i].Draft, states[i].Previous} {
			if p != nil && (p.APIIssuerConfig.validate(row.APIIssuer) != nil || !validJWTIdentityValue(p.ID, 64)) {
				return nil, nil, ErrSSOUnavailable
			}
		}
	}
	// Configuration removal/revocation releases the old JWKS cache. Never retain
	// candidate verifiers or an unbounded history of policy revisions.
	m.mu.Lock()
	for id, cached := range m.verifiers {
		keep := false
		for i, row := range rows {
			p := states[i].Active
			if row.ID == id && p != nil && p.Enabled && p.ID == cached.profileID {
				keep = true
				break
			}
		}
		if !keep {
			delete(m.verifiers, id)
		}
	}
	m.mu.Unlock()
	return rows, states, nil
}
func apiIssuerView(row apiIssuerRow, state apiIssuerState) APIIssuerView {
	view := APIIssuerView{APIIssuer: row.APIIssuer, Revision: row.Revision, Active: state.Active, Draft: state.Draft, CanRollback: state.CanRollback, LastTestAt: state.LastTestAt, LastTestStatus: state.LastTestStatus}
	if state.Proof != nil {
		view.TestExpiresAt = state.Proof.ExpiresAt
	}
	return view
}
func (m AuthModule) APIIssuers(ctx context.Context) ([]APIIssuerView, error) {
	rows, states, err := m.apiIssuers.rows(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]APIIssuerView, 0, len(rows))
	for i, row := range rows {
		result = append(result, apiIssuerView(row, states[i]))
	}
	return result, nil
}
func (m *APIIssuerManager) load(ctx context.Context, id string) (apiIssuerRow, apiIssuerState, error) {
	rows, states, err := m.rows(ctx)
	if err != nil {
		return apiIssuerRow{}, apiIssuerState{}, err
	}
	for i, row := range rows {
		if row.ID == id {
			return row, states[i], nil
		}
	}
	return apiIssuerRow{}, apiIssuerState{}, ErrSSOConfiguration
}
func (m *APIIssuerManager) save(ctx context.Context, row apiIssuerRow, state apiIssuerState) (APIIssuerView, error) {
	payload, err := m.seal(row.APIIssuer, state)
	if err != nil {
		return APIIssuerView{}, err
	}
	if err = m.store.SaveAPIIssuer(ctx, row.ID, row.Revision, payload); err != nil {
		return APIIssuerView{}, err
	}
	row.Revision++
	return apiIssuerView(row, state), nil
}
func (m AuthModule) apiNamespaceAvailable(ctx context.Context, issuer, audience string) error {
	primary, err := m.currentJWTModule(ctx)
	if err != nil {
		return err
	}
	if primary.jwtConfig.Issuer == issuer && primary.jwtConfig.Audience == audience {
		return ErrSSOConfiguration
	}
	if m.sso == nil {
		return nil
	}
	views, err := m.sso.Connections(ctx)
	if err != nil {
		return err
	}
	for _, v := range views {
		for _, p := range []*SSOProfileView{v.Active, v.Draft} {
			if p != nil && p.Issuer == issuer && p.ClientID == audience {
				return ErrSSOConfiguration
			}
		}
	}
	return nil
}
func (m AuthModule) CreateAPIIssuer(ctx context.Context, input APIIssuerInput) (APIIssuerView, error) {
	if m.apiIssuers == nil {
		return APIIssuerView{}, ErrSSOUnavailable
	}
	if input.APIIssuerConfig.validate(input.APIIssuer) != nil {
		return APIIssuerView{}, ErrSSOConfiguration
	}
	if err := m.apiNamespaceAvailable(ctx, input.Issuer, input.Audience); err != nil {
		return APIIssuerView{}, err
	}
	id, err := ssoRandom()
	if err != nil {
		return APIIssuerView{}, err
	}
	state := apiIssuerState{Draft: &APIIssuerProfile{APIIssuerConfig: input.APIIssuerConfig, ID: id, Enabled: true}}
	payload, err := m.apiIssuers.seal(input.APIIssuer, state)
	if err != nil {
		return APIIssuerView{}, err
	}
	if err = m.apiIssuers.store.CreateAPIIssuer(ctx, input.APIIssuer, payload); err != nil {
		return APIIssuerView{}, err
	}
	return apiIssuerView(apiIssuerRow{APIIssuer: input.APIIssuer, Revision: 1}, state), nil
}
func (m AuthModule) SaveAPIIssuerDraft(ctx context.Context, id string, input APIIssuerDraftInput) (APIIssuerView, error) {
	row, state, err := m.apiIssuers.load(ctx, id)
	if err != nil {
		return APIIssuerView{}, err
	}
	if row.Revision != input.ExpectedRevision {
		return APIIssuerView{}, ErrSSOConflict
	}
	if input.APIIssuerConfig.validate(row.APIIssuer) != nil {
		return APIIssuerView{}, ErrSSOConfiguration
	}
	profileID, err := ssoRandom()
	if err != nil {
		return APIIssuerView{}, err
	}
	state.Draft = &APIIssuerProfile{APIIssuerConfig: input.APIIssuerConfig, ID: profileID, Enabled: true}
	state.Proof = nil
	return m.apiIssuers.save(ctx, row, state)
}
func (m AuthModule) apiCandidate(issuer APIIssuer, p *APIIssuerProfile, v *jwtVerifier) AuthModule {
	m.sso, m.apiIssuers = nil, nil
	m.jwtConfig = p.jwtConfig(issuer)
	m.jwtVerifier = v
	m.jwtOrganizationID = issuer.OrganizationID
	return m
}
func (m AuthModule) validAPIIssuerProof(ctx context.Context, issuer APIIssuer, req RequestContext, actor string) bool {
	if req.UserID != actor || req.OrganizationID != issuer.OrganizationID || req.JWTIdentity == nil {
		return false
	}
	if issuer.OrganizationID == "" {
		return slices.Contains(req.Roles, "admin")
	}
	return m.validSSOTestAdmin(ctx, &SSOProfile{SSOProfileConfig: SSOProfileConfig{OrganizationID: issuer.OrganizationID}}, req)
}
func (m AuthModule) TestAPIIssuer(ctx context.Context, id string, revision int64, actor, token string) (APIIssuerView, error) {
	row, state, err := m.apiIssuers.load(ctx, id)
	if err != nil {
		return APIIssuerView{}, err
	}
	if row.Revision != revision {
		return APIIssuerView{}, ErrSSOConflict
	}
	if state.Draft == nil || !validJWTIdentityValue(actor, 256) || len(token) > 32768 {
		return APIIssuerView{}, ErrSSOConfiguration
	}
	verifier, err := newJWTVerifier(state.Draft.jwtConfig(row.APIIssuer))
	if err != nil {
		return APIIssuerView{}, ErrSSOConfiguration
	}
	candidate := m.apiCandidate(row.APIIssuer, state.Draft, verifier)
	req := RequestContext{APIKey: token}
	err = candidate.authorizeJWTConfigured(ctx, &req)
	req.APIKey = ""
	state.Proof = nil
	state.LastTestAt, state.LastTestStatus = m.apiIssuers.now().Unix(), "failed"
	if err == nil && m.validAPIIssuerProof(ctx, row.APIIssuer, req, actor) {
		claims, verifyErr := verifier.Verify(ctx, token)
		if verifyErr != nil {
			return APIIssuerView{}, verifyErr
		}
		expires := min(m.apiIssuers.now().Add(5*time.Minute).Unix(), claims.ExpiresAt)
		state.Proof = &apiIssuerProof{Actor: actor, ExpiresAt: expires, Request: req}
		state.LastTestStatus = "passed"
	}
	view, saveErr := m.apiIssuers.save(ctx, row, state)
	if saveErr != nil {
		return APIIssuerView{}, saveErr
	}
	if state.Proof == nil {
		if errors.Is(err, ErrJWTUnavailable) || errors.Is(err, ErrJWTDirectoryUnavailable) {
			return APIIssuerView{}, err
		}
		return APIIssuerView{}, ErrUnauthorized
	}
	return view, nil
}
func (m AuthModule) ChangeAPIIssuer(ctx context.Context, id, action string, revision int64, actor string) (APIIssuerView, error) {
	row, state, err := m.apiIssuers.load(ctx, id)
	if err != nil {
		return APIIssuerView{}, err
	}
	if row.Revision != revision {
		return APIIssuerView{}, ErrSSOConflict
	}
	switch action {
	case "activate":
		if state.Draft == nil || state.Proof == nil || state.Proof.ExpiresAt <= m.apiIssuers.now().Unix() || state.Proof.Actor != actor {
			return APIIssuerView{}, ErrSSOConfiguration
		}
		if err = m.apiNamespaceAvailable(ctx, row.Issuer, row.Audience); err != nil {
			return APIIssuerView{}, err
		}
		candidate := m.apiCandidate(row.APIIssuer, state.Draft, nil)
		req := state.Proof.Request
		if err = candidate.ReauthorizeJWTPrincipal(ctx, &req); err != nil {
			return APIIssuerView{}, err
		}
		if !m.validAPIIssuerProof(ctx, row.APIIssuer, req, actor) {
			return APIIssuerView{}, ErrUnauthorized
		}
		state.Previous, state.Active, state.Draft = state.Active, state.Draft, nil
		state.CanRollback = true
	case "disable":
		if state.Active == nil || !state.Active.Enabled {
			return APIIssuerView{}, ErrSSOConfiguration
		}
		previous := *state.Active
		state.Previous = &previous
		active := *state.Active
		active.Enabled = false
		state.Active = &active
		state.CanRollback = true
	case "rollback":
		if !state.CanRollback {
			return APIIssuerView{}, ErrSSOConfiguration
		}
		if err = m.apiNamespaceAvailable(ctx, row.Issuer, row.Audience); err != nil {
			return APIIssuerView{}, err
		}
		state.Active, state.Previous = state.Previous, state.Active
		state.Draft = nil
	default:
		return APIIssuerView{}, ErrSSOConfiguration
	}
	state.Proof = nil
	return m.apiIssuers.save(ctx, row, state)
}
func (m *APIIssuerManager) verifier(row apiIssuerRow, p *APIIssuerProfile) (*jwtVerifier, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached, ok := m.verifiers[row.ID]; ok && cached.profileID == p.ID {
		return cached.verifier, nil
	}
	v, err := newJWTVerifier(p.jwtConfig(row.APIIssuer))
	if err != nil {
		return nil, ErrJWTUnavailable
	}
	if _, exists := m.verifiers[row.ID]; !exists && len(m.verifiers) >= maxAPIIssuers {
		return nil, ErrJWTUnavailable
	}
	m.verifiers[row.ID] = apiIssuerVerifier{profileID: p.ID, verifier: v}
	return v, nil
}
func apiIssuerConnectionID(id, profile string) string { return "api/" + id + "/" + profile }
func (m AuthModule) apiJWTModule(ctx context.Context, issuer string, audience any, connection string) (AuthModule, error) {
	if m.apiIssuers != nil {
		rows, states, err := m.apiIssuers.rows(ctx)
		if err != nil {
			return m, ErrJWTUnavailable
		}
		matches := 0
		for _, row := range rows {
			if row.Issuer == issuer && claimHasAudience(audience, row.Audience) {
				matches++
			}
		}
		if matches > 1 {
			return m, ErrUnauthorized
		}
		for i, row := range rows {
			if row.Issuer != issuer || !claimHasAudience(audience, row.Audience) {
				continue
			}
			p := states[i].Active
			if p == nil || !p.Enabled || (connection != "" && connection != apiIssuerConnectionID(row.ID, p.ID)) {
				return m, ErrUnauthorized
			}
			v, err := m.apiIssuers.verifier(row, p)
			if err != nil {
				return m, err
			}
			candidate := m.apiCandidate(row.APIIssuer, p, v)
			candidate.apiConnectionID = apiIssuerConnectionID(row.ID, p.ID)
			return candidate, nil
		}
	}
	primary, err := m.currentJWTModule(ctx)
	if err != nil {
		return m, err
	}
	primary.apiIssuers = nil
	if connection != "" || (primary.jwtConfig.Issuer != "" && primary.jwtConfig.Issuer != issuer) || (primary.jwtConfig.Audience != "" && !claimHasAudience(audience, primary.jwtConfig.Audience)) {
		return m, ErrUnauthorized
	}
	return primary, nil
}
func unverifiedAPIRouting(token string) (jwtClaims, error) {
	if len(token) > 32768 {
		return jwtClaims{}, ErrUnauthorized
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, ErrUnauthorized
	}
	return decodeJWTClaims(parts[1])
}
