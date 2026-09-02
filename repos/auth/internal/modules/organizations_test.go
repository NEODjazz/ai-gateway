package modules

import (
	"context"
	"testing"
)

type organizationTestStore struct {
	managementStore
	item Organization
}

func (s *organizationTestStore) ListOrganizations(context.Context, int) ([]Organization, error) {
	return []Organization{s.item}, nil
}
func (s *organizationTestStore) PutOrganization(_ context.Context, item Organization) (Organization, error) {
	s.item = item
	item.TeamIDs = []string{}
	return item, nil
}
func (s *organizationTestStore) PutOrganizationTeam(_ context.Context, organizationID, teamID string) (Organization, error) {
	s.item.ID = organizationID
	s.item.TeamIDs = []string{teamID}
	return s.item, nil
}
func (s *organizationTestStore) DeleteOrganizationTeam(_ context.Context, organizationID, teamID string) (bool, error) {
	if s.item.ID != organizationID || len(s.item.TeamIDs) != 1 || s.item.TeamIDs[0] != teamID {
		return false, nil
	}
	s.item.TeamIDs = []string{}
	return true, nil
}

func TestOrganizationDirectoryValidatesAndAssignsTeam(t *testing.T) {
	store := &organizationTestStore{}
	module := NewAuthModuleWithStore(true, store, "pepper", false)
	organization, err := module.PutOrganization(context.Background(), Organization{ID: "acme", Name: "Acme", Status: "active"})
	if err != nil || organization.ID != "acme" {
		t.Fatalf("organization=%+v err=%v", organization, err)
	}
	organization, err = module.PutOrganizationTeam(context.Background(), "acme", "platform")
	if err != nil || len(organization.TeamIDs) != 1 || organization.TeamIDs[0] != "platform" {
		t.Fatalf("assignment=%+v err=%v", organization, err)
	}
	if _, err := module.PutOrganization(context.Background(), Organization{ID: "bad/id", Name: "Bad", Status: "active"}); err == nil {
		t.Fatal("invalid organization ID accepted")
	}
	if deleted, err := module.DeleteOrganizationTeam(context.Background(), "acme", "platform"); err != nil || !deleted {
		t.Fatalf("delete assignment: deleted=%v err=%v", deleted, err)
	}
	if _, err := module.DeleteOrganizationTeam(context.Background(), "bad/id", "platform"); err == nil {
		t.Fatal("invalid organization ID accepted for delete")
	}
}
