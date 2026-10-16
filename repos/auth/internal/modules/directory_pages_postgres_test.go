package modules

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func directoryPostgresTestStore(t *testing.T) *PostgresVirtualKeyStore {
	t.Helper()
	store := ssoSessionPostgresStore(t)
	for _, name := range []string{"003_virtual_keys.sql", "006_identity_directory.sql", "010_scim_users.sql", "011_scim_user_deletions.sql", "012_scim_groups.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(t.Context(), string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestPostgresDirectorySearchPagingSortAndScopeIntegration(t *testing.T) {
	store := directoryPostgresTestStore(t)
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO users(id,name,email,status,roles)
		SELECT 'user-'||lpad(i::text,3,'0'),'Engineer '||i,'u'||i||'@example.test','active',ARRAY['user'] FROM generate_series(1,130) i`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutTeam(t.Context(), DirectoryTeam{ID: "a", Name: "Platform %_", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutTeam(t.Context(), DirectoryTeam{ID: "b", Name: "Other platform", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutMembership(t.Context(), TeamMembership{TeamID: "a", UserID: "user-101", Roles: []string{"member"}}); err != nil {
		t.Fatal(err)
	}
	module := AuthModule{store: store}
	users, total, err := module.ListDirectoryUsers(t.Context(), "", 100, 25, false, "id", "ascending", DirectoryFilter{Search: "ENGINEER", Status: "active", Role: "user"})
	if err != nil || total != 130 || len(users) != 25 || users[0].ID != "user-101" || users[24].ID != "user-125" {
		t.Fatalf("page: rows=%d total=%d err=%v", len(users), total, err)
	}
	users, total, err = module.ListDirectoryUsers(t.Context(), "a", 0, 25, false, "id", "ascending", DirectoryFilter{Search: "engineer"})
	if err != nil || total != 1 || len(users) != 1 || users[0].ID != "user-101" {
		t.Fatal("filtered listing escaped team scope", err)
	}
	users, total, err = module.ListDirectoryUsers(t.Context(), "a", 100, 25, false, "createdAt", "descending", DirectoryFilter{})
	if err != nil || total != 1 || len(users) != 0 {
		t.Fatal("empty page lost total", err)
	}
	_, total, err = module.ListDirectoryUsers(t.Context(), "", 0, 25, false, "userName", "descending", DirectoryFilter{Role: "admin"})
	if err != nil || total != 0 {
		t.Fatal("role filter ignored", err)
	}
	teams, total, err := module.ListDirectoryTeams(t.Context(), "", 0, 25, false, "displayName", "ascending", DirectoryFilter{Search: "%_", Status: "active"})
	if err != nil || total != 1 || len(teams) != 1 || teams[0].ID != "a" || teams[0].MemberCount != 1 {
		t.Fatal("literal team search, status or count lost", err)
	}
	_, total, err = module.ListDirectoryTeams(t.Context(), "b", 0, 25, false, "id", "ascending", DirectoryFilter{Search: "Platform", Status: "active"})
	if err != nil || total != 0 {
		t.Fatal("team filtering ignored scope/status", err)
	}
	if _, _, err := module.ListDirectoryTeams(t.Context(), "", 0, 25, false, "id", "ascending", DirectoryFilter{Role: "admin"}); !errors.Is(err, ErrInvalidDirectoryEntry) {
		t.Fatal("unsupported role filter silently ignored")
	}
	if _, err := store.CreateUser(t.Context(), DirectoryUser{ID: "user-101", Name: "Overwrite", Status: "disabled"}); !errors.Is(err, ErrDirectoryConflict) {
		t.Fatal("create overwrote an existing user", err)
	}
	saved, err := store.GetUser(t.Context(), "user-101")
	if err != nil || saved.Status != "active" || saved.Name == "Overwrite" {
		t.Fatal("conflicting create changed user", err)
	}
	if _, _, err := store.SaveTeamWithMembers(t.Context(), DirectoryTeam{ID: "a", Name: "Overwrite", Status: "disabled"}, nil, true); !errors.Is(err, ErrDirectoryConflict) {
		t.Fatal("create overwrote an existing team", err)
	}
}
