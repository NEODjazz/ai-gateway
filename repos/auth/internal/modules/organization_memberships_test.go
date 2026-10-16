package modules

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type organizationMembershipStub struct {
	fakeVirtualKeyStore
	saved OrganizationMembership
}

func (s *organizationMembershipStub) PutOrganizationMembership(_ context.Context, m OrganizationMembership) (OrganizationMembership, error) {
	s.saved = m
	return m, nil
}
func (s *organizationMembershipStub) ListOrganizationMemberships(_ context.Context, org string, offset, limit int) (OrganizationMembershipPage, error) {
	return OrganizationMembershipPage{Data: []OrganizationMembership{s.saved}, Total: 1, Offset: offset, Limit: limit}, nil
}
func TestOrganizationMembershipValidation(t *testing.T) {
	store := &organizationMembershipStub{}
	m := NewAuthModuleWithStore(true, store, "fixture", false)
	valid := OrganizationMembership{OrganizationID: "org-a", UserID: "user-a", Status: "active", Roles: []string{"user", "org_admin"}}
	for _, test := range []struct {
		name   string
		change func(*OrganizationMembership)
	}{
		{"invalid organization", func(m *OrganizationMembership) { m.OrganizationID = "../org" }},
		{"missing user", func(m *OrganizationMembership) { m.UserID = "" }},
		{"invalid status", func(m *OrganizationMembership) { m.Status = "deleted" }},
		{"missing roles", func(m *OrganizationMembership) { m.Roles = nil }},
		{"global admin", func(m *OrganizationMembership) { m.Roles = []string{"admin"} }},
		{"team admin", func(m *OrganizationMembership) { m.Roles = []string{"team_admin"} }},
		{"duplicates", func(m *OrganizationMembership) { m.Roles = []string{"user", "user"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			member := valid
			test.change(&member)
			if _, err := m.PutOrganizationMembership(t.Context(), member); !errors.Is(err, ErrInvalidDirectoryEntry) {
				t.Fatal("invalid approval accepted", err)
			}
		})
	}
	if _, err := m.PutOrganizationMembership(t.Context(), valid); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.saved.Roles, []string{"org_admin", "user"}) {
		t.Fatal("roles were not normalized")
	}
	for _, bounds := range [][2]int{{-1, 10}, {0, 0}, {0, 501}, {1000001, 10}} {
		if _, err := m.ListOrganizationMemberships(t.Context(), "org-a", bounds[0], bounds[1]); !errors.Is(err, ErrInvalidDirectoryEntry) {
			t.Fatal("unbounded page accepted")
		}
	}
}

func TestDirectoryJWTOrganizationAdminRequiresScopedApproval(t *testing.T) {
	for _, test := range []struct {
		name, org        string
		global, approved []string
		allow            bool
	}{
		{"claim only", "org-a", nil, nil, false},
		{"global directory role", "org-a", []string{"org_admin"}, nil, false},
		{"different membership role", "org-a", nil, []string{"user"}, false},
		{"unscoped approval", "", nil, []string{"org_admin"}, false},
		{"approved organization", "org-a", nil, []string{"org_admin"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, store, claims := directoryJWTModule(t)
			m.jwtConfig.RoleMappings["organization-admin"] = "org_admin"
			store.principal.OrganizationID = test.org
			store.principal.Roles = test.global
			store.principal.OrganizationRoles = test.approved
			claims["resource_access"] = map[string]any{"gateway": map[string]any{"roles": []string{"organization-admin"}}}
			req := RequestContext{APIKey: testJWT(t, "test-signing-secret", claims)}
			err := m.Handle(t.Context(), &req)
			if test.allow {
				if err != nil || !reflect.DeepEqual(req.Roles, []string{"org_admin"}) || req.OrganizationID != "org-a" {
					t.Fatal("scoped approval not applied", err)
				}
			} else if !errors.Is(err, ErrUnauthorized) {
				t.Fatal("unapproved organization admin accepted", err)
			}
		})
	}
}
