package modules

import (
	"context"
	"errors"
	"testing"
)

type principalManagementStub struct {
	fakeJWTPrincipalStore
	saved         JWTPrincipalPolicy
	offset, limit int
}

func (s *principalManagementStub) PutJWTPrincipal(_ context.Context, p JWTPrincipalPolicy) (JWTPrincipalPolicy, error) {
	s.saved = p
	return p, nil
}
func (s *principalManagementStub) ListJWTPrincipals(_ context.Context, user string, offset, limit int) (JWTPrincipalPage, error) {
	s.offset, s.limit = offset, limit
	return JWTPrincipalPage{Data: []JWTPrincipalPolicy{s.saved}, Total: 9, Offset: offset, Limit: limit}, nil
}

func TestJWTPrincipalProvisioningValidation(t *testing.T) {
	m, _, _ := directoryJWTModule(t)
	store := &principalManagementStub{}
	m.store = store
	valid := JWTPrincipalPolicy{Issuer: m.jwtConfig.Issuer, Subject: "subject", Audience: m.jwtConfig.Audience, UserID: "user", Enabled: true}
	for _, test := range []struct {
		name   string
		change func(*JWTPrincipalPolicy)
	}{
		{"missing issuer", func(p *JWTPrincipalPolicy) { p.Issuer = "" }},
		{"control subject", func(p *JWTPrincipalPolicy) { p.Subject = "a\x00b" }},
		{"ambiguous audience", func(p *JWTPrincipalPolicy) { p.Audience = " gateway" }},
		{"missing user", func(p *JWTPrincipalPolicy) { p.UserID = "" }},
		{"invalid group", func(p *JWTPrincipalPolicy) { p.AccessGroupIDs = []string{"../admin"} }},
		{"negative rpm", func(p *JWTPrincipalPolicy) { p.RateLimitRPM = -1 }},
		{"overflow tpm", func(p *JWTPrincipalPolicy) { p.RateLimitTPM = 2147483648 }},
		{"control grant", func(p *JWTPrincipalPolicy) { p.AllowedModels = []string{"model\n"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := valid
			test.change(&p)
			if _, err := m.PutJWTPrincipal(t.Context(), p); !errors.Is(err, ErrInvalidDirectoryEntry) {
				t.Fatalf("accepted invalid policy: %v", err)
			}
		})
	}
	for _, grants := range [][]string{nil, {}, {"*"}, {"model-a"}} {
		p := valid
		p.AllowedModels = grants
		if _, err := m.PutJWTPrincipal(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	page, err := m.ListJWTPrincipals(t.Context(), "user", 10, 5)
	if err != nil || page.Total != 9 || store.offset != 10 || store.limit != 5 {
		t.Fatalf("pagination failed: %v", err)
	}
	if _, err = m.ListJWTPrincipals(t.Context(), "", 0, 501); !errors.Is(err, ErrInvalidDirectoryEntry) {
		t.Fatal("unbounded listing accepted")
	}
}
