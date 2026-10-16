package modules

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPostgresOrganizationMembershipAndImmutablePrincipalTenant(t *testing.T) {
	store := ssoSessionPostgresStore(t)
	paths, err := filepath.Glob(filepath.Join("..", "..", "migrations", "postgres", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.pool.Exec(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	m, _, claims := directoryJWTModule(t)
	m.store = store
	m.jwtConfig.RoleMappings["organization-admin"] = "org_admin"
	claims["resource_access"] = map[string]any{"gateway": map[string]any{"roles": []string{"organization-admin"}}}
	if _, err := store.PutUser(t.Context(), DirectoryUser{ID: "user-a", Status: "active", Roles: []string{"org_admin", "user"}}); err != nil {
		t.Fatal(err)
	}
	for _, org := range []string{"org-a", "org-b"} {
		if _, err := store.PutOrganization(t.Context(), Organization{ID: org, Name: org, Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	policy := JWTPrincipalPolicy{Issuer: m.jwtConfig.Issuer, Subject: "external-subject", Audience: m.jwtConfig.Audience, UserID: "user-a", OrganizationID: "org-a", Enabled: true, AllowedModels: []string{"model-a"}}
	if _, err = m.PutJWTPrincipal(t.Context(), policy); err != nil {
		t.Fatal(err)
	}
	authorize := func() (RequestContext, error) {
		req := RequestContext{APIKey: testJWT(t, "test-signing-secret", claims)}
		err := m.Handle(t.Context(), &req)
		return req, err
	}
	if _, err = authorize(); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("global directory role bypassed membership", err)
	}
	member := OrganizationMembership{OrganizationID: "org-b", UserID: "user-a", Roles: []string{"org_admin"}, Status: "active"}
	if _, err = m.PutOrganizationMembership(t.Context(), member); err != nil {
		t.Fatal(err)
	}
	if _, err = authorize(); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("other tenant approval accepted", err)
	}
	member.OrganizationID = "org-a"
	if _, err = m.PutOrganizationMembership(t.Context(), member); err != nil {
		t.Fatal(err)
	}
	first, err := authorize()
	if err != nil || first.OrganizationID != "org-a" {
		t.Fatal("approved tenant not applied", err)
	}
	if err = m.ReauthorizeJWTPrincipal(t.Context(), &first); err != nil {
		t.Fatal(err)
	}
	page, err := m.ListOrganizationMemberships(t.Context(), "org-a", 1, 1)
	if err != nil || page.Total != 1 || len(page.Data) != 0 {
		t.Fatal("incorrect off-page total", err)
	}
	if _, err = m.ListOrganizationMemberships(t.Context(), "absent", 0, 10); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatal("unknown organization accepted", err)
	}
	changed := policy
	changed.OrganizationID = "org-b"
	if _, err = m.PutJWTPrincipal(t.Context(), changed); !errors.Is(err, ErrDirectoryConflict) {
		t.Fatal("principal tenant changed", err)
	}
	policy.Enabled = false
	if _, err = m.PutJWTPrincipal(t.Context(), policy); err != nil {
		t.Fatal(err)
	}
	if _, err = m.PutJWTPrincipal(t.Context(), changed); !errors.Is(err, ErrDirectoryConflict) {
		t.Fatal("disabled principal tenant changed", err)
	}
	policy.Enabled = true
	if _, err = m.PutJWTPrincipal(t.Context(), policy); err != nil {
		t.Fatal(err)
	}
	member.Status = "disabled"
	if _, err = m.PutOrganizationMembership(t.Context(), member); err != nil {
		t.Fatal(err)
	}
	if _, err = authorize(); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled membership authorized", err)
	}
	if err = m.ReauthorizeJWTPrincipal(t.Context(), &first); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked org job authorized", err)
	}
	member.Status = "active"
	member.Roles = []string{"user"}
	if _, err = m.PutOrganizationMembership(t.Context(), member); err != nil {
		t.Fatal(err)
	}
	if _, err = authorize(); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked admin role authorized", err)
	}
	member.Roles = []string{"org_admin"}
	if _, err = m.PutOrganizationMembership(t.Context(), member); err != nil {
		t.Fatal(err)
	}
	key := StoredVirtualKey{ID: "org-key", UserID: "user-a", OrganizationID: "org-a", Roles: []string{"org_admin"}}
	if err = store.Create(t.Context(), key, "fixture-token-hash"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Lookup(t.Context(), "fixture-token-hash"); err != nil || !found {
		t.Fatal("approved organization key denied", err)
	}
	member.Status = "disabled"
	if _, err = m.PutOrganizationMembership(t.Context(), member); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Lookup(t.Context(), "fixture-token-hash"); err != nil || found {
		t.Fatal("revoked organization key authorized", err)
	}
	member.Status = "active"
	if _, err = m.PutOrganizationMembership(t.Context(), member); err != nil {
		t.Fatal(err)
	}
	for _, binding := range [][2]string{{"team-a", "org-a"}, {"team-b", "org-b"}} {
		if _, err = store.PutTeam(t.Context(), DirectoryTeam{ID: binding[0], Name: binding[0], Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if _, err = store.PutOrganizationTeam(t.Context(), binding[1], binding[0]); err != nil {
			t.Fatal(err)
		}
		if _, err = store.PutMembership(t.Context(), TeamMembership{TeamID: binding[0], UserID: "user-a"}); err != nil {
			t.Fatal(err)
		}
	}
	teamPolicy := policy
	teamPolicy.Audience = "team-client"
	teamPolicy.TeamID = "team-a"
	teamPolicy.OrganizationID = ""
	saved, err := m.PutJWTPrincipal(t.Context(), teamPolicy)
	if err != nil || saved.OrganizationID != "org-a" {
		t.Fatal("team tenant was not pinned", err)
	}
	teamPolicy.TeamID = "team-b"
	if _, err = m.PutJWTPrincipal(t.Context(), teamPolicy); !errors.Is(err, ErrDirectoryConflict) {
		t.Fatal("team change transferred principal tenant", err)
	}
	if _, err = store.DeleteOrganizationTeam(t.Context(), "org-a", "team-a"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LookupJWTPrincipal(t.Context(), saved.Issuer, saved.Subject, saved.Audience); err != nil || found {
		t.Fatal("detached team changed principal into unscoped identity", err)
	}
	if _, err = store.PutOrganizationTeam(t.Context(), "org-b", "team-a"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LookupJWTPrincipal(t.Context(), saved.Issuer, saved.Subject, saved.Audience); err != nil || found {
		t.Fatal("reassigned team changed principal tenant", err)
	}
}

func TestPostgresPrincipalTenantMigrationBackfillsOnce(t *testing.T) {
	store := ssoSessionPostgresStore(t)
	paths, err := filepath.Glob(filepath.Join("..", "..", "migrations", "postgres", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if filepath.Base(path) == "016_organization_memberships.sql" {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.pool.Exec(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	query := func(sql string, args ...any) {
		t.Helper()
		if _, err := store.pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	query(`INSERT INTO users(id,status,roles) VALUES('legacy-user','active',ARRAY['user'])`)
	query(`INSERT INTO auth_teams(id,name,status) VALUES('legacy-team','Team','active')`)
	query(`INSERT INTO auth_organizations(id,name,status) VALUES('legacy-org','Organization','active')`)
	query(`INSERT INTO auth_organization_teams(organization_id,team_id) VALUES('legacy-org','legacy-team')`)
	query(`INSERT INTO auth_jwt_principals(issuer,subject,audience,user_id,team_id) VALUES('issuer','subject','legacy','legacy-user','legacy-team')`)
	path := filepath.Join("..", "..", "migrations", "postgres", "016_organization_memberships.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	query(string(body))
	var org string
	if err = store.pool.QueryRow(t.Context(), `SELECT organization_id FROM auth_jwt_principals WHERE audience='legacy'`).Scan(&org); err != nil || org != "legacy-org" {
		t.Fatal("legacy tenant not pinned", err)
	}
	query(`INSERT INTO auth_jwt_principals(issuer,subject,audience,user_id,team_id) VALUES('issuer','subject','unscoped','legacy-user','legacy-team')`)
	query(string(body))
	if err = store.pool.QueryRow(t.Context(), `SELECT COALESCE(organization_id,'') FROM auth_jwt_principals WHERE audience='unscoped'`).Scan(&org); err != nil || org != "" {
		t.Fatal("migration replay silently reassigned an identity", err)
	}
	if _, found, err := store.LookupJWTPrincipal(t.Context(), "issuer", "subject", "unscoped"); err != nil || found {
		t.Fatal("unscoped binding adopted a tenant", err)
	}
}
