package gateway

import (
	"net/http/httptest"
	"testing"
)

func TestOrganizationKeyListRequiresOwnedPageAndForbidsGlobalMutations(t *testing.T) {
	for _, test := range []struct {
		path   string
		status int
	}{{"/admin/v1/keys", 200}, {"/admin/v1/keys?organization_id=org-b", 403}, {"/admin/v1/keys?expand=financials", 403}} {
		client := &recordingManagementClient{page: &VirtualKeyPage{Data: []VirtualKeyMetadata{{ID: "vk_keya", OrganizationID: "org-a"}}, Total: 9, Limit: 100}}
		w := httptest.NewRecorder()
		Routes(organizationReportHandler("org-a", "org_admin").WithManagement(client)).ServeHTTP(w, httptest.NewRequest("GET", test.path, nil))
		if w.Code != test.status {
			t.Fatal(w.Code, w.Body.String())
		}
		if test.status == 200 && (client.filter.OrganizationID != "org-a" || client.audit.OrganizationID != "org-a") {
			t.Fatal("key scope missing")
		}
	}
	for _, org := range []string{"org-b", ""} {
		client := &recordingManagementClient{page: &VirtualKeyPage{Data: []VirtualKeyMetadata{{ID: "vk_keyb", OrganizationID: org}}, Total: 1}}
		w := httptest.NewRecorder()
		Routes(organizationReportHandler("org-a", "org_admin").WithManagement(client)).ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/keys", nil))
		if w.Code != 502 {
			t.Fatal("foreign or unassigned key metadata disclosed", w.Code)
		}
	}
	for _, test := range []struct{ method, path string }{{"POST", "/admin/v1/keys"}, {"PUT", "/admin/v1/keys/vk_keya"}, {"DELETE", "/admin/v1/keys/vk_keya"}, {"POST", "/admin/v1/keys/vk_keya/rotate"}, {"POST", "/admin/v1/keys/vk_keya/disable"}, {"POST", "/admin/v1/keys/vk_keya/enable"}} {
		w := httptest.NewRecorder()
		Routes(organizationReportHandler("org-a", "org_admin").WithManagement(&recordingManagementClient{})).ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
		if w.Code != 403 {
			t.Fatal("org admin reached global key mutation", w.Code)
		}
	}
}
