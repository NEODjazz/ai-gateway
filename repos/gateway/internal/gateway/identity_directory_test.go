package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

type directoryClientStub struct {
	teamFilter  string
	putTeam     string
	memberTeam  string
	memberUser  string
	deletedTeam string
	deletedUser string
}

func (c *directoryClientStub) ListUsers(context.Context, ManagementAudit, string, int) ([]DirectoryUser, error) {
	return []DirectoryUser{{ID: "user-1", Status: "active"}}, nil
}
func (c *directoryClientStub) PutUser(_ context.Context, _ ManagementAudit, id string, user DirectoryUser) (DirectoryUser, error) {
	user.ID = id
	return user, nil
}
func (c *directoryClientStub) ListTeams(_ context.Context, _ ManagementAudit, teamID string, _ int) ([]DirectoryTeam, error) {
	c.teamFilter = teamID
	return []DirectoryTeam{{ID: "team-a", Name: "Team A", Status: "active"}}, nil
}
func (c *directoryClientStub) PutTeam(_ context.Context, _ ManagementAudit, id string, team DirectoryTeam) (DirectoryTeam, error) {
	c.putTeam = id
	team.ID = id
	return team, nil
}
func (c *directoryClientStub) PutMembership(_ context.Context, _ ManagementAudit, teamID, userID string, m TeamMembership) (TeamMembership, error) {
	c.memberTeam, c.memberUser = teamID, userID
	m.TeamID, m.UserID = teamID, userID
	return m, nil
}
func (c *directoryClientStub) ListMemberships(_ context.Context, _ ManagementAudit, teamID string, _ int) ([]TeamMembership, error) {
	c.memberTeam = teamID
	return []TeamMembership{{TeamID: teamID, UserID: "user-1", Roles: []string{"member"}}}, nil
}
func (c *directoryClientStub) DeleteMembership(_ context.Context, _ ManagementAudit, teamID, userID string) error {
	c.deletedTeam, c.deletedUser = teamID, userID
	return nil
}

func TestIdentityDirectoryAdminCRUD(t *testing.T) {
	client := &directoryClientStub{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithIdentityDirectory(client)
	request := httptest.NewRequest(http.MethodPut, "/admin/v1/teams/team-a", strings.NewReader(`{"name":"Team A","status":"active"}`))
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || client.putTeam != "team-a" {
		t.Fatalf("status=%d client=%+v body=%s", response.Code, client, response.Body.String())
	}
}

type teamAdminModule struct{}

func (teamAdminModule) Name() string   { return "auth" }
func (teamAdminModule) Required() bool { return true }
func (teamAdminModule) Handle(_ context.Context, req *modules.RequestContext) error {
	req.UserID = "manager"
	req.TeamID = "team-a"
	req.Roles = []string{"team_admin"}
	req.CredentialID = "cred"
	req.APIKey = ""
	return nil
}

func TestTeamAdminIsRestrictedToOwnTeam(t *testing.T) {
	client := &directoryClientStub{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{teamAdminModule{}}), modelsProvider{}).WithIdentityDirectory(client)
	list := httptest.NewRecorder()
	Routes(handler).ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/teams", nil))
	if list.Code != http.StatusOK || client.teamFilter != "team-a" {
		t.Fatalf("team-scoped list failed: status=%d filter=%q", list.Code, client.teamFilter)
	}
	foreign := httptest.NewRecorder()
	Routes(handler).ServeHTTP(foreign, httptest.NewRequest(http.MethodPut, "/admin/v1/teams/team-b/members/user-1", strings.NewReader(`{"roles":["member"]}`)))
	if foreign.Code != http.StatusForbidden || client.memberTeam != "" {
		t.Fatalf("foreign mutation reached directory: status=%d client=%+v", foreign.Code, client)
	}
	own := httptest.NewRecorder()
	Routes(handler).ServeHTTP(own, httptest.NewRequest(http.MethodPut, "/admin/v1/teams/team-a/members/user-1", strings.NewReader(`{"roles":["member"]}`)))
	if own.Code != http.StatusOK || client.memberTeam != "team-a" || client.memberUser != "user-1" {
		t.Fatalf("own mutation failed: status=%d client=%+v body=%s", own.Code, client, own.Body.String())
	}
	members := httptest.NewRecorder()
	Routes(handler).ServeHTTP(members, httptest.NewRequest(http.MethodGet, "/admin/v1/teams/team-a/members?limit=25", nil))
	if members.Code != http.StatusOK || !strings.Contains(members.Body.String(), `"user_id":"user-1"`) {
		t.Fatalf("membership list failed: status=%d body=%s", members.Code, members.Body.String())
	}
	removeForeign := httptest.NewRecorder()
	Routes(handler).ServeHTTP(removeForeign, httptest.NewRequest(http.MethodDelete, "/admin/v1/teams/team-b/members/user-1", nil))
	if removeForeign.Code != http.StatusForbidden || client.deletedTeam != "" {
		t.Fatalf("foreign membership delete reached directory: status=%d client=%+v", removeForeign.Code, client)
	}
	removeOwn := httptest.NewRecorder()
	Routes(handler).ServeHTTP(removeOwn, httptest.NewRequest(http.MethodDelete, "/admin/v1/teams/team-a/members/user-1", nil))
	if removeOwn.Code != http.StatusNoContent || client.deletedTeam != "team-a" || client.deletedUser != "user-1" {
		t.Fatalf("own membership delete failed: status=%d client=%+v body=%s", removeOwn.Code, client, removeOwn.Body.String())
	}
	global := httptest.NewRecorder()
	Routes(handler).ServeHTTP(global, httptest.NewRequest(http.MethodPut, "/admin/v1/users/user-1", strings.NewReader(`{"status":"active"}`)))
	if global.Code != http.StatusForbidden {
		t.Fatalf("team admin changed global user: status=%d", global.Code)
	}
}
