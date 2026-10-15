package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-auth/internal/modules"
)

type teamOrganizationHTTPStore struct {
	calls int
	err   error
}

func (*teamOrganizationHTTPStore) Lookup(context.Context, string) (modules.StoredVirtualKey, bool, error) {
	return modules.StoredVirtualKey{}, false, nil
}
func (*teamOrganizationHTTPStore) Ready(context.Context) error { return nil }
func (*teamOrganizationHTTPStore) Close()                      {}
func (s *teamOrganizationHTTPStore) TeamOrganization(_ context.Context, id string) (modules.TeamOrganization, error) {
	s.calls++
	return modules.TeamOrganization{TeamID: id}, s.err
}
func (s *teamOrganizationHTTPStore) SetTeamOrganization(ctx context.Context, id, _ string) (modules.TeamOrganization, error) {
	return s.TeamOrganization(ctx, id)
}
func (s *teamOrganizationHTTPStore) ClearTeamOrganization(ctx context.Context, id, _ string) (modules.TeamOrganization, error) {
	return s.TeamOrganization(ctx, id)
}
func (s *teamOrganizationHTTPStore) OrganizationPage(_ context.Context, q modules.OrganizationPageQuery) (modules.OrganizationPage, error) {
	s.calls++
	return modules.OrganizationPage{Data: []modules.OrganizationMetadata{}, Total: 620, Offset: q.Offset, Limit: q.Limit}, s.err
}
func (s *teamOrganizationHTTPStore) CreateTeamInOrganization(_ context.Context, team modules.DirectoryTeam, _ string) (modules.DirectoryTeam, error) {
	s.calls++
	return team, s.err
}

func TestTeamOrganizationHTTPSecretValidationAndErrors(t *testing.T) {
	s := &teamOrganizationHTTPStore{}
	module := modules.NewAuthModuleWithStore(true, s, "fixture", false)
	mux := http.NewServeMux()
	registerIdentityDirectoryRoutes(mux, &module, "fixture-secret")
	for _, tc := range []struct {
		method, path, body, secret string
		status                     int
	}{
		{"GET", "/internal/v1/teams/team-a/organization", "", "", 401},
		{"GET", "/internal/v1/teams/team-a/organization", "", "fixture-secret", 200},
		{"GET", "/internal/v1/organizations/page?offset=500&limit=25", "", "fixture-secret", 200},
		{"GET", "/internal/v1/organizations/page?offset=-1", "", "fixture-secret", 400},
		{"GET", "/internal/v1/organizations/page?limit=101", "", "fixture-secret", 400},
		{"PUT", "/internal/v1/teams/team-a/organization", `{"organization_id":"owner","team_id":"foreign"}`, "fixture-secret", 400},
		{"PUT", "/internal/v1/teams/team-a/organization", `{"organization_id":"owner"}`, "fixture-secret", 200},
		{"DELETE", "/internal/v1/teams/team-a/organization", `{"organization_id":"owner"}`, "fixture-secret", 200},
		{"POST", "/internal/v1/teams", `{"team":{"id":"new","name":"New","status":"active"},"organization_id":"owner"}`, "fixture-secret", 201},
	} {
		before := s.calls
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("X-Management-Token", tc.secret)
		r.Header.Set("X-Request-ID", "ownership-request")
		r.Header.Set("X-Actor-ID", "operator")
		r.Header.Set("X-Actor-Credential-ID", "fixture-credential")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.status || tc.status >= 400 && s.calls != before {
			t.Fatalf("%s %s status=%d", tc.method, tc.path, w.Code)
		}
	}
	s.err = modules.ErrOrganizationTeamConflict
	r := httptest.NewRequest("PUT", "/internal/v1/teams/team-a/organization", strings.NewReader(`{"organization_id":"owner"}`))
	r.Header.Set("X-Management-Token", "fixture-secret")
	r.Header.Set("X-Request-ID", "ownership-request")
	r.Header.Set("X-Actor-ID", "operator")
	r.Header.Set("X-Actor-Credential-ID", "fixture-credential")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal("ownership conflict lost", w.Code)
	}
}
