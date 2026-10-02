package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type memoryOrganizationMembershipClient struct {
	memoryOrganizationClient
	member OrganizationMembership
	calls  int
}

func (c *memoryOrganizationMembershipClient) ListOrganizationMemberships(_ context.Context, _ ManagementAudit, org string, offset, limit int) (OrganizationMembershipPage, error) {
	c.calls++
	return OrganizationMembershipPage{Data: []OrganizationMembership{}, Total: 7, Offset: offset, Limit: limit}, nil
}
func (c *memoryOrganizationMembershipClient) PutOrganizationMembership(_ context.Context, _ ManagementAudit, m OrganizationMembership) (OrganizationMembership, error) {
	c.calls++
	c.member = m
	return m, nil
}
func TestOrganizationMembershipApprovalRequiresGlobalAdminAndUsesPath(t *testing.T) {
	client := &memoryOrganizationMembershipClient{}
	for _, role := range []string{"org_admin", "team_admin", "user"} {
		h := NewHandler(modulesPipeline(role), modelsProvider{}).WithOrganizations(client)
		for _, request := range []struct{ method, path string }{{"GET", "/admin/v1/organizations/org-a/members"}, {"PUT", "/admin/v1/organizations/org-a/members/user-a"}} {
			w := httptest.NewRecorder()
			Routes(h).ServeHTTP(w, httptest.NewRequest(request.method, request.path, strings.NewReader(`{"status":"active","roles":["org_admin"]}`)))
			if w.Code != http.StatusForbidden {
				t.Fatalf("role=%s status=%d", role, w.Code)
			}
		}
	}
	if client.calls != 0 {
		t.Fatal("unapproved actor reached management")
	}
	h := NewHandler(modulesPipeline("admin"), modelsProvider{}).WithOrganizations(client)
	w := httptest.NewRecorder()
	Routes(h).ServeHTTP(w, httptest.NewRequest("PUT", "/admin/v1/organizations/org-a/members/user-a", strings.NewReader(`{"organization_id":"org-b","user_id":"user-b","status":"active","roles":["org_admin"]}`)))
	if w.Code != http.StatusOK || client.member.OrganizationID != "org-a" || client.member.UserID != "user-a" {
		t.Fatal("path ownership not applied", w.Code)
	}
	w = httptest.NewRecorder()
	Routes(h).ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/organizations/org-a/members?offset=20&limit=5", nil))
	var page OrganizationMembershipPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Total != 7 || page.Offset != 20 || page.Limit != 5 {
		t.Fatal("membership page total incorrect", w.Code)
	}
}
