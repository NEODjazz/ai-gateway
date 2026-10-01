package modules

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeJWTPrincipalStore struct {
	fakeVirtualKeyStore
	principal authorizedJWTPrincipal
	found     bool
	err       error
	lookup    [3]string
}

func (s *fakeJWTPrincipalStore) JWTPrincipalsReady(context.Context) error { return s.err }
func (s *fakeJWTPrincipalStore) LookupJWTPrincipal(_ context.Context, issuer, subject, audience string) (authorizedJWTPrincipal, bool, error) {
	s.lookup = [3]string{issuer, subject, audience}
	return s.principal, s.found, s.err
}

func directoryJWTModule(t *testing.T) (AuthModule, *fakeJWTPrincipalStore, map[string]any) {
	t.Helper()
	module := NewAuthModuleWithJWT(true, JWTAuthConfig{
		Secret: "test-signing-secret", Issuer: "https://idp.example/realms/users", Audience: "gateway",
		IdentityMode: "directory", RolesClaim: "resource_access.gateway.roles",
		RoleMappings: map[string]string{"gateway-user": "user", "gateway-admin": "admin"},
	})
	module.jwtVerifier.now = func() time.Time { return time.Unix(1000, 0) }
	store := &fakeJWTPrincipalStore{found: true, principal: authorizedJWTPrincipal{
		JWTPrincipalPolicy: JWTPrincipalPolicy{Issuer: module.jwtConfig.Issuer, Audience: "gateway", Subject: "external-subject", UserID: "directory-user", TeamID: "team-a", Tags: []string{"regulated"}, AccessGroupIDs: []string{"group-a"}, AllowedModels: []string{"model-a"}, AllowedTools: []string{"read"}, RateLimitRPM: 7, RateLimitTPM: 100, Enabled: true},
		OrganizationID:     "org-a", Roles: []string{"user"},
	}}
	module.store = store
	claims := map[string]any{"iss": module.jwtConfig.Issuer, "aud": []string{"openwebui", "gateway"}, "sub": "external-subject", "exp": 2000,
		"resource_access": map[string]any{"gateway": map[string]any{"roles": []string{"gateway-user"}}}}
	return module, store, claims
}

func TestDirectoryJWTRefreshPreservesIdentityAndReloadsPolicy(t *testing.T) {
	module, store, claims := directoryJWTModule(t)
	first := RequestContext{APIKey: testJWT(t, "test-signing-secret", claims)}
	if err := module.Handle(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if first.UserID != "directory-user" || first.CredentialID == "" || first.TeamID != "team-a" || first.OrganizationID != "org-a" || first.APIKey != "" || !first.ModelAccessRestricted || !first.ToolAccessRestricted {
		t.Fatal("directory identity or explicit access restrictions were not applied")
	}
	if first.RateLimitRPM != 7 || first.RateLimitTPM != 100 || !reflect.DeepEqual(first.AllowedModels, []string{"model-a"}) || !reflect.DeepEqual(first.AllowedTools, []string{"read"}) || !reflect.DeepEqual(first.Tags, []string{"regulated"}) || !reflect.DeepEqual(first.AccessGroupIDs, []string{"group-a"}) {
		t.Fatal("directory policy was not applied")
	}
	if store.lookup != [3]string{module.jwtConfig.Issuer, "external-subject", "gateway"} {
		t.Fatal("principal lookup did not use the verified issuer, subject and configured resource audience")
	}
	claims["exp"], claims["jti"] = 3000, "refreshed-token"
	store.principal.AllowedModels, store.principal.RateLimitRPM = []string{"model-b"}, 3
	second := RequestContext{APIKey: testJWT(t, "test-signing-secret", claims)}
	if err := module.Handle(context.Background(), &second); err != nil {
		t.Fatal(err)
	}
	if first.UserID != second.UserID || first.CredentialID != second.CredentialID || second.RateLimitRPM != 3 || !reflect.DeepEqual(second.AllowedModels, []string{"model-b"}) {
		t.Fatal("refresh changed identity or retained stale directory policy")
	}
	store.found = false
	if err := module.Handle(context.Background(), &RequestContext{APIKey: testJWT(t, "test-signing-secret", claims)}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("removed binding retained access: %v", err)
	}
}

func TestDirectoryJWTRejectsUnprovisionedOrUnmappedClaims(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fakeJWTPrincipalStore, map[string]any)
	}{
		{"unknown principal", func(s *fakeJWTPrincipalStore, _ map[string]any) { s.found = false }},
		{"disabled binding", func(s *fakeJWTPrincipalStore, _ map[string]any) { s.principal.Enabled = false }},
		{"wrong binding subject", func(s *fakeJWTPrincipalStore, _ map[string]any) { s.principal.Subject = "other" }},
		{"wrong team claim", func(_ *fakeJWTPrincipalStore, c map[string]any) { c["team_id"] = "other-team" }},
		{"wrong audience", func(_ *fakeJWTPrincipalStore, c map[string]any) { c["aud"] = "openwebui" }},
		{"wrong issuer", func(_ *fakeJWTPrincipalStore, c map[string]any) { c["iss"] = "https://foreign.example" }},
		{"expired", func(_ *fakeJWTPrincipalStore, c map[string]any) { c["exp"] = 800 }},
		{"control in subject", func(_ *fakeJWTPrincipalStore, c map[string]any) { c["sub"] = "subject\x00extra" }},
		{"roles from wrong claim", func(_ *fakeJWTPrincipalStore, c map[string]any) {
			delete(c, "resource_access")
			c["roles"] = []string{"gateway-user"}
		}},
		{"unapproved administrator", func(_ *fakeJWTPrincipalStore, c map[string]any) {
			c["resource_access"] = map[string]any{"gateway": map[string]any{"roles": []string{"gateway-admin"}}}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			module, store, claims := directoryJWTModule(t)
			test.mutate(store, claims)
			req := RequestContext{APIKey: testJWT(t, "test-signing-secret", claims)}
			if err := module.Handle(context.Background(), &req); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("expected authorization rejection, got %v", err)
			}
			if req.UserID != "" || req.CredentialID != "" {
				t.Fatal("failed authorization published a partial principal")
			}
		})
	}
}

func TestDirectoryJWTDependencyFailuresFailClosed(t *testing.T) {
	module, store, claims := directoryJWTModule(t)
	store.err = errors.New("directory offline")
	if err := module.Handle(context.Background(), &RequestContext{APIKey: testJWT(t, "test-signing-secret", claims)}); !errors.Is(err, ErrJWTDirectoryUnavailable) {
		t.Fatalf("directory failure was hidden: %v", err)
	}
	module.store = nil
	if err := module.Ready(context.Background()); !errors.Is(err, ErrJWTDirectoryUnavailable) {
		t.Fatalf("missing directory was ready: %v", err)
	}
}

func TestJWTPrincipalScopesAreSeparate(t *testing.T) {
	base := jwtPrincipalCredentialID("issuer", "audience", "subject")
	for _, scope := range [][3]string{{"other", "audience", "subject"}, {"issuer", "other", "subject"}, {"issuer", "audience", "other"}} {
		if base == jwtPrincipalCredentialID(scope[0], scope[1], scope[2]) {
			t.Fatal("different identity scopes collided")
		}
	}
}

func TestDirectoryJWTConfigurationRejectsUnsafeDefaults(t *testing.T) {
	for _, config := range []JWTAuthConfig{
		{IdentityMode: "typo"},
		{IdentityMode: "directory"},
		{IdentityMode: "directory", Issuer: "issuer", Audience: "audience"},
		{IdentityMode: "directory", Issuer: "issuer", Audience: "audience", RoleMappings: map[string]string{"role": "superuser"}},
	} {
		if _, err := newJWTVerifier(config); err == nil {
			t.Fatal("unsafe directory config was accepted")
		}
	}
	t.Setenv("AUTH_JWT_ROLE_MAPPINGS_JSON", "invalid-json")
	if _, err := newJWTVerifier(JWTAuthConfigFromEnv()); err == nil {
		t.Fatal("invalid role mappings silently fell back")
	}
}
