package modules

import (
	"context"
	"testing"
)

type membershipTestStore struct {
	managementStore
	memberships []TeamMembership
	deletedTeam string
	deletedUser string
}

func (s *membershipTestStore) ListMemberships(context.Context, string, int) ([]TeamMembership, error) {
	return append([]TeamMembership(nil), s.memberships...), nil
}

func (s *membershipTestStore) DeleteMembership(_ context.Context, teamID, userID string) (bool, error) {
	s.deletedTeam, s.deletedUser = teamID, userID
	return true, nil
}

func TestTeamMembershipLifecycleValidatesScope(t *testing.T) {
	store := &membershipTestStore{memberships: []TeamMembership{{TeamID: "platform", UserID: "user-1", Roles: []string{"team_admin"}}}}
	module := NewAuthModuleWithStore(true, store, "pepper", false)
	memberships, err := module.ListTeamMemberships(context.Background(), "platform", 50)
	if err != nil || len(memberships) != 1 || memberships[0].UserID != "user-1" {
		t.Fatalf("memberships=%+v err=%v", memberships, err)
	}
	deleted, err := module.DeleteTeamMembership(context.Background(), "platform", "user-1")
	if err != nil || !deleted || store.deletedTeam != "platform" || store.deletedUser != "user-1" {
		t.Fatalf("deleted=%v store=%+v err=%v", deleted, store, err)
	}
	if _, err := module.ListTeamMemberships(context.Background(), "bad/team", 50); err == nil {
		t.Fatal("invalid team ID was accepted")
	}
	if _, err := module.DeleteTeamMembership(context.Background(), "platform", "bad/user"); err == nil {
		t.Fatal("invalid user ID was accepted")
	}
}
