package modules

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryAPIIssuers struct {
	fakeVirtualKeyStore
	mu          sync.Mutex
	rows        map[string]apiIssuerRow
	principals  map[string]authorizedJWTPrincipal
	unavailable bool
}

func (s *memoryAPIIssuers) ListAPIIssuers(context.Context) ([]apiIssuerRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return nil, ErrSSOUnavailable
	}
	rows := []apiIssuerRow{}
	for _, row := range s.rows {
		row.Payload = bytes.Clone(row.Payload)
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}
func (s *memoryAPIIssuers) CreateAPIIssuer(_ context.Context, issuer APIIssuer, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) >= maxAPIIssuers {
		return ErrSSOConfiguration
	}
	for _, row := range s.rows {
		if row.ID == issuer.ID || row.Issuer == issuer.Issuer && row.Audience == issuer.Audience {
			return ErrSSOConflict
		}
	}
	s.rows[issuer.ID] = apiIssuerRow{APIIssuer: issuer, Revision: 1, Payload: bytes.Clone(payload)}
	return nil
}
func (s *memoryAPIIssuers) SaveAPIIssuer(_ context.Context, id string, revision int64, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok || row.Revision != revision {
		return ErrSSOConflict
	}
	row.Revision++
	row.Payload = bytes.Clone(payload)
	s.rows[id] = row
	return nil
}
func (s *memoryAPIIssuers) JWTPrincipalsReady(context.Context) error { return nil }
func (s *memoryAPIIssuers) LookupJWTPrincipal(_ context.Context, issuer, subject, audience string) (authorizedJWTPrincipal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.principals[jwtPrincipalCredentialID(issuer, audience, subject)]
	return p, ok, nil
}
func apiIssuerFixture(t *testing.T) (AuthModule, *memoryAPIIssuers) {
	t.Helper()
	store := &memoryAPIIssuers{rows: map[string]apiIssuerRow{}, principals: map[string]authorizedJWTPrincipal{}}
	manager, err := newAPIIssuerManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	module := NewAuthModuleWithJWT(true, JWTAuthConfig{IdentityMode: "legacy"})
	module.store, module.apiIssuers, module.demoKeys = store, manager, false
	return module, store
}
func apiIssuerTestInput(t *testing.T, id, org string) (APIIssuerInput, *ecdsa.PrivateKey, *atomic.Int64) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	requests := new(atomic.Int64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jsonWebKey{ecJWK("same-kid", &key.PublicKey)}})
	}))
	t.Cleanup(server.Close)
	mappings := map[string]string{"owners": "admin"}
	if org != "" {
		mappings = map[string]string{"owners": "org_admin"}
	}
	return APIIssuerInput{APIIssuer: APIIssuer{ID: id, Name: id, OrganizationID: org, Issuer: server.URL, Audience: "api-resource"}, APIIssuerConfig: APIIssuerConfig{JWKSURL: server.URL + "/jwks", RolesClaim: "roles", RoleMappings: mappings}}, key, requests
}
func apiIssuerToken(t *testing.T, input APIIssuerInput, key *ecdsa.PrivateKey, changes map[string]any) string {
	t.Helper()
	claims := map[string]any{"iss": input.Issuer, "aud": input.Audience, "sub": "same-subject", "exp": time.Now().Add(time.Hour).Unix(), "roles": []string{"owners"}}
	for name, value := range changes {
		claims[name] = value
	}
	return signES256JWT(t, "same-kid", key, claims)
}
func approveAPIIssuer(store *memoryAPIIssuers, input APIIssuerInput) {
	p := authorizedJWTPrincipal{JWTPrincipalPolicy: JWTPrincipalPolicy{Issuer: input.Issuer, Audience: input.Audience, Subject: "same-subject", UserID: "operator", OrganizationID: input.OrganizationID, Enabled: true, AllowedModels: []string{"model"}}, Roles: []string{"admin", "org_admin"}, OrganizationRoles: []string{"org_admin"}}
	store.principals[jwtPrincipalCredentialID(input.Issuer, input.Audience, p.Subject)] = p
}
func activateAPIIssuer(t *testing.T, m AuthModule, input APIIssuerInput, key *ecdsa.PrivateKey) APIIssuerView {
	t.Helper()
	view, err := m.CreateAPIIssuer(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.ChangeAPIIssuer(t.Context(), input.ID, "activate", view.Revision, "operator"); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("unverified activation", err)
	}
	view, err = m.TestAPIIssuer(t.Context(), input.ID, view.Revision, "operator", apiIssuerToken(t, input, key, nil))
	if err != nil {
		t.Fatal(err)
	}
	view, err = m.ChangeAPIIssuer(t.Context(), input.ID, "activate", view.Revision, "operator")
	if err != nil {
		t.Fatal(err)
	}
	return view
}
func TestAPIIssuersSeparateSameSubjectAndKidAcrossOrganizations(t *testing.T) {
	m, store := apiIssuerFixture(t)
	a, ka, ca := apiIssuerTestInput(t, "a", "org-a")
	b, kb, cb := apiIssuerTestInput(t, "b", "org-b")
	approveAPIIssuer(store, a)
	approveAPIIssuer(store, b)
	va := activateAPIIssuer(t, m, a, ka)
	vb := activateAPIIssuer(t, m, b, kb)
	first, second := RequestContext{APIKey: apiIssuerToken(t, a, ka, nil)}, RequestContext{APIKey: apiIssuerToken(t, b, kb, nil)}
	if err := m.Handle(t.Context(), &first); err != nil {
		t.Fatal(err)
	}
	if err := m.Handle(t.Context(), &second); err != nil {
		t.Fatal(err)
	}
	if first.OrganizationID != "org-a" || second.OrganizationID != "org-b" || first.CredentialID == second.CredentialID || first.JWTIdentity.ConnectionID == second.JWTIdentity.ConnectionID || first.APIKey != "" {
		t.Fatal("issuer/tenant namespace lost")
	}
	if err := m.ReauthorizeJWTPrincipal(t.Context(), &first); err != nil {
		t.Fatal(err)
	}
	for _, changes := range []map[string]any{{"iss": "https://untrusted.invalid"}, {"aud": "browser-client"}, {"nonce": "browser-id-token"}, {"exp": time.Now().Add(-time.Hour).Unix()}} {
		req := RequestContext{APIKey: apiIssuerToken(t, a, ka, changes)}
		if err := m.Handle(t.Context(), &req); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("invalid claims accepted", err)
		}
	}
	// A valid token signed by the other issuer's key must not borrow its kid cache.
	if err := m.Handle(t.Context(), &RequestContext{APIKey: apiIssuerToken(t, a, kb, nil)}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("shared kid trusted across issuers", err)
	}
	oldCalls := ca.Load() + cb.Load()
	if err := m.Handle(t.Context(), &RequestContext{APIKey: apiIssuerToken(t, a, ka, map[string]any{"iss": "https://attacker.invalid"})}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if ca.Load()+cb.Load() != oldCalls {
		t.Fatal("unknown issuer caused a network lookup")
	}
	va, err := m.ChangeAPIIssuer(t.Context(), "a", "disable", va.Revision, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Handle(t.Context(), &RequestContext{APIKey: apiIssuerToken(t, a, ka, nil)}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled issuer accepted", err)
	}
	if err = m.ReauthorizeJWTPrincipal(t.Context(), &first); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled issuer job accepted", err)
	}
	if err = m.Handle(t.Context(), &RequestContext{APIKey: apiIssuerToken(t, b, kb, nil)}); err != nil {
		t.Fatal("other issuer revoked", err)
	}
	if _, err = m.ChangeAPIIssuer(t.Context(), "a", "rollback", va.Revision, "operator"); err != nil {
		t.Fatal(err)
	}
	if err = m.Handle(t.Context(), &RequestContext{APIKey: apiIssuerToken(t, a, ka, nil)}); err != nil {
		t.Fatal(err)
	}
	if vb.Active == nil || len(m.apiIssuers.verifiers) > maxAPIIssuers {
		t.Fatal("registry cache invalid")
	}
}
func TestAPIIssuerProofCASPolicyAndCipherBinding(t *testing.T) {
	m, store := apiIssuerFixture(t)
	input, key, _ := apiIssuerTestInput(t, "a", "org-a")
	approveAPIIssuer(store, input)
	view, err := m.CreateAPIIssuer(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.TestAPIIssuer(t.Context(), "a", view.Revision, "someone-else", apiIssuerToken(t, input, key, nil)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("other operator proved trust", err)
	}
	views, err := m.APIIssuers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	view = views[0]
	token := apiIssuerToken(t, input, key, nil)
	view, err = m.TestAPIIssuer(t.Context(), "a", view.Revision, "operator", token)
	if err != nil {
		t.Fatal(err)
	}
	row := store.rows["a"]
	raw, err := m.apiIssuers.aead.Open(nil, row.Payload[:m.apiIssuers.aead.NonceSize()], row.Payload[m.apiIssuers.aead.NonceSize():], apiIssuerAAD(row.APIIssuer))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(token)) || bytes.Contains(row.Payload, []byte(input.JWKSURL)) {
		t.Fatal("token persisted or config unencrypted")
	}
	if _, err = m.ChangeAPIIssuer(t.Context(), "a", "activate", view.Revision-1, "operator"); !errors.Is(err, ErrSSOConflict) {
		t.Fatal("stale CAS accepted", err)
	}
	principalKey := jwtPrincipalCredentialID(input.Issuer, input.Audience, "same-subject")
	p := store.principals[principalKey]
	p.OrganizationRoles = nil
	store.principals[principalKey] = p
	if _, err = m.ChangeAPIIssuer(t.Context(), "a", "activate", view.Revision, "operator"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("deprovisioned proof activated", err)
	}
	row.APIIssuer.OrganizationID = "org-b"
	store.rows["a"] = row
	if _, err = m.APIIssuers(t.Context()); !errors.Is(err, ErrSSOUnavailable) {
		t.Fatal("cipher metadata reassigned", err)
	}
}
func TestAPIIssuerConfigurationBoundsAndTrustSeparation(t *testing.T) {
	m, store := apiIssuerFixture(t)
	input, _, _ := apiIssuerTestInput(t, "a", "")
	for _, mutate := range []func(*APIIssuerInput){func(i *APIIssuerInput) { i.JWKSURL = "https://different.example/jwks" }, func(i *APIIssuerInput) { i.Issuer = "http://unsafe.example" }, func(i *APIIssuerInput) { i.RolesClaim = "" }, func(i *APIIssuerInput) { i.OrganizationID = "org-a" }, func(i *APIIssuerInput) { i.RoleMappings = map[string]string{"unknown": "root"} }} {
		bad := input
		mutate(&bad)
		if _, err := m.CreateAPIIssuer(t.Context(), bad); !errors.Is(err, ErrSSOConfiguration) {
			t.Fatal("invalid trust accepted", err)
		}
	}
	browser, _ := connectionFixture(t)
	m.sso = browser
	if _, err := m.CreateAPIIssuer(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	config := testSSOConfig()
	config.Issuer, config.ClientID, config.Audience = input.Issuer, input.Audience, input.Audience
	if _, err := m.SaveSSODraft(t.Context(), SSODraftInput{SSOProfileConfig: config}); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("API audience reused for browser", err)
	}
	primary := m
	primary.jwtConfig.Issuer, primary.jwtConfig.Audience = input.Issuer, "primary-api"
	bad := input
	bad.ID = "primary"
	bad.Audience = "primary-api"
	if _, err := primary.CreateAPIIssuer(t.Context(), bad); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("primary namespace overwritten", err)
	}
	store.unavailable = true
	if err := m.Handle(t.Context(), &RequestContext{APIKey: "a.b.c"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	store.unavailable = false
	for n := 1; n < maxAPIIssuers; n++ {
		entry := input
		entry.ID = strings.Repeat("a", n+1)
		entry.Audience = entry.ID
		if _, err := m.CreateAPIIssuer(t.Context(), entry); err != nil {
			t.Fatal(err)
		}
	}
	overflow := input
	overflow.ID = "overflow"
	overflow.Audience = "overflow"
	if _, err := m.CreateAPIIssuer(t.Context(), overflow); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("registry exceeded bound", err)
	}
}

func TestBrowserLifecycleRechecksAPIReservationsAfterDraftSave(t *testing.T) {
	m, _ := apiIssuerFixture(t)
	input, _, _ := apiIssuerTestInput(t, "api", "")
	browser, _ := connectionFixture(t)
	m.sso = browser
	if _, err := m.CreateAPIIssuer(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	config := testSSOConfig()
	config.Issuer = input.Issuer
	config.ClientID = input.Audience
	config.Audience = input.Audience
	config.JWKSURL = input.JWKSURL
	config.AuthorizationURL = input.Issuer + "/authorize"
	config.TokenURL = input.Issuer + "/token"
	// Model an edit that passed its initial check before another replica reserved
	// this namespace. Activation and rollback must check the current registry.
	if _, err := browser.SaveDraft(t.Context(), SSODraftInput{SSOProfileConfig: config}); err != nil {
		t.Fatal(err)
	}
	if _, err := browser.StartTest(t.Context(), 1, "operator"); err != nil {
		t.Fatal(err)
	}
	state, revision, err := browser.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.Attempt.Status = "passed"
	state.Attempt.UserID = "operator"
	if err = browser.save(t.Context(), revision, state); err != nil {
		t.Fatal(err)
	}
	revision++
	if _, err = m.ChangeSSO(t.Context(), "activate", revision, "operator"); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("browser activation reused a newly reserved API audience", err)
	}
	state.Previous, state.Draft, state.Attempt = state.Draft, nil, nil
	state.CanRollback = true
	if err = browser.save(t.Context(), revision, state); err != nil {
		t.Fatal(err)
	}
	revision++
	if _, err = m.ChangeSSO(t.Context(), "rollback", revision, "operator"); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("browser rollback reused API audience", err)
	}
}
