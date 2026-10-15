package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOrganizationPageQueryValidation(t *testing.T) {
	for _, q := range []OrganizationPageQuery{{Limit: 0}, {Limit: 101}, {Limit: 25, Offset: -1}, {Limit: 25, Status: "bad"}, {Limit: 25, Search: "bad\x00"}} {
		if q.Valid() {
			t.Fatalf("invalid page accepted: %+v", q)
		}
	}
	if !(OrganizationPageQuery{Limit: 25, Search: "%_", Status: "active"}).Valid() {
		t.Fatal("literal search rejected")
	}
}

func TestPostgresTeamOrganizationPagingCreationAndIsolationIntegration(t *testing.T) {
	s := directoryPostgresTestStore(t)
	ctx := t.Context()
	for _, name := range []string{"004_allowed_tools.sql", "005_virtual_key_metadata.sql", "007_organizations.sql", "008_virtual_key_ownership.sql", "009_virtual_key_access_groups.sql", "013_jwt_principals.sql", "016_organization_memberships.sql"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", "postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO auth_organizations(id,name,status,created_at)
	 SELECT 'org-'||lpad(i::text,4,'0'),'Organization '||lpad(i::text,4,'0'),CASE WHEN i=620 THEN 'disabled' ELSE 'active' END,'2026-10-01'::timestamptz FROM generate_series(1,620)i;
	 INSERT INTO auth_organizations(id,name,status,created_at) VALUES('oldest','Legacy %_ owner','active','2026-01-01');
	 INSERT INTO auth_teams(id,name,status) VALUES('team','Team','active'),('legacy','Legacy','active'),('race','Race','active')`); err != nil {
		t.Fatal(err)
	}
	m := AuthModule{store: s}
	page, err := m.OrganizationPage(ctx, OrganizationPageQuery{Status: "active", Offset: 500, Limit: 25})
	if err != nil || page.Total != 620 || len(page.Data) != 25 || page.Offset != 500 || page.Limit != 25 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	for _, item := range page.Data {
		if item.Status != "active" {
			t.Fatal("status filter ignored")
		}
	}
	page, err = m.OrganizationPage(ctx, OrganizationPageQuery{Search: "%_", Limit: 25})
	if err != nil || page.Total != 1 || page.Data[0].ID != "oldest" {
		t.Fatal("literal search ignored", err)
	}
	page, err = m.OrganizationPage(ctx, OrganizationPageQuery{Search: "ORGANIZATION 0619", Limit: 25})
	if err != nil || page.Total != 1 || page.Data[0].ID != "org-0619" {
		t.Fatal("late organization cannot be selected", err)
	}
	page, err = m.OrganizationPage(ctx, OrganizationPageQuery{Offset: 1000, Limit: 25})
	if err != nil || page.Total != 621 || len(page.Data) != 0 {
		t.Fatal("empty page lost count", err)
	}
	if org, err := s.PutOrganizationTeam(ctx, "oldest", "legacy"); err != nil || org.ID != "oldest" || len(org.TeamIDs) != 1 {
		t.Fatal("legacy assignment still depends on the first 500 organizations", err)
	}
	owner, err := m.TeamOrganization(ctx, "team")
	if err != nil || owner.Organization != nil || owner.TeamID != "team" {
		t.Fatal("unassigned team is not explicit", err)
	}
	owner, err = m.ChangeTeamOrganization(ctx, "team", "oldest", false)
	if err != nil || owner.Organization == nil || owner.Organization.ID != "oldest" {
		t.Fatal("assignment failed", err)
	}
	data, _ := json.Marshal(owner)
	if strings.Contains(string(data), "team_ids") || strings.Contains(string(data), "legacy") {
		t.Fatal("owner read leaked unrelated teams")
	}
	if _, err = m.ChangeTeamOrganization(ctx, "team", "org-0619", false); !errors.Is(err, ErrOrganizationTeamConflict) {
		t.Fatal("implicit re-parenting accepted", err)
	}
	if _, err = m.ChangeTeamOrganization(ctx, "team", "org-0619", true); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatal("stale unassignment removed another owner's team", err)
	}
	owner, err = m.TeamOrganization(ctx, "team")
	if err != nil || owner.Organization.ID != "oldest" {
		t.Fatal("failed mutation changed owner", err)
	}
	if err := s.Create(ctx, StoredVirtualKey{ID: "pinned", TeamID: "team", OrganizationID: "oldest"}, "ownership-pinned-fixture"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, StoredVirtualKey{ID: "unscoped", TeamID: "team"}, "ownership-unscoped-fixture"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Lookup(ctx, "ownership-pinned-fixture"); err != nil || !ok {
		t.Fatal("valid scoped key rejected", err)
	}
	owner, err = m.ChangeTeamOrganization(ctx, "team", "oldest", true)
	if err != nil || owner.Organization != nil {
		t.Fatal("unassignment failed", err)
	}
	if _, err = m.ChangeTeamOrganization(ctx, "team", "org-0619", false); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Lookup(ctx, "ownership-pinned-fixture"); err != nil || ok {
		t.Fatal("old organization's key was silently migrated", err)
	}
	if key, ok, err := s.Lookup(ctx, "ownership-unscoped-fixture"); err != nil || !ok || key.OrganizationID != "" {
		t.Fatal("key acquired an implicit organization", err)
	}
	for _, orgID := range []string{"missing", "org-0620"} {
		id := "failed-" + orgID
		_, err = m.CreateTeamInOrganization(ctx, DirectoryTeam{ID: id, Name: "Atomic", Status: "active"}, orgID)
		if err == nil {
			t.Fatal("creation accepted unavailable organization")
		}
		var exists bool
		if err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_teams WHERE id=$1)`, id).Scan(&exists); err != nil || exists {
			t.Fatal("failed assignment left a partially created team", err)
		}
	}
	created, err := m.CreateTeamInOrganization(ctx, DirectoryTeam{ID: "new-team", Name: "New", Status: "disabled"}, "org-0619")
	if err != nil || created.ID != "new-team" || created.Status != "disabled" {
		t.Fatal("atomic creation failed", err)
	}
	owner, err = m.TeamOrganization(ctx, "new-team")
	if err != nil || owner.Organization == nil || owner.Organization.ID != "org-0619" {
		t.Fatal("creation lost organization", err)
	}
	if _, err = m.CreateTeamInOrganization(ctx, DirectoryTeam{ID: "new-team", Name: "Overwrite", Status: "active"}, "oldest"); !errors.Is(err, ErrDirectoryConflict) {
		t.Fatal("creation overwrote existing team", err)
	}
	owner, _ = m.TeamOrganization(ctx, "new-team")
	if owner.Organization.ID != "org-0619" {
		t.Fatal("duplicate create moved team")
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.ChangeTeamOrganization(ctx, "race", fmt.Sprintf("org-%04d", i+1), false)
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrOrganizationTeamConflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("concurrent assignments did not elect one owner", winners.Load())
	}
}
