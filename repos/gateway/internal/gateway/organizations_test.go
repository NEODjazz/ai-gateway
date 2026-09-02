package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type memoryOrganizationClient struct {
	items      map[string]Organization
	putTeamErr error
}

func (c *memoryOrganizationClient) ListOrganizations(context.Context, ManagementAudit, int) ([]Organization, error) {
	result := make([]Organization, 0, len(c.items))
	for _, item := range c.items {
		result = append(result, item)
	}
	return result, nil
}
func (c *memoryOrganizationClient) PutOrganization(_ context.Context, _ ManagementAudit, id string, item Organization) (Organization, error) {
	item.ID = id
	item.TeamIDs = []string{}
	c.items[id] = item
	return item, nil
}
func (c *memoryOrganizationClient) PutOrganizationTeam(_ context.Context, _ ManagementAudit, id, teamID string) (Organization, error) {
	if c.putTeamErr != nil {
		return Organization{}, c.putTeamErr
	}
	item := c.items[id]
	item.TeamIDs = append(item.TeamIDs, teamID)
	c.items[id] = item
	return item, nil
}

func TestOrganizationTeamConflictIsPreserved(t *testing.T) {
	client := &memoryOrganizationClient{
		items:      map[string]Organization{"acme": {ID: "acme"}},
		putTeamErr: &ManagementError{Status: http.StatusConflict},
	}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithOrganizations(client)
	recorder := httptest.NewRecorder()
	Routes(handler).ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/admin/v1/organizations/acme/teams/platform", nil))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"code":"conflict"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
func (c *memoryOrganizationClient) DeleteOrganizationTeam(_ context.Context, _ ManagementAudit, id, teamID string) error {
	item := c.items[id]
	filtered := make([]string, 0, len(item.TeamIDs))
	for _, candidate := range item.TeamIDs {
		if candidate != teamID {
			filtered = append(filtered, candidate)
		}
	}
	item.TeamIDs = filtered
	c.items[id] = item
	return nil
}

func TestOrganizationLifecycleRequiresGlobalAdmin(t *testing.T) {
	client := &memoryOrganizationClient{items: map[string]Organization{}}
	handler := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithOrganizations(client)
	router := Routes(handler)
	put := httptest.NewRecorder()
	router.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/admin/v1/organizations/acme", strings.NewReader(`{"name":"Acme","status":"active"}`)))
	if put.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", put.Code, put.Body.String())
	}
	team := httptest.NewRecorder()
	router.ServeHTTP(team, httptest.NewRequest(http.MethodPut, "/admin/v1/organizations/acme/teams/platform", nil))
	if team.Code != http.StatusOK || !strings.Contains(team.Body.String(), `"team_ids":["platform"]`) {
		t.Fatalf("team status=%d body=%s", team.Code, team.Body.String())
	}
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/organizations", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"id":"acme"`) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	remove := httptest.NewRecorder()
	router.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/admin/v1/organizations/acme/teams/platform", nil))
	if remove.Code != http.StatusNoContent || len(client.items["acme"].TeamIDs) != 0 {
		t.Fatalf("remove status=%d client=%+v body=%s", remove.Code, client, remove.Body.String())
	}
}
