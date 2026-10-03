package modules

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPostgresOrganizationKeysUseOwnedScopeAndImmutableIdentity(t *testing.T) {
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
	if _, err = store.PutUser(t.Context(), DirectoryUser{ID: "shared-user", Status: "active", Roles: []string{"user"}}); err != nil {
		t.Fatal(err)
	}
	for _, org := range []string{"org-a", "org-b"} {
		if _, err = store.PutOrganization(t.Context(), Organization{ID: org, Name: org, Status: "active"}); err != nil {
			t.Fatal(err)
		}
		team := org + "-team"
		if _, err = store.PutTeam(t.Context(), DirectoryTeam{ID: team, Name: team, Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if _, err = store.PutOrganizationTeam(t.Context(), org, team); err != nil {
			t.Fatal(err)
		}
		if _, err = store.PutMembership(t.Context(), TeamMembership{TeamID: team, UserID: "shared-user"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []StoredVirtualKey{
		{ID: "key-a", UserID: "shared-user", OrganizationID: "org-a", TeamID: "org-a-team", Roles: []string{"user"}},
		{ID: "key-b", UserID: "shared-user", OrganizationID: "org-b", TeamID: "org-b-team", Roles: []string{"user"}},
		{ID: "key-unscoped", UserID: "shared-user", Roles: []string{"user"}},
	} {
		if err = store.Create(t.Context(), key, key.ID+"-hash"); err != nil {
			t.Fatal(err)
		}
	}
	for _, offset := range []int{0, 1, 10} {
		page, err := store.ListPage(t.Context(), VirtualKeyListQuery{OrganizationID: "org-a", StrictOrganization: true, Limit: 1, Offset: offset, SortBy: "created", SortOrder: "asc"})
		if err != nil || page.Total != 1 {
			t.Fatal("tenant total included member's foreign or unscoped key", err, page.Total)
		}
		if offset == 0 {
			if len(page.Data) != 1 || page.Data[0].ID != "key-a" {
				t.Fatal("foreign/unscoped key metadata disclosed")
			}
		} else if len(page.Data) != 0 {
			t.Fatal("offset returned foreign key")
		}
	}
	key := StoredVirtualKey{ID: "key-a", UserID: "shared-user", OrganizationID: "org-a", TeamID: "org-a-team", Alias: "updated"}
	if updated, err := store.Update(t.Context(), key.ID, key); err != nil || !updated {
		t.Fatal("same-owner policy update denied", err)
	}
	for _, change := range []func(*StoredVirtualKey){func(k *StoredVirtualKey) { k.OrganizationID = "org-b"; k.TeamID = "org-b-team" }, func(k *StoredVirtualKey) { k.UserID = "other-user" }, func(k *StoredVirtualKey) { k.OrganizationID = "" }, func(k *StoredVirtualKey) { k.TeamID = "org-b-team" }} {
		bad := key
		change(&bad)
		if _, err = store.Update(t.Context(), key.ID, bad); !errors.Is(err, ErrInvalidVirtualKey) {
			t.Fatal("key ownership/context mutation accepted", err)
		}
		bad.ID = "replacement"
		if err = store.Rotate(t.Context(), key.ID, bad, "replacement-hash"); !errors.Is(err, ErrInvalidVirtualKey) {
			t.Fatal("rotation changed tenant identity", err)
		}
		if _, found, err := store.Lookup(t.Context(), "key-a-hash"); err != nil || !found {
			t.Fatal("failed rotation revoked existing key", err)
		}
	}
	bad := key
	bad.ID = "wrong-team"
	bad.TeamID = "org-b-team"
	if err = store.Create(t.Context(), bad, "wrong-team-hash"); !errors.Is(err, ErrInvalidVirtualKey) {
		t.Fatal("cross-tenant team creation accepted", err)
	}
	if _, err = store.pool.Exec(t.Context(), `UPDATE auth_virtual_keys SET team_id='org-b-team' WHERE id='key-a'`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Lookup(t.Context(), "key-a-hash"); err != nil || found {
		t.Fatal("legacy cross-tenant team key authenticated", err)
	}
}
