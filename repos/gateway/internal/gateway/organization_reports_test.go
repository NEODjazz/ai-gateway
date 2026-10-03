package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

type organizationReportAuth struct {
	org   string
	roles []string
}

func (organizationReportAuth) Name() string   { return "fixture" }
func (organizationReportAuth) Required() bool { return true }
func (a organizationReportAuth) Handle(_ context.Context, req *modules.RequestContext) error {
	req.OrganizationID = a.org
	req.UserID = "approved-user"
	req.CredentialID = "approved-credential"
	req.Roles = a.roles
	return nil
}
func organizationReportHandler(org string, roles ...string) Handler {
	return NewHandler(modules.NewPipeline([]modules.Module{organizationReportAuth{org: org, roles: roles}}), modelsProvider{})
}

func TestOrganizationUsageScopeCannotBeOverridden(t *testing.T) {
	for _, test := range []struct {
		path   string
		status int
	}{
		{"/admin/v1/usage/report", 200},
		{"/admin/v1/usage/report?scope_type=organization&scope_id=org-a&model=model-a", 200},
		{"/admin/v1/usage/report?scope_type=organization&scope_id=org-b", 403},
		{"/admin/v1/usage/report?scope_type=user&scope_id=approved-user", 403},
		{"/admin/v1/usage/report?scope_type=team&scope_id=team-a", 403},
		{"/admin/v1/customers/organization/org-a/usage", 200},
		{"/admin/v1/customers/organization/org-b/usage", 403},
		{"/admin/v1/customers/key/key-a/usage", 403},
	} {
		t.Run(test.path, func(t *testing.T) {
			client := &recordingUsageClient{}
			h := Routes(organizationReportHandler("org-a", "org_admin").WithUsageReporting(client))
			r := httptest.NewRequest("GET", test.path, nil)
			r.Header.Set("X-Actor-Organization-ID", "org-b")
			r.Header.Set("X-Organization-ID", "org-b")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatal("unexpected scope status", w.Code, w.Body.String())
			}
			if test.status == 200 {
				if client.audit.OrganizationID != "org-a" {
					t.Fatal("unverified organization forwarded")
				}
				if strings.Contains(test.path, "customers") {
					if client.scopeType != "organization" || client.scopeID != "org-a" {
						t.Fatal("customer scope escaped tenant")
					}
				} else if client.query.ScopeType != "organization" || client.query.ScopeID != "org-a" {
					t.Fatal("default scope escaped tenant")
				}
			} else if client.audit.ActorID != "" {
				t.Fatal("unauthorized report reached backend")
			}
		})
	}
	for _, roles := range [][]string{{"org_admin"}, {"user"}, {"team_admin"}} {
		w := httptest.NewRecorder()
		Routes(organizationReportHandler("", roles...).WithUsageReporting(&recordingUsageClient{})).ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/usage/report", nil))
		if w.Code != 403 {
			t.Fatal("unscoped or unapproved organization administrator accepted", w.Code)
		}
	}
}

type scopedRequestLogClient struct {
	filter RequestLogFilter
	group  RequestLogGroupFilter
	audit  ManagementAudit
	row    RequestLog
	calls  int
}

func (c *scopedRequestLogClient) ListRequestLogs(_ context.Context, a ManagementAudit, f RequestLogFilter) (RequestLogPage, error) {
	c.calls++
	c.audit = a
	c.filter = f
	return RequestLogPage{Data: []RequestLog{{OrganizationID: f.OrganizationID}}, NextRequestID: "scoped-cursor"}, nil
}
func (c *scopedRequestLogClient) ListRequestLogGroups(_ context.Context, a ManagementAudit, f RequestLogGroupFilter) (RequestLogGroupPage, error) {
	c.calls++
	c.audit = a
	c.group = f
	return RequestLogGroupPage{Data: []RequestLogGroup{}}, nil
}
func (c *scopedRequestLogClient) GetRequestLog(_ context.Context, a ManagementAudit, _ string) (RequestLog, error) {
	c.calls++
	c.audit = a
	return c.row, nil
}
func (c *scopedRequestLogClient) GetRequestLogSettings(_ context.Context, a ManagementAudit) (RequestLogSettings, error) {
	c.calls++
	c.audit = a
	return RequestLogSettings{}, nil
}

func TestOrganizationRequestLogsAlwaysCarryTenant(t *testing.T) {
	for _, test := range []struct {
		path   string
		status int
	}{
		{"/admin/v1/request-logs?user_id=shared-user&limit=1", 200},
		{"/admin/v1/request-logs?organization_id=org-b", 403},
		{"/admin/v1/request-logs/groups?dimension=session&session_id=shared", 200},
		{"/admin/v1/request-logs/groups?dimension=trace&organization_id=org-b", 403},
		{"/admin/v1/request-logs/settings", 200},
	} {
		t.Run(test.path, func(t *testing.T) {
			client := &scopedRequestLogClient{}
			w := httptest.NewRecorder()
			Routes(organizationReportHandler("org-a", "org_admin").WithRequestLogs(client)).ServeHTTP(w, httptest.NewRequest("GET", test.path, nil))
			if w.Code != test.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if test.status != 200 {
				if client.calls != 0 {
					t.Fatal("foreign tenant reached backend")
				}
				return
			}
			if client.audit.OrganizationID != "org-a" {
				t.Fatal("trusted tenant missing")
			}
			if strings.Contains(test.path, "groups") {
				if client.group.OrganizationID != "org-a" {
					t.Fatal("group scope missing")
				}
			} else if !strings.Contains(test.path, "settings") && client.filter.OrganizationID != "org-a" {
				t.Fatal("list scope missing")
			}
		})
	}
	for _, org := range []string{"org-a", "org-b", ""} {
		client := &scopedRequestLogClient{row: RequestLog{RequestID: "shared-id", OrganizationID: org}}
		w := httptest.NewRecorder()
		Routes(organizationReportHandler("org-a", "org_admin").WithRequestLogs(client)).ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/request-logs/shared-id", nil))
		expected := 404
		if org == "org-a" {
			expected = 200
		}
		if w.Code != expected {
			t.Fatal("foreign detail disclosed", w.Code)
		}
	}
}

func TestRemoteReportsUseVerifiedOrganizationMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Actor-Organization-ID") != "org-a" || r.Header.Get("X-Actor-Roles") != "org_admin" || r.Header.Get("Authorization") != "" {
			t.Fatal("trusted tenant metadata missing")
		}
		_ = json.NewEncoder(w).Encode(UsageReport{})
	}))
	t.Cleanup(server.Close)
	_, err := NewRemoteBudgetManagementClient(server.URL, "fixture-service-secret").FilteredUsageReport(t.Context(), ManagementAudit{ActorID: "user-a", OrganizationID: "org-a", Roles: []string{"org_admin", "admin,external"}}, UsageReportQuery{ScopeType: "organization", ScopeID: "org-a"})
	if err != nil {
		t.Fatal(err)
	}
}
