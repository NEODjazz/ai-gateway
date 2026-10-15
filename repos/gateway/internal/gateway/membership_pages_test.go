package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type membershipPagesStub struct {
	directoryClientStub
	filter  MembershipPageFilter
	created TeamMembership
	err     error
}

func (c *membershipPagesStub) GetTeamMetadata(_ context.Context, _ ManagementAudit, id string) (DirectoryTeam, error) {
	return DirectoryTeam{ID: id, Name: "Platform", MemberCount: 620}, nil
}

func TestTeamMetadataDoesNotLoadMembersAndEnforcesScope(t *testing.T) {
	c := &membershipPagesStub{}
	router := Routes(NewHandler(teamPipeline(), nil).WithIdentityDirectory(c))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/teams/team-a", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"member_count":620`) || strings.Contains(w.Body.String(), "member_ids") {
		t.Fatal("team metadata contract lost", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/teams/foreign", nil))
	if w.Code != 403 {
		t.Fatal("team metadata escaped verified scope", w.Code)
	}
}

func (c *membershipPagesStub) ListMembershipsPage(_ context.Context, _ ManagementAudit, f MembershipPageFilter) (MembershipPage, error) {
	c.filter = f
	return MembershipPage{Data: []DirectoryMembership{{TeamMembership: TeamMembership{TeamID: f.TeamID, UserID: f.UserID}, UserName: "Alice", TeamName: "Platform"}}, Total: 620, Limit: f.Limit, Offset: f.Offset}, c.err
}
func (c *membershipPagesStub) CreateMembership(_ context.Context, _ ManagementAudit, m TeamMembership) (TeamMembership, error) {
	c.created = m
	return m, c.err
}

func TestMembershipPagesPreserveScopeTotalsAndRejectUnboundedQueries(t *testing.T) {
	c := &membershipPagesStub{}
	admin := Routes(NewHandler(modulesPipeline("admin"), nil).WithIdentityDirectory(c))
	w := httptest.NewRecorder()
	admin.ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/team-memberships?team_id=team-a&offset=500&limit=25&search=Alice&sort_by=user&sort_order=asc", nil))
	if w.Code != 200 || c.filter.Offset != 500 || c.filter.TeamID != "team-a" || c.filter.Search != "Alice" || !strings.Contains(w.Body.String(), `"total":620`) {
		t.Fatal("paged contract lost", w.Code, c.filter)
	}
	for _, query := range []string{"", "team_id=a&limit=501", "team_id=a&offset=-1", "team_id=a&sort_by=bad", "team_id=a&search=%00"} {
		w = httptest.NewRecorder()
		admin.ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/team-memberships?"+query, nil))
		if w.Code != 400 {
			t.Fatalf("accepted query %s: %d", query, w.Code)
		}
	}
	scoped := Routes(NewHandler(teamPipeline(), nil).WithIdentityDirectory(c))
	w = httptest.NewRecorder()
	scoped.ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/team-memberships?team_id=foreign&user_id=user-1", nil))
	if w.Code != 200 || c.filter.TeamID != "team-a" || c.filter.UserID != "user-1" {
		t.Fatal("query escaped verified team")
	}
	for _, role := range []string{"user", "org_admin"} {
		w = httptest.NewRecorder()
		Routes(NewHandler(modulesPipeline(role), nil).WithIdentityDirectory(c)).ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/team-memberships?team_id=a", nil))
		if w.Code != 403 {
			t.Fatal("unapproved role read directory", role, w.Code)
		}
	}
}

func TestMembershipCreationUsesMatchingTeamAndReturnsConflict(t *testing.T) {
	c := &membershipPagesStub{}
	router := Routes(NewHandler(teamPipeline(), nil).WithIdentityDirectory(c))
	call := func(team, body string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/v1/teams/"+team+"/members", strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
	call("foreign", `{"user_id":"one","roles":["member"]}`, 403)
	if c.created.UserID != "" {
		t.Fatal("foreign mutation reached backend")
	}
	call("team-a", `{"user_id":"one","roles":["member"]}`, 201)
	if c.created.TeamID != "team-a" || c.created.UserID != "one" {
		t.Fatal("membership scope lost")
	}
	c.err = &ManagementError{Status: http.StatusConflict}
	call("team-a", `{"user_id":"one","roles":["member"]}`, 409)
	call("team-a", `{"team_id":"foreign","user_id":"one"}`, 400)
}
