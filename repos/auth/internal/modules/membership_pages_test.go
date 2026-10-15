package modules

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMembershipQueryValidation(t *testing.T) {
	for _, q := range []MembershipQuery{{Limit: 25}, {TeamID: "bad/team", Limit: 25}, {UserID: "user", Limit: 501}, {TeamID: "team", Limit: 25, Search: "bad\x00"}, {TeamID: "team", Limit: 25, SortBy: "created; DROP TABLE users"}, {TeamID: "team", Limit: 25, SortOrder: "descending"}} {
		if q.valid() {
			t.Fatalf("invalid membership query accepted: %+v", q)
		}
	}
}

func TestPostgresMembershipPagesAndAtomicCreationIntegration(t *testing.T) {
	s := directoryPostgresTestStore(t)
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO users(id,name,email,status,roles) SELECT 'u-'||lpad(i::text,3,'0'),'Engineer '||i,'u'||i||'@example.test','active',ARRAY['user'] FROM generate_series(1,620) i`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := s.PutTeam(t.Context(), DirectoryTeam{ID: id, Name: "Team " + id, Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO auth_team_memberships(team_id,user_id,roles) SELECT 'a','u-'||lpad(i::text,3,'0'),ARRAY['developer'] FROM generate_series(1,610) i`); err != nil {
		t.Fatal(err)
	}
	m := AuthModule{store: s}
	rows, total, err := m.ListDirectoryMemberships(t.Context(), MembershipQuery{TeamID: "a", Offset: 500, Limit: 25, SortBy: "joined", SortOrder: "asc"})
	if err != nil || total != 610 || len(rows) != 25 || rows[0].UserID != "u-501" || rows[0].TeamName != "Team a" || rows[0].UserEmail != "u501@example.test" || rows[0].Roles[0] != "developer" {
		t.Fatalf("membership page: total=%d rows=%d err=%v", total, len(rows), err)
	}
	metadata, err := m.GetDirectoryTeamMetadata(t.Context(), "a")
	if err != nil || metadata.MemberCount != 610 || len(metadata.MemberIDs) != 0 {
		t.Fatal("team metadata loaded participant IDs or lost count", err)
	}
	if _, err := m.CreateTeamMembership(t.Context(), TeamMembership{TeamID: "b", UserID: "u-501", Roles: []string{"member"}}); err != nil {
		t.Fatal(err)
	}
	rows, total, err = m.ListDirectoryMemberships(t.Context(), MembershipQuery{UserID: "u-501", Limit: 25, SortBy: "team", SortOrder: "asc"})
	if err != nil || total != 2 || len(rows) != 2 || rows[0].TeamID != "a" || rows[1].TeamID != "b" {
		t.Fatal("reverse membership view failed", err)
	}
	rows, total, err = m.ListDirectoryMemberships(t.Context(), MembershipQuery{TeamID: "a", UserID: "u-501", Limit: 25, Search: "u501@"})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatal("scoped membership search failed", err)
	}
	rows, total, err = m.ListDirectoryMemberships(t.Context(), MembershipQuery{TeamID: "a", UserID: "u-501", Limit: 25, Offset: 100})
	if err != nil || total != 1 || len(rows) != 0 {
		t.Fatal("empty page lost total", err)
	}
	if _, err := m.CreateTeamMembership(t.Context(), TeamMembership{TeamID: "a", UserID: "u-501", Roles: []string{"member"}}); !errors.Is(err, ErrDirectoryConflict) {
		t.Fatal("duplicate creation overwrote membership", err)
	}
	rows, _, err = m.ListDirectoryMemberships(t.Context(), MembershipQuery{TeamID: "a", UserID: "u-501", Limit: 25})
	if err != nil || rows[0].Roles[0] != "developer" {
		t.Fatal("duplicate creation changed existing roles", err)
	}
	var successes atomic.Int64
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := m.CreateTeamMembership(t.Context(), TeamMembership{TeamID: "a", UserID: "u-620", Roles: []string{"member"}})
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrDirectoryConflict) {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent creates admitted=%d", successes.Load())
	}
	for _, kind := range []string{"user", "team"} {
		query := `UPDATE users SET status='disabled' WHERE id='u-619'`
		team, user := "a", "u-619"
		if kind == "team" {
			query = `UPDATE auth_teams SET status='disabled' WHERE id='b'`
			team, user = "b", "u-618"
		}
		if _, err := s.pool.Exec(t.Context(), query); err != nil {
			t.Fatal(err)
		}
		if _, err := m.CreateTeamMembership(t.Context(), TeamMembership{TeamID: team, UserID: user}); !errors.Is(err, ErrInvalidDirectoryEntry) {
			t.Fatal(fmt.Sprintf("inactive %s accepted", kind), err)
		}
	}
}
