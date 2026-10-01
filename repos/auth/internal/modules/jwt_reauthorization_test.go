package modules

import (
	"errors"
	"testing"
)

func TestJWTJobReauthorizationUsesCurrentDirectory(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*AuthModule, *fakeJWTPrincipalStore, *RequestContext)
		want   error
	}{
		{"active", func(*AuthModule, *fakeJWTPrincipalStore, *RequestContext) {}, nil},
		{"disabled", func(_ *AuthModule, s *fakeJWTPrincipalStore, _ *RequestContext) { s.principal.Enabled = false }, ErrUnauthorized},
		{"grants changed", func(_ *AuthModule, s *fakeJWTPrincipalStore, _ *RequestContext) {
			s.principal.AllowedModels = []string{"other"}
		}, ErrUnauthorized},
		{"limits changed", func(_ *AuthModule, s *fakeJWTPrincipalStore, _ *RequestContext) { s.principal.RateLimitRPM++ }, ErrUnauthorized},
		{"membership changed", func(_ *AuthModule, s *fakeJWTPrincipalStore, _ *RequestContext) { s.principal.TeamID = "other" }, ErrUnauthorized},
		{"roles revoked", func(_ *AuthModule, s *fakeJWTPrincipalStore, _ *RequestContext) { s.principal.Roles = nil }, ErrUnauthorized},
		{"mapping revoked", func(m *AuthModule, _ *fakeJWTPrincipalStore, _ *RequestContext) {
			m.jwtConfig.RoleMappings = map[string]string{}
		}, ErrUnauthorized},
		{"wrong owner", func(_ *AuthModule, _ *fakeJWTPrincipalStore, r *RequestContext) { r.UserID = "other" }, ErrUnauthorized},
		{"no reference", func(_ *AuthModule, _ *fakeJWTPrincipalStore, r *RequestContext) { r.JWTIdentity = nil }, ErrUnauthorized},
		{"outage", func(_ *AuthModule, s *fakeJWTPrincipalStore, _ *RequestContext) {
			s.err = errors.New("database failure")
		}, ErrJWTDirectoryUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, s, claims := directoryJWTModule(t)
			r := RequestContext{APIKey: testJWT(t, "test-signing-secret", claims)}
			if err := m.Handle(t.Context(), &r); err != nil {
				t.Fatal(err)
			}
			test.mutate(&m, s, &r)
			if err := m.ReauthorizeJWTPrincipal(t.Context(), &r); !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
		})
	}
}
