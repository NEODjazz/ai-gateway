package modules

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memorySSOSettings struct {
	mu       sync.Mutex
	revision int64
	payload  []byte
	err      error
}

func (s *memorySSOSettings) LoadSSOSettings(context.Context) (int64, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision, bytes.Clone(s.payload), s.err
}
func (s *memorySSOSettings) SaveSSOSettings(_ context.Context, revision int64, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if revision != s.revision {
		return ErrSSOConflict
	}
	s.revision++
	s.payload = bytes.Clone(payload)
	return nil
}
func testSSOConfig() SSOProfileConfig {
	return SSOProfileConfig{Issuer: "https://idp.example/tenant", Audience: "gateway", JWKSURL: "https://idp.example/jwks", AuthorizationURL: "https://idp.example/authorize", TokenURL: "https://idp.example/token", ClientID: "console", RedirectURL: "https://gateway.example/auth/sso/callback", Scopes: []string{"openid", "profile"}, RolesClaim: "roles", RoleMappings: map[string]string{"gateway-admin": "admin", "gateway-user": "user"}, SessionTTLSeconds: 3600}
}
func testSSOManager(t *testing.T) (*SSOManager, *memorySSOSettings) {
	t.Helper()
	store := &memorySSOSettings{}
	manager, err := NewSSOManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Unix(1000, 0) }
	return manager, store
}
func TestSSODraftEncryptionRedactionRevisionAndTicket(t *testing.T) {
	ctx := context.Background()
	manager, store := testSSOManager(t)
	secret := "test-client-secret"
	input := SSODraftInput{SSOProfileConfig: testSSOConfig(), ClientSecret: &secret}
	view, err := manager.SaveDraft(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	state, revision, err := manager.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 1 || state.Draft.ClientSecret != secret || !view.Draft.ClientSecretConfigured {
		t.Fatal("draft not stored")
	}
	encoded, _ := json.Marshal(view)
	if bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte("session_key")) || bytes.Contains(store.payload, []byte(secret)) || bytes.Contains(store.payload, []byte(state.Draft.SessionKey)) {
		t.Fatal("secret exposed or stored in plaintext")
	}
	if _, err := manager.SaveDraft(ctx, input); !errors.Is(err, ErrSSOConflict) {
		t.Fatal("stale revision accepted")
	}
	ticket, err := manager.StartTest(ctx, 1, "admin-user")
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ = manager.Load(ctx)
	if !validSSOTicket(state.Attempt, ticket, manager.now()) || validSSOTicket(state.Attempt, strings.Repeat("x", 43), manager.now()) || validSSOTicket(state.Attempt, ticket, manager.now().Add(5*time.Minute)) {
		t.Fatal("ticket checks failed")
	}
	if bytes.Contains(store.payload, []byte(ticket)) {
		t.Fatal("raw ticket persisted")
	}
	oldKey := state.Draft.SessionKey
	input.ExpectedRevision = 2
	input.ClientSecret = nil
	input.ClientID = "another-client"
	if _, err := manager.SaveDraft(ctx, input); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("write-only secret was silently transferred to another client")
	}
	input.ClientID = "console"
	view, err = manager.SaveDraft(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ = manager.Load(ctx)
	if state.Draft.ClientSecret != secret || state.Draft.SessionKey == oldKey || state.Attempt != nil || view.TestStatus != "not_started" {
		t.Fatal("edit retained proof or changed preserved secret")
	}
	blank := ""
	input.ClientSecret = &blank
	input.ExpectedRevision = 3
	view, err = manager.SaveDraft(ctx, input)
	if err != nil || view.Draft.ClientSecretConfigured {
		t.Fatal("explicit secret clearing failed")
	}
	other, _ := NewSSOManager(store, strings.Repeat("z", 32))
	if _, _, err := other.Load(ctx); !errors.Is(err, ErrSSOUnavailable) {
		t.Fatal("wrong encryption key accepted")
	}
	store.payload[len(store.payload)-1] ^= 1
	if _, _, err := manager.Load(ctx); !errors.Is(err, ErrSSOUnavailable) {
		t.Fatal("tampered ciphertext accepted")
	}
}
func TestSSOConfigurationRejectsUnsafeOrIncompleteTrust(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*SSOProfileConfig)
	}{
		{"foreign token endpoint", func(p *SSOProfileConfig) { p.TokenURL = "https://evil.example/token" }},
		{"http issuer", func(p *SSOProfileConfig) { p.Issuer = "http://idp.example/tenant" }},
		{"userinfo", func(p *SSOProfileConfig) { p.JWKSURL = "https://password@idp.example/jwks" }},
		{"query", func(p *SSOProfileConfig) { p.TokenURL += "?secret=value" }},
		{"callback path", func(p *SSOProfileConfig) { p.RedirectURL = "https://gateway.example/evil" }},
		{"missing openid", func(p *SSOProfileConfig) { p.Scopes = []string{"profile"} }},
		{"scope injection", func(p *SSOProfileConfig) { p.Scopes = []string{"openid", "profile email"} }},
		{"missing mappings", func(p *SSOProfileConfig) { p.RoleMappings = nil }},
		{"invalid role", func(p *SSOProfileConfig) { p.RoleMappings = map[string]string{"owner": "superuser"} }},
		{"unbounded ttl", func(p *SSOProfileConfig) { p.SessionTTLSeconds = 86401 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := testSSOConfig()
			test.mutate(&p)
			if p.Validate() == nil {
				t.Fatal("invalid trust accepted")
			}
		})
	}
	p := testSSOConfig()
	p.RedirectURL = "http://ai-gateway.localhost/auth/sso/callback"
	if err := p.Validate(); err != nil {
		t.Fatal("localhost redirect rejected")
	}
	if _, err := NewSSOManager(&memorySSOSettings{}, "short"); !errors.Is(err, ErrSSOUnavailable) {
		t.Fatal("weak encryption key accepted")
	}
}

func TestSSOTestActivationDirectoryRevocationAndRollback(t *testing.T) {
	ctx := context.Background()
	manager, _ := testSSOManager(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jsonWebKey{rsaJWK("test", &key.PublicKey)}})
	}))
	defer server.Close()
	config := testSSOConfig()
	config.Issuer = server.URL
	config.JWKSURL = server.URL + "/jwks"
	config.TokenURL = server.URL + "/token"
	config.AuthorizationURL = server.URL + "/authorize"
	manager.now = time.Now
	module, store, _ := directoryJWTModule(t)
	store.principal.Issuer = config.Issuer
	store.principal.Roles = []string{"admin"}
	module.sso = manager
	view, err := manager.SaveDraft(ctx, SSODraftInput{SSOProfileConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.ChangeSSO(ctx, "activate", view.Revision, "directory-user"); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("untested draft activated")
	}
	ticket, err := manager.StartTest(ctx, view.Revision, "other-admin")
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": config.Issuer, "aud": "gateway", "sub": "external-subject", "roles": []string{"gateway-admin"}, "exp": time.Now().Add(time.Hour).Unix()}
	token := signRS256JWT(t, "test", key, claims)
	if err := module.VerifySSOTest(ctx, view.Draft.ID, ticket, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("another admin's proof accepted")
	}
	view, _ = manager.View(ctx)
	ticket, err = manager.StartTest(ctx, view.Revision, "directory-user")
	if err != nil {
		t.Fatal(err)
	}
	if err := module.VerifySSOTest(ctx, view.Draft.ID, ticket, token); err != nil {
		t.Fatal(err)
	}
	if err := module.VerifySSOTest(ctx, view.Draft.ID, ticket, token); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("proof replay accepted")
	}
	view, _ = manager.View(ctx)
	store.principal.Enabled = false
	if _, err := module.ChangeSSO(ctx, "activate", view.Revision, "directory-user"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("deprovisioned administrator activated trust")
	}
	store.principal.Enabled = true
	view, err = module.ChangeSSO(ctx, "activate", view.Revision, "directory-user")
	if err != nil {
		t.Fatal(err)
	}
	if view.Active == nil || view.Draft != nil || !view.CanRollback {
		t.Fatal("activation state invalid")
	}
	req := RequestContext{APIKey: token}
	if err := module.Handle(ctx, &req); err != nil || req.UserID != "directory-user" {
		t.Fatalf("managed JWT rejected: %v", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := module.currentJWTModule(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	view, err = module.ChangeSSO(ctx, "disable", view.Revision, "directory-user")
	if err != nil || view.Active.Enabled {
		t.Fatal("disable failed")
	}
	if err := module.Handle(ctx, &RequestContext{APIKey: token}); err != nil {
		t.Fatal("browser disable revoked inference JWT trust")
	}
	view, err = module.ChangeSSO(ctx, "rollback", view.Revision, "directory-user")
	if err != nil || !view.Active.Enabled {
		t.Fatal("disable rollback failed")
	}
}

func TestSSOStoreFailureLeavesVirtualKeysAvailable(t *testing.T) {
	manager, store := testSSOManager(t)
	store.err = ErrSSOUnavailable
	module := NewAuthModuleWithVirtualKeys(true, []VirtualKey{{Token: "test-admin-key", UserID: "admin", Roles: []string{"admin"}}})
	module.sso = manager
	if err := module.Handle(context.Background(), &RequestContext{APIKey: "test-admin-key"}); err != nil {
		t.Fatal("key fallback failed")
	}
	if _, err := module.currentJWTModule(context.Background()); !errors.Is(err, ErrJWTUnavailable) {
		t.Fatal("SSO outage failed open")
	}
}

func TestSSODiscoveryValidatesIssuerEndpointsAndPKCE(t *testing.T) {
	var issuer string
	mode := "valid"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		metadata := map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "code_challenge_methods_supported": []string{"S256"}}
		switch mode {
		case "issuer":
			metadata["issuer"] = "https://foreign.example"
		case "endpoint":
			metadata["token_endpoint"] = "https://foreign.example/token"
		case "pkce":
			metadata["code_challenge_methods_supported"] = []string{"plain"}
		case "redirect":
			http.Redirect(w, &http.Request{}, "https://foreign.example", 302)
			return
		}
		_ = json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()
	issuer = server.URL
	if _, err := DiscoverSSO(context.Background(), issuer); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"issuer", "endpoint", "pkce", "redirect"} {
		mode = invalid
		if _, err := DiscoverSSO(context.Background(), issuer); !errors.Is(err, ErrSSOConfiguration) {
			t.Fatalf("%s accepted: %v", invalid, err)
		}
	}
}
