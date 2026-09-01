package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresVirtualKeyLifecycleIntegration(t *testing.T) {
	dsn := os.Getenv("AUTH_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("AUTH_POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, name := range []string{"003_virtual_keys.sql", "004_allowed_tools.sql", "005_virtual_key_metadata.sql", "006_identity_directory.sql", "007_organizations.sql", "008_virtual_key_ownership.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "postgres", name))
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
	defer store.Close()
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	oldID, newID, expiredID, organizationKeyID, memberKeyID := "key-old-"+suffix, "key-new-"+suffix, "key-expired-"+suffix, "key-org-"+suffix, "key-member-"+suffix
	directoryUserID, directoryTeamID, organizationID := "user-"+suffix, "team-"+suffix, "org-"+suffix
	t.Cleanup(func() {
		ids := []string{newID, oldID, expiredID, organizationKeyID, memberKeyID}
		_, _ = pool.Exec(context.Background(), `UPDATE auth_virtual_keys SET rotated_from_id=NULL,rotated_to_id=NULL WHERE id = ANY($1)`, ids)
		_, _ = pool.Exec(context.Background(), `DELETE FROM auth_virtual_keys WHERE id = ANY($1)`, ids)
		_, _ = pool.Exec(context.Background(), `DELETE FROM auth_organization_teams WHERE organization_id=$1`, organizationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM auth_organizations WHERE id=$1`, organizationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM auth_team_memberships WHERE team_id=$1`, directoryTeamID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM auth_teams WHERE id=$1`, directoryTeamID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, directoryUserID)
	})
	user, err := store.PutUser(ctx, DirectoryUser{ID: directoryUserID, Email: "owner@example.test", Name: "Owner", Status: "active", Roles: []string{"developer"}})
	if err != nil || user.Name != "Owner" {
		t.Fatalf("put user failed: user=%+v err=%v", user, err)
	}
	team, err := store.PutTeam(ctx, DirectoryTeam{ID: directoryTeamID, Name: "Platform", Status: "active"})
	if err != nil || team.Name != "Platform" {
		t.Fatalf("put team failed: team=%+v err=%v", team, err)
	}
	if _, err := store.PutMembership(ctx, TeamMembership{TeamID: directoryTeamID, UserID: directoryUserID, Roles: []string{"team_admin"}}); err != nil {
		t.Fatal(err)
	}
	users, err := store.ListUsers(ctx, directoryTeamID, 10)
	if err != nil || len(users) != 1 || users[0].ID != directoryUserID || len(users[0].TeamIDs) != 1 {
		t.Fatalf("scoped users=%+v err=%v", users, err)
	}
	teams, err := store.ListTeams(ctx, directoryTeamID, 10)
	if err != nil || len(teams) != 1 || teams[0].MemberCount != 1 {
		t.Fatalf("scoped teams=%+v err=%v", teams, err)
	}
	organization, err := store.PutOrganization(ctx, Organization{ID: organizationID, Name: "Acme", Status: "active"})
	if err != nil || organization.ID != organizationID {
		t.Fatalf("put organization=%+v err=%v", organization, err)
	}
	organization, err = store.PutOrganizationTeam(ctx, organizationID, directoryTeamID)
	if err != nil || len(organization.TeamIDs) != 1 || organization.TeamIDs[0] != directoryTeamID {
		t.Fatalf("assign organization team=%+v err=%v", organization, err)
	}
	organizationToken := "organization-token-" + suffix
	if err := store.Create(ctx, StoredVirtualKey{ID: organizationKeyID, OrganizationID: organizationID, Roles: []string{"developer"}}, credentialLookupHash(organizationToken, "pepper")); err != nil {
		t.Fatal(err)
	}
	if found, ok, err := store.Lookup(ctx, credentialLookupHash(organizationToken, "pepper")); err != nil || !ok || found.OrganizationID != organizationID || found.UserID != "" {
		t.Fatalf("organization-owned key lookup failed: key=%+v ok=%v err=%v", found, ok, err)
	}
	if err := store.Create(ctx, StoredVirtualKey{ID: memberKeyID, Alias: "platform-member", UserID: directoryUserID, Roles: []string{"developer"}}, credentialLookupHash("member-token-"+suffix, "pepper")); err != nil {
		t.Fatal(err)
	}
	for _, query := range []VirtualKeyListQuery{
		{Limit: 10, TeamID: directoryTeamID, Search: "platform", SortBy: "alias", SortOrder: "asc"},
		{Limit: 10, OrganizationID: organizationID, Search: "platform", SortBy: "created", SortOrder: "desc"},
	} {
		page, err := store.ListPage(ctx, query)
		if err != nil || page.Total != 1 || len(page.Data) != 1 || page.Data[0].ID != memberKeyID {
			t.Fatalf("membership-aware key page=%+v err=%v query=%+v", page, err, query)
		}
	}

	oldToken := "old-token-" + suffix
	old := StoredVirtualKey{ID: oldID, Alias: "automation", Description: "CI key", Tags: []string{"ci", "prod"}, UserID: "user-1", TeamID: "team-1", Roles: []string{"developer"}, AllowedModels: []string{"gpt-*"}, AllowedTools: []string{"mcp.weather.*"}, RateLimitRPM: 10}
	if err := store.Create(ctx, old, credentialLookupHash(oldToken, "pepper")); err != nil {
		t.Fatal(err)
	}
	if found, ok, err := store.Lookup(ctx, credentialLookupHash(oldToken, "pepper")); err != nil || !ok || found.ID != oldID || found.Alias != "automation" || len(found.Tags) != 2 || found.Tags[0] != "ci" || found.Tags[1] != "prod" || len(found.AllowedTools) != 1 {
		t.Fatalf("active key lookup failed: key=%+v ok=%v err=%v", found, ok, err)
	}
	old.RateLimitRPM = 15
	old.Description = "updated CI key"
	if updated, err := store.Update(ctx, oldID, old); err != nil || !updated {
		t.Fatalf("update failed: updated=%v err=%v", updated, err)
	}
	if disabled, err := store.SetDisabled(ctx, oldID, true); err != nil || !disabled {
		t.Fatalf("disable failed: disabled=%v err=%v", disabled, err)
	}
	if _, ok, err := store.Lookup(ctx, credentialLookupHash(oldToken, "pepper")); err != nil || ok {
		t.Fatalf("disabled key must not authorize: ok=%v err=%v", ok, err)
	}
	if enabled, err := store.SetDisabled(ctx, oldID, false); err != nil || !enabled {
		t.Fatalf("enable failed: enabled=%v err=%v", enabled, err)
	}
	if found, ok, err := store.Lookup(ctx, credentialLookupHash(oldToken, "pepper")); err != nil || !ok || found.RateLimitRPM != 15 {
		t.Fatalf("enabled updated key lookup failed: key=%+v ok=%v err=%v", found, ok, err)
	}

	expiredAt := time.Now().Add(-time.Minute)
	expiredToken := "expired-token-" + suffix
	if err := store.Create(ctx, StoredVirtualKey{ID: expiredID, UserID: "user-1", ExpiresAt: &expiredAt}, credentialLookupHash(expiredToken, "pepper")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Lookup(ctx, credentialLookupHash(expiredToken, "pepper")); err != nil || ok {
		t.Fatalf("expired key must not authorize: ok=%v err=%v", ok, err)
	}
	expiredPage, err := store.ListPage(ctx, VirtualKeyListQuery{Limit: 10, KeyID: expiredID, Status: "expired", SortBy: "status", SortOrder: "asc"})
	if err != nil || expiredPage.Total != 1 || len(expiredPage.Data) != 1 || expiredPage.Data[0].ID != expiredID {
		t.Fatalf("expired key filter page=%+v err=%v", expiredPage, err)
	}

	newToken := "new-token-" + suffix
	replacement := StoredVirtualKey{ID: newID, UserID: "user-1", TeamID: "team-1", Roles: []string{"developer"}, RateLimitRPM: 20}
	if err := store.Rotate(ctx, oldID, replacement, credentialLookupHash(newToken, "pepper")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Lookup(ctx, credentialLookupHash(oldToken, "pepper")); err != nil || ok {
		t.Fatalf("rotated key must be revoked: ok=%v err=%v", ok, err)
	}
	rotated, ok, err := store.Lookup(ctx, credentialLookupHash(newToken, "pepper"))
	if err != nil || !ok || rotated.ID != newID || rotated.RotatedFromID != oldID || rotated.RotationFamily != oldID {
		t.Fatalf("replacement key lookup failed: key=%+v ok=%v err=%v", rotated, ok, err)
	}
	if revoked, err := store.Revoke(ctx, newID); err != nil || !revoked {
		t.Fatalf("revoke failed: revoked=%v err=%v", revoked, err)
	}
	if _, ok, err := store.Lookup(ctx, credentialLookupHash(newToken, "pepper")); err != nil || ok {
		t.Fatalf("revoked key must not authorize: ok=%v err=%v", ok, err)
	}
	listed, err := store.List(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundMetadata := false
	for _, key := range listed {
		if key.ID == newID {
			foundMetadata = key.UserID == "user-1" && key.RevokedAt != nil && key.RotationFamily == oldID
		}
	}
	if !foundMetadata {
		t.Fatalf("safe lifecycle metadata for %q was not listed: %+v", newID, listed)
	}
}
