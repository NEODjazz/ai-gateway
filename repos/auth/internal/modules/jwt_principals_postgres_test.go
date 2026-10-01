package modules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresJWTPrincipalDirectoryIntegration(t *testing.T) {
	dsn := os.Getenv("AUTH_POSTGRES_TEST_DSN")
	if dsn == "" {
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("AUTH_POSTGRES_TEST_DSN is required")
		}
		t.Skip("AUTH_POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrations, err := filepath.Glob(filepath.Join("..", "..", "migrations", "postgres", "*.sql"))
	if err != nil || len(migrations) == 0 {
		t.Fatal("auth migrations unavailable")
	}
	for _, path := range migrations {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewPostgresVirtualKeyStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.JWTPrincipalsReady(ctx); err != nil {
		t.Fatal(err)
	}
	module, _, claims := directoryJWTModule(t)
	module.store = store
	userID := "jwt-user-" + time.Now().UTC().Format("20060102150405.000000000")
	teamID, orgID := userID+"-team", userID+"-org"
	claims["sub"] = userID
	query := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	query(`INSERT INTO users(id,status,roles) VALUES($1,'active',ARRAY['user'])`, userID)
	query(`INSERT INTO auth_teams(id,name,status) VALUES($1,$1,'active')`, teamID)
	query(`INSERT INTO auth_organizations(id,name,status) VALUES($1,$1,'active')`, orgID)
	query(`INSERT INTO auth_team_memberships(team_id,user_id) VALUES($1,$2)`, teamID, userID)
	query(`INSERT INTO auth_organization_teams(organization_id,team_id) VALUES($1,$2)`, orgID, teamID)
	query(`INSERT INTO auth_jwt_principals(issuer,subject,audience,user_id,team_id,allowed_models,allowed_tools,rate_limit_rpm)
		VALUES($1,$2,$3,$2,$4,ARRAY['model-a'],ARRAY['read'],4)`, module.jwtConfig.Issuer, userID, module.jwtConfig.Audience, teamID)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, sql := range []string{`DELETE FROM auth_jwt_principals WHERE user_id=$1`, `DELETE FROM auth_teams WHERE id=$1||'-team'`, `DELETE FROM auth_organizations WHERE id=$1||'-org'`, `DELETE FROM users WHERE id=$1`} {
			if _, err := pool.Exec(cleanup, sql, userID); err != nil {
				t.Error(err)
			}
		}
	})
	authorize := func() (RequestContext, error) {
		req := RequestContext{APIKey: testJWT(t, "test-signing-secret", claims)}
		err := module.Handle(ctx, &req)
		return req, err
	}

	// Management updates cannot change an existing principal's directory owner.
	policy := JWTPrincipalPolicy{Issuer: module.jwtConfig.Issuer, Subject: userID, Audience: module.jwtConfig.Audience, UserID: userID, TeamID: teamID, AllowedModels: []string{"model-a"}, AllowedTools: []string{"read"}, RateLimitRPM: 4, Enabled: true}
	if _, err := module.PutJWTPrincipal(ctx, policy); err != nil {
		t.Fatal(err)
	}
	page, err := module.ListJWTPrincipals(ctx, userID, 0, 1)
	if err != nil || page.Total != 1 || len(page.Data) != 1 {
		t.Fatalf("principal listing failed: %v", err)
	}
	page, err = module.ListJWTPrincipals(ctx, userID, 1, 1)
	if err != nil || page.Total != 1 || len(page.Data) != 0 {
		t.Fatalf("principal total changed with offset: %v", err)
	}
	query(`INSERT INTO users(id,status,roles) VALUES($1,'active',ARRAY['user'])`, userID+"-other")
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID+"-other")
		if err != nil {
			t.Error(err)
		}
	})
	changed := policy
	changed.UserID = userID + "-other"
	if _, err := module.PutJWTPrincipal(ctx, changed); !errors.Is(err, ErrDirectoryConflict) {
		t.Fatalf("owner reassignment accepted: %v", err)
	}
	policy.Enabled = false
	if _, err := module.PutJWTPrincipal(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := module.PutJWTPrincipal(ctx, changed); !errors.Is(err, ErrDirectoryConflict) {
		t.Fatalf("disabled owner reassignment accepted: %v", err)
	}
	policy.Enabled = true
	if _, err := module.PutJWTPrincipal(ctx, policy); err != nil {
		t.Fatal(err)
	}
	first, err := authorize()
	if err != nil || first.UserID != userID || first.TeamID != teamID || first.OrganizationID != orgID || first.RateLimitRPM != 4 || len(first.AllowedModels) != 1 {
		t.Fatalf("directory binding was not applied: %v", err)
	}
	if err := module.ReauthorizeJWTPrincipal(ctx, &first); err != nil {
		t.Fatal(err)
	}
	claims["exp"], claims["jti"] = 3000, "refresh"
	query(`UPDATE auth_jwt_principals SET allowed_models=ARRAY['model-b'],rate_limit_rpm=2 WHERE user_id=$1`, userID)
	if err := module.ReauthorizeJWTPrincipal(ctx, &first); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("queued stale grants authorized: %v", err)
	}
	second, err := authorize()
	if err != nil || second.CredentialID != first.CredentialID || second.UserID != first.UserID || second.RateLimitRPM != 2 || len(second.AllowedModels) != 1 || second.AllowedModels[0] != "model-b" {
		t.Fatalf("refresh retained stale policy or changed identity: %v", err)
	}
	for _, sql := range []string{
		`UPDATE users SET status='disabled' WHERE id=$1`,
		`UPDATE users SET scim_deleted_at=now() WHERE id=$1`,
		`UPDATE auth_teams SET status='disabled' WHERE id=$1||'-team'`,
		`UPDATE auth_teams SET scim_deleted_at=now() WHERE id=$1||'-team'`,
		`UPDATE auth_organizations SET status='disabled' WHERE id=$1||'-org'`,
		`DELETE FROM auth_team_memberships WHERE user_id=$1`,
		`UPDATE auth_jwt_principals SET enabled=false WHERE user_id=$1`,
	} {
		query(sql, userID)
		if err := module.ReauthorizeJWTPrincipal(ctx, &second); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("disabled queued principal retained authorization: %v", err)
		}
		if _, err := authorize(); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("inactive directory relationship retained JWT access: %v", err)
		}
		query(`UPDATE users SET status='active',scim_deleted_at=NULL WHERE id=$1`, userID)
		query(`UPDATE auth_teams SET status='active',scim_deleted_at=NULL WHERE id=$1||'-team'`, userID)
		query(`UPDATE auth_organizations SET status='active' WHERE id=$1||'-org'`, userID)
		query(`INSERT INTO auth_team_memberships(team_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, teamID, userID)
		query(`UPDATE auth_jwt_principals SET enabled=true WHERE user_id=$1`, userID)
	}
	if _, err := authorize(); err != nil {
		t.Fatalf("restored directory relationship could not authenticate: %v", err)
	}
}
