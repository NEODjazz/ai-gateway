package modules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectorySummaryQueryValidation(t *testing.T) {
	for _, q := range []DirectorySummaryQuery{{ScopeType: "global", IDs: []string{"a"}}, {ScopeType: "team"}, {ScopeType: "user", IDs: []string{"a", "a"}}, {ScopeType: "user", IDs: []string{"bad/id"}}, {ScopeType: "team", IDs: make([]string, 201)}} {
		if q.Valid() {
			t.Fatalf("invalid query accepted: %+v", q)
		}
	}
	if !(DirectorySummaryQuery{ScopeType: "team", IDs: []string{"a", "b"}}).Valid() {
		t.Fatal("valid query rejected")
	}
}

func TestPostgresDirectorySummariesExactCountsAndOwnersIntegration(t *testing.T) {
	s := directoryPostgresTestStore(t)
	ctx := t.Context()
	data, err := os.ReadFile(filepath.Join("..", "..", "migrations", "postgres", "007_organizations.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, string(data)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO users(id,name,status) VALUES('alice','Alice','active'),('bob','Bob','active'),('foreign','Foreign','active');
	 INSERT INTO auth_teams(id,name,status) SELECT 'team-'||i,'Team '||i,'active' FROM generate_series(1,620)i;
	 INSERT INTO auth_organizations(id,name,status) VALUES('owner-a','Owner A','active'),('owner-b','Owner B','disabled');
	 INSERT INTO auth_organization_teams(organization_id,team_id) VALUES('owner-a','team-620'),('owner-b','team-2');
	 INSERT INTO auth_team_memberships(team_id,user_id,roles) VALUES('team-620','alice',ARRAY['member']),('team-620','bob',ARRAY['member']),('team-2','bob',ARRAY['member']);
	 INSERT INTO auth_virtual_keys(id,token_hash,user_id,team_id,rotation_family_id,revoked_at) VALUES
	 ('key-a',repeat('a',64),'alice','team-620','family-a',NULL),('key-b',repeat('b',64),'alice','team-2','family-b',NULL),
	 ('key-c',repeat('c',64),'foreign','team-2','family-c',NULL),('key-d',repeat('d',64),'bob','team-620','family-d',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO auth_virtual_keys(id,token_hash,user_id,team_id,rotation_family_id) SELECT 'extra-'||i,lpad(i::text,64,'0'),'alice','team-620','extra-family-'||i FROM generate_series(1,1500)i`); err != nil {
		t.Fatal(err)
	}
	m := AuthModule{store: s}
	rows, err := m.DirectorySummaries(ctx, DirectorySummaryQuery{ScopeType: "team", IDs: []string{"team-620", "team-3", "unknown"}})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	byID := map[string]DirectorySummary{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	late, empty := byID["team-620"], byID["team-3"]
	if late.KeyCount != 1503 || late.Organization == nil || late.Organization.ID != "owner-a" || empty.KeyCount != 0 || empty.Organization != nil {
		t.Fatalf("incorrect ownership or key totals: %+v", byID)
	}
	encoded, err := json.Marshal(rows)
	if err != nil || strings.Contains(string(encoded), "token_hash") || strings.Contains(string(encoded), "key-a") || strings.Contains(string(encoded), "owner-b") || strings.Contains(string(encoded), "team_ids") {
		t.Fatal("summary exposed key inventory or unrelated ownership", err)
	}
	users, err := m.DirectorySummaries(ctx, DirectorySummaryQuery{ScopeType: "user", IDs: []string{"alice", "bob"}})
	if err != nil || len(users) != 2 || users[0].ID != "alice" || users[0].KeyCount != 1502 || users[1].ID != "bob" || users[1].KeyCount != 1 || users[0].Organization != nil {
		t.Fatalf("incorrect user scope: %+v err=%v", users, err)
	}
}
