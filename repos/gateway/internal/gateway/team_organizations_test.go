package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type teamOrganizationClientStub struct {
	memoryOrganizationClient
	query         OrganizationPageQuery
	teamID, orgID string
	remove        bool
	calls         int
	err           error
	wrongTeam     bool
}

func (c *teamOrganizationClientStub) TeamOrganization(_ context.Context, _ ManagementAudit, id string) (TeamOrganization, error) {
	c.calls++
	if c.wrongTeam {
		id = "foreign"
	}
	return TeamOrganization{TeamID: id, Organization: &OrganizationMetadata{ID: "owner", Name: "Owner", Status: "active"}}, c.err
}
func (c *teamOrganizationClientStub) ChangeTeamOrganization(_ context.Context, _ ManagementAudit, id, orgID string, remove bool) (TeamOrganization, error) {
	c.calls++
	c.teamID, c.orgID, c.remove = id, orgID, remove
	return TeamOrganization{TeamID: id}, c.err
}
func (c *teamOrganizationClientStub) OrganizationPage(_ context.Context, _ ManagementAudit, q OrganizationPageQuery) (OrganizationPage, error) {
	c.calls++
	c.query = q
	return OrganizationPage{Data: []OrganizationMetadata{{ID: "org-620", Name: "Late organization", Status: "active"}}, Total: 620, Offset: q.Offset, Limit: q.Limit}, c.err
}
func (c *teamOrganizationClientStub) CreateTeamInOrganization(_ context.Context, _ ManagementAudit, team DirectoryTeam, id string) (DirectoryTeam, error) {
	c.calls++
	c.teamID, c.orgID = team.ID, id
	return team, c.err
}

func TestTeamOrganizationScopeMutationAndMetadata(t *testing.T) {
	c := &teamOrganizationClientStub{}
	directory := &directoryClientStub{}
	for _, tc := range []struct {
		role, path, method, body string
		status                   int
	}{
		{"admin", "/admin/v1/teams/team-a/organization", "GET", "", 200},
		{"admin", "/admin/v1/teams/team-a/organization", "PUT", `{"organization_id":"owner"}`, 200},
		{"admin", "/admin/v1/teams/team-a/organization", "DELETE", `{"organization_id":"owner"}`, 200},
		{"team_admin", "/admin/v1/teams/team-a/organization", "PUT", `{"organization_id":"other"}`, 403},
		{"org_admin", "/admin/v1/teams/team-a/organization", "GET", "", 403},
		{"user", "/admin/v1/teams/team-a/organization", "GET", "", 403},
		{"admin", "/admin/v1/teams/team-a/organization", "PUT", `{"organization_id":"owner","team_id":"foreign"}`, 400},
		{"admin", "/admin/v1/teams/team-a/organization", "DELETE", `{}`, 400},
	} {
		before := c.calls
		w := httptest.NewRecorder()
		h := NewHandler(modulesPipeline(tc.role), nil).WithIdentityDirectory(directory).WithOrganizations(c)
		Routes(h).ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.status || tc.status != 200 && c.calls != before {
			t.Fatalf("%s %s status=%d calls=%d", tc.role, tc.method, w.Code, c.calls)
		}
	}
	for _, tc := range []struct {
		id     string
		status int
	}{{"team-a", 200}, {"foreign", 403}} {
		w := httptest.NewRecorder()
		Routes(NewHandler(teamPipeline(), nil).WithIdentityDirectory(directory).WithOrganizations(c)).ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/teams/"+tc.id+"/organization?team_id=foreign", nil))
		if w.Code != tc.status {
			t.Fatal("team read escaped verified scope", w.Code)
		}
	}
	c.wrongTeam = true
	w := httptest.NewRecorder()
	Routes(NewHandler(teamPipeline(), nil).WithIdentityDirectory(directory).WithOrganizations(c)).ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/teams/team-a/organization", nil))
	if w.Code != 502 || strings.Contains(w.Body.String(), "Owner") {
		t.Fatal("foreign metadata passed through")
	}
}

func TestOrganizationPageAndAtomicTeamCreation(t *testing.T) {
	c := &teamOrganizationClientStub{}
	audit := &recordingAuditClient{}
	h := NewHandler(modulesPipeline("admin"), nil).WithIdentityDirectory(&directoryClientStub{}).WithOrganizations(c).WithAudit(audit)
	call := func(method, path, body string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		Routes(h).ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != status {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
	call("GET", "/admin/v1/organizations/page?offset=500&limit=25&search=late&status=active", "", 200)
	if c.query.Offset != 500 || c.query.Search != "late" || c.query.Status != "active" {
		t.Fatal("organization filters lost")
	}
	for _, q := range []string{"limit=101", "offset=bad", "status=bad", "search=%00"} {
		before := c.calls
		call("GET", "/admin/v1/organizations/page?"+q, "", 400)
		if c.calls != before {
			t.Fatal("invalid page reached backend")
		}
	}
	call("POST", "/admin/v1/teams", `{"id":"new-team","name":"New","status":"active","organization_id":"org-620"}`, 201)
	if c.teamID != "new-team" || c.orgID != "org-620" || len(audit.events) != 2 || audit.events[0].Details["organization_id"] != "org-620" {
		t.Fatal("atomic creation lost target or audit")
	}
	c.err = &ManagementError{Status: 409}
	call("PUT", "/admin/v1/teams/team-a/organization", `{"organization_id":"foreign"}`, 409)
	c.err = &ManagementError{Status: 400}
	call("POST", "/admin/v1/teams", `{"id":"new-team","name":"New","status":"active","organization_id":"disabled"}`, 400)
	for _, role := range []string{"team_admin", "org_admin", "user"} {
		w := httptest.NewRecorder()
		Routes(NewHandler(modulesPipeline(role), nil).WithOrganizations(c)).ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/organizations/page?limit=25", nil))
		if w.Code != 403 {
			t.Fatal("scoped session accessed global choices", role, w.Code)
		}
	}
}

func TestRemoteTeamOrganizationRoutesUseScopedManagementSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Management-Token") != "fixture-secret" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Actor-ID") != "operator" {
			t.Error("incorrect management authentication")
		}
		switch r.URL.Path {
		case "/internal/v1/teams/team-a/organization":
			if r.Method != "GET" {
				var body map[string]string
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["organization_id"] != "org-620" {
					t.Error("ownership target lost")
				}
			}
			_ = json.NewEncoder(w).Encode(TeamOrganization{TeamID: "team-a"})
		case "/internal/v1/organizations/page":
			if r.URL.Query().Get("offset") != "500" || r.URL.Query().Get("search") != "late" {
				t.Error("page filters lost")
			}
			_ = json.NewEncoder(w).Encode(OrganizationPage{Data: []OrganizationMetadata{}, Total: 620, Offset: 500, Limit: 25})
		case "/internal/v1/organizations/org-620/teams/team-a":
			if r.Method != "DELETE" || r.ContentLength != 0 {
				t.Error("legacy DELETE unexpectedly gained a request body")
			}
			w.WriteHeader(http.StatusNoContent)
		case "/internal/v1/teams":
			var body struct {
				Team           DirectoryTeam `json:"team"`
				OrganizationID string        `json:"organization_id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Team.ID != "team-a" || body.OrganizationID != "org-620" {
				t.Error("atomic create target lost")
			}
			_ = json.NewEncoder(w).Encode(body.Team)
		default:
			t.Error("unexpected route", r.URL.Path)
		}
	}))
	defer server.Close()
	c := NewRemoteManagementClient(server.URL, "fixture-secret")
	audit := ManagementAudit{ActorID: "operator"}
	if _, err := c.TeamOrganization(t.Context(), audit, "team-a"); err != nil {
		t.Fatal(err)
	}
	for _, remove := range []bool{false, true} {
		if _, err := c.ChangeTeamOrganization(t.Context(), audit, "team-a", "org-620", remove); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.OrganizationPage(t.Context(), audit, OrganizationPageQuery{Search: "late", Offset: 500, Limit: 25}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteOrganizationTeam(t.Context(), audit, "org-620", "team-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTeamInOrganization(t.Context(), audit, DirectoryTeam{ID: "team-a"}, "org-620"); err != nil {
		t.Fatal(err)
	}
}
