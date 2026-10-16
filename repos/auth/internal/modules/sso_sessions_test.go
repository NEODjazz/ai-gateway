package modules

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memorySSOSessions struct {
	*memorySSOSettings
	sessions map[string][]byte
	logins   map[string]bool
}

func (s *memorySSOSessions) CreateSSOSession(_ context.Context, hash, login, profile, user string, expires int64, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logins[login] {
		return ErrUnauthorized
	}
	s.logins[login] = true
	s.sessions[hash] = bytes.Clone(payload)
	return nil
}
func (s *memorySSOSessions) LoadSSOSession(_ context.Context, hash string, _ int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, ok := s.sessions[hash]; ok {
		return bytes.Clone(value), nil
	}
	return nil, ErrUnauthorized
}
func (s *memorySSOSessions) RevokeSSOSession(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, hash)
	return nil
}
func browserIdentityFixture(t *testing.T) (AuthModule, *fakeJWTPrincipalStore, *memorySSOSessions, *SSOProfile, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jsonWebKey{rsaJWK("browser", &key.PublicKey)}})
	}))
	t.Cleanup(server.Close)
	module, directory, _ := directoryJWTModule(t)
	config := testSSOConfig()
	config.Issuer = server.URL
	config.JWKSURL = server.URL + "/jwks"
	config.AuthorizationURL = server.URL + "/authorize"
	config.TokenURL = server.URL + "/token"
	config.Audience = config.ClientID
	directory.principal.Issuer = config.Issuer
	directory.principal.Audience = config.ClientID
	directory.principal.Roles = []string{"admin", "user"}
	store := &memorySSOSessions{memorySSOSettings: &memorySSOSettings{}, sessions: map[string][]byte{}, logins: map[string]bool{}}
	manager, err := NewSSOManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	module.sso = manager
	profile := &SSOProfile{SSOProfileConfig: config, ID: "browser-profile", Enabled: true, SessionKey: strings.Repeat("epoch", 8)}
	if err := manager.save(t.Context(), 0, SSOSettingsState{SchemaVersion: 1, APITrustSeparated: true, Active: profile}); err != nil {
		t.Fatal(err)
	}
	return module, directory, store, profile, key
}
func browserIdentityClaims(profile *SSOProfile, nonce string) map[string]any {
	return map[string]any{"iss": profile.Issuer, "aud": profile.ClientID, "sub": "external-subject", "roles": []string{"gateway-admin"}, "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(), "nonce": nonce}
}

func TestSSOServerSessionPinsOrganizationAndReloadsScopedApproval(t *testing.T) {
	m, directory, _, profile, key := browserIdentityFixture(t)
	profile.RoleMappings = map[string]string{"organization-admin": "org_admin"}
	state, revision, err := m.sso.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.Active = profile
	if err = m.sso.save(t.Context(), revision, state); err != nil {
		t.Fatal(err)
	}
	directory.principal.Roles = []string{"org_admin"}
	directory.principal.OrganizationRoles = []string{"org_admin"}
	nonce := strings.Repeat("n", 43)
	claims := browserIdentityClaims(profile, nonce)
	claims["roles"] = []string{"organization-admin"}
	session, err := m.CreateSSOBrowserSession(t.Context(), SSOBrowserLogin{ProfileID: profile.ID, Token: signRS256JWT(t, "browser", key, claims), Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Handle(t.Context(), &RequestContext{APIKey: session.Token}); err != nil {
		t.Fatal(err)
	}
	directory.principal.OrganizationID = "org-b"
	if err = m.Handle(t.Context(), &RequestContext{APIKey: session.Token}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("browser session changed tenant", err)
	}
	directory.principal.OrganizationID = "org-a"
	directory.principal.OrganizationRoles = []string{"user"}
	if err = m.Handle(t.Context(), &RequestContext{APIKey: session.Token}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("global directory role bypassed removed approval", err)
	}
}
func TestSSOIDTokenValidationIsIndependentAndRejectsInvalidClaims(t *testing.T) {
	module, _, _, profile, key := browserIdentityFixture(t)
	nonce := strings.Repeat("n", 43)
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		nonce  string
		valid  bool
	}{
		{"valid", func(map[string]any) {}, nonce, true},
		{"wrong nonce", func(map[string]any) {}, strings.Repeat("x", 43), false},
		{"missing nonce", func(c map[string]any) { delete(c, "nonce") }, nonce, false},
		{"noncanonical nonce", func(c map[string]any) { c["nonce"] = " " + nonce }, nonce, false},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://foreign.example" }, nonce, false},
		{"API audience", func(c map[string]any) { c["aud"] = "api-resource" }, nonce, false},
		{"missing iat", func(c map[string]any) { delete(c, "iat") }, nonce, false},
		{"old iat", func(c map[string]any) { c["iat"] = time.Now().Add(-10 * time.Minute).Unix() }, nonce, false},
		{"future iat", func(c map[string]any) { c["iat"] = time.Now().Add(time.Minute).Unix() }, nonce, false},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, nonce, false},
		{"missing authorized party", func(c map[string]any) { c["aud"] = []string{profile.ClientID, "other"} }, nonce, false},
		{"wrong authorized party", func(c map[string]any) { c["azp"] = "other" }, nonce, false},
		{"invalid authorized party", func(c map[string]any) { c["azp"] = 1 }, nonce, false},
		{"invalid access hash", func(c map[string]any) { c["at_hash"] = 1 }, nonce, false},
		{"noncanonical subject", func(c map[string]any) { c["sub"] = " external-subject " }, nonce, false},
		{"multiple audiences", func(c map[string]any) { c["aud"] = []string{profile.ClientID, "other"}; c["azp"] = profile.ClientID }, nonce, true},
		{"unmapped admin", func(c map[string]any) { c["roles"] = []string{"unexpected-admin"} }, nonce, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := browserIdentityClaims(profile, nonce)
			test.mutate(claims)
			token := signRS256JWT(t, "browser", key, claims)
			req, err := module.verifySSOIdentity(t.Context(), profile, SSOBrowserLogin{ProfileID: profile.ID, Token: token, Nonce: test.nonce})
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
			if test.valid && (req.UserID != "directory-user" || req.JWTIdentity.Audience != profile.ClientID) {
				t.Fatal("wrong browser identity")
			}
		})
	}
	// Even an API audience accidentally equal to the browser client cannot accept
	// a nonce-bearing browser ID token as an API credential.
	cfg := profile.jwtConfig()
	cfg.Audience = profile.ClientID
	api := NewAuthModuleWithJWT(true, cfg)
	api.store = module.store
	token := signRS256JWT(t, "browser", key, browserIdentityClaims(profile, nonce))
	if err := api.Handle(t.Context(), &RequestContext{APIKey: token}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("browser ID token accepted by API")
	}
}
func TestSSOIDTokenAccessTokenHash(t *testing.T) {
	module, _, _, profile, key := browserIdentityFixture(t)
	nonce := strings.Repeat("n", 43)
	access := "opaque-provider-token"
	claims := browserIdentityClaims(profile, nonce)
	digest := sha256.Sum256([]byte(access))
	claims["at_hash"] = base64.RawURLEncoding.EncodeToString(digest[:16])
	token := signRS256JWT(t, "browser", key, claims)
	for _, value := range []string{access, "wrong", ""} {
		_, err := module.verifySSOIdentity(t.Context(), profile, SSOBrowserLogin{ProfileID: profile.ID, Token: token, Nonce: nonce, AccessToken: value})
		if (err == nil) != (value == access) {
			t.Fatal("access token hash verification failed")
		}
	}
}
func TestSSOServerSessionRechecksDirectoryAndRevokes(t *testing.T) {
	module, directory, store, profile, key := browserIdentityFixture(t)
	nonce := strings.Repeat("n", 43)
	token := signRS256JWT(t, "browser", key, browserIdentityClaims(profile, nonce))
	login := SSOBrowserLogin{ProfileID: profile.ID, Token: token, Nonce: nonce, AccessToken: "opaque-provider-token"}
	session, err := module.CreateSSOBrowserSession(t.Context(), login)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(session.Token, token) || session.ExpiresAt < time.Now().Add(50*time.Minute).Unix() {
		t.Fatal("upstream token retained or session incorrectly bounded by ID token expiry")
	}
	hash, ok := ssoSessionHash(session.Token)
	if !ok {
		t.Fatal("invalid opaque handle")
	}
	if bytes.Contains(store.sessions[hash], []byte(token)) || bytes.Contains(store.sessions[hash], []byte("directory-user")) {
		t.Fatal("session persisted unencrypted")
	}
	if _, err := module.CreateSSOBrowserSession(t.Context(), login); err == nil {
		t.Fatal("browser callback replay accepted")
	}
	request := func() (RequestContext, error) {
		req := RequestContext{APIKey: session.Token}
		err := module.Handle(t.Context(), &req)
		return req, err
	}
	req, err := request()
	if err != nil || req.UserID != "directory-user" || req.Metadata["auth.method"] != "browser_sso" {
		t.Fatalf("session rejected: %v", err)
	}
	directory.principal.AllowedModels = []string{"updated-model"}
	req, err = request()
	if err != nil || len(req.AllowedModels) != 1 || req.AllowedModels[0] != "updated-model" {
		t.Fatal("session retained stale grants")
	}
	directory.principal.Enabled = false
	if _, err := request(); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("deprovisioned session accepted")
	}
	directory.principal.Enabled = true
	directory.principal.Roles = []string{"user"}
	if _, err := request(); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked admin role retained")
	}
	directory.principal.Roles = []string{"admin"}
	if err := module.RevokeSSOBrowserSession(t.Context(), session.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := request(); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("logged out session accepted")
	}
	if _, err := module.CreateSSOBrowserSession(t.Context(), login); err == nil {
		t.Fatal("replay accepted after logout")
	}
}
func TestSSOServerSessionDisableRollbackAndExpiry(t *testing.T) {
	module, _, _, profile, key := browserIdentityFixture(t)
	nonce := strings.Repeat("n", 43)
	session, err := module.CreateSSOBrowserSession(t.Context(), SSOBrowserLogin{ProfileID: profile.ID, Token: signRS256JWT(t, "browser", key, browserIdentityClaims(profile, nonce)), Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	_, revision, _ := module.sso.Load(t.Context())
	view, err := module.ChangeSSO(t.Context(), "disable", revision, "directory-user")
	if err != nil {
		t.Fatal(err)
	}
	if err := module.Handle(t.Context(), &RequestContext{APIKey: session.Token}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled connection session accepted")
	}
	_, err = module.ChangeSSO(t.Context(), "rollback", view.Revision, "directory-user")
	if err != nil {
		t.Fatal(err)
	}
	if err := module.Handle(t.Context(), &RequestContext{APIKey: session.Token}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("rollback resurrected revoked session")
	}
	module.sso.now = func() time.Time { return time.Unix(session.ExpiresAt+1, 0) }
	if err := module.Handle(t.Context(), &RequestContext{APIKey: session.Token}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired session accepted")
	}
}
