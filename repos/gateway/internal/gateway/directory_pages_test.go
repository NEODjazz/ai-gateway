package gateway

import (
	"ai-gateway-gateway/internal/modules"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type directoryPageStub struct {
	directoryClientStub
	filter DirectoryPageFilter
}

func (c *directoryPageStub) ListUsersPage(ctx context.Context, audit ManagementAudit, filter DirectoryPageFilter) ([]DirectoryUser, int, error) {
	c.filter = filter
	return c.ListUsers(ctx, audit, filter.TeamID, filter.Offset, filter.Limit, filter.IncludeDeleted, filter.SortBy, filter.SortOrder)
}
func (c *directoryPageStub) ListTeamsPage(ctx context.Context, audit ManagementAudit, filter DirectoryPageFilter) ([]DirectoryTeam, int, error) {
	c.filter = filter
	return c.ListTeams(ctx, audit, filter.TeamID, filter.Offset, filter.Limit, filter.IncludeDeleted, filter.SortBy, filter.SortOrder)
}

func TestDirectoryPagesValidateFiltersAndPinTeamScope(t *testing.T) {
	c := &directoryPageStub{}
	h := Routes(NewHandler(modulesPipeline("admin"), nil).WithIdentityDirectory(c))
	query := "/admin/v1/users?search=OWNER%25_&status=active&role=user&sort_by=userName&sort_order=descending&include_deleted=false&offset=100&limit=25"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", query, nil))
	if w.Code != 200 || c.filter.Search != "OWNER%_" || c.filter.Offset != 100 || c.filter.IncludeDeleted || c.filter.SortBy != "userName" || c.filter.Role != "user" || !strings.Contains(w.Body.String(), `"total":7`) {
		t.Fatalf("filter lost: %d %+v", w.Code, c.filter)
	}
	for _, suffix := range []string{"status=bad", "sort_by=unknown&sort_order=ascending", "sort_order=ascending", "sort_by=id&sort_order=bad", "search=%00", "include_deleted=bad", "role=a%0Ab", "limit=501"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/users?"+suffix, nil))
		if w.Code != 400 {
			t.Fatalf("accepted %s: %d", suffix, w.Code)
		}
	}
	scoped := Routes(NewHandler(modulesPipeline("team_admin"), nil).WithIdentityDirectory(c))
	// A role alone must never manufacture a team scope.
	w = httptest.NewRecorder()
	scoped.ServeHTTP(w, httptest.NewRequest("GET", query, nil))
	if w.Code != http.StatusForbidden {
		t.Fatal("unbound team administrator read directory")
	}
	scoped = Routes(NewHandler(teamPipeline(), nil).WithIdentityDirectory(c))
	w = httptest.NewRecorder()
	scoped.ServeHTTP(w, httptest.NewRequest("GET", query+"&team_id=foreign", nil))
	if w.Code != 200 || c.filter.TeamID != "team-a" {
		t.Fatal("query escaped verified team scope")
	}
}

func TestDirectoryUserDetailsRequireScopeAndCreationIsSeparate(t *testing.T) {
	c := &directoryPageStub{directoryClientStub: directoryClientStub{user: &DirectoryUser{ID: "one", TeamIDs: []string{"team-a", "private-team"}}}}
	scoped := Routes(NewHandler(teamPipeline(), nil).WithIdentityDirectory(c))
	w := httptest.NewRecorder()
	scoped.ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/users/one", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-team") {
		t.Fatal("user detail exposed other memberships")
	}
	c.user.TeamIDs = []string{"private-team"}
	w = httptest.NewRecorder()
	scoped.ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/users/one", nil))
	if w.Code != 404 {
		t.Fatal("foreign user detail exposed")
	}
	for _, role := range []string{"user", "team_admin", "org_admin"} {
		for _, path := range []string{"/admin/v1/users", "/admin/v1/teams"} {
			w = httptest.NewRecorder()
			Routes(NewHandler(modulesPipeline(role), nil).WithIdentityDirectory(c)).ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"id":"new","name":"New","status":"active"}`)))
			if w.Code != 403 {
				t.Fatalf("%s created identity: %d", role, w.Code)
			}
		}
	}
	admin := Routes(NewHandler(modulesPipeline("admin"), nil).WithIdentityDirectory(c))
	w = httptest.NewRecorder()
	admin.ServeHTTP(w, httptest.NewRequest("POST", "/admin/v1/users", strings.NewReader(`{"id":"new","name":"New","roles":["user"],"status":"active"}`)))
	if w.Code != 201 || c.user.ID != "new" {
		t.Fatal("create user failed")
	}
	w = httptest.NewRecorder()
	admin.ServeHTTP(w, httptest.NewRequest("POST", "/admin/v1/teams", strings.NewReader(`{"id":"new-team","name":"New","status":"active"}`)))
	if w.Code != 201 || c.group == nil || c.group.Team.ID != "new-team" {
		t.Fatal("create team failed")
	}
	for _, path := range []string{"/admin/v1/users", "/admin/v1/teams"} {
		for _, extra := range []string{`"team_ids":["foreign"]`, `"created_at":"2026-01-01T00:00:00Z"`, `"member_ids":["one"]`} {
			w = httptest.NewRecorder()
			admin.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"id":"unsafe","status":"active",`+extra+`}`)))
			if w.Code != 400 {
				t.Fatalf("creation silently accepted unsupported field: %s %s", path, extra)
			}
		}
	}
}

func teamPipeline() modules.Pipeline { return modules.NewPipeline([]modules.Module{teamAdminModule{}}) }

func TestRemoteDirectoryPagesNegotiateFiltersAndPreserveVerifiedScope(t *testing.T) {
	var version atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("team_id") != "team-a" || q.Get("search") != "literal%_" || q.Get("offset") != "100" || q.Get("include_deleted") != "false" {
			t.Error("directory query lost filter or scope")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []DirectoryUser{{ID: "one"}}, "total": 130, "filtering_version": version.Load()})
	}))
	t.Cleanup(server.Close)
	c := NewRemoteManagementClient(server.URL, "fixture")
	filter := DirectoryPageFilter{TeamID: "team-a", Search: "literal%_", Offset: 100, Limit: 25}
	if rows, _, err := c.ListUsersPage(t.Context(), ManagementAudit{}, filter); err == nil || len(rows) != 0 {
		t.Fatal("old backend silently ignored filters")
	}
	version.Store(1)
	if rows, total, err := c.ListUsersPage(t.Context(), ManagementAudit{}, filter); err != nil || len(rows) != 1 || total != 130 {
		t.Fatal("supported filter negotiation failed", err)
	}
}
