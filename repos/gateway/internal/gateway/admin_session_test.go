package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

type sessionAuthModule struct {
	err error
}

func (sessionAuthModule) Name() string   { return "auth" }
func (sessionAuthModule) Required() bool { return true }
func (m sessionAuthModule) Handle(_ context.Context, req *modules.RequestContext) error {
	if m.err != nil {
		return m.err
	}
	req.UserID = "operator-1"
	req.TeamID = "team-1"
	req.OrganizationID = "org-1"
	req.CredentialID = "credential-1"
	req.CredentialAlias = "console"
	req.Roles = []string{"team_admin", "developer", "team_admin"}
	req.AllowedModels = []string{"model-b", "model-a", "model-a"}
	req.AllowedTools = []string{"search"}
	return nil
}

func TestAdminSessionReturnsSafeCapabilities(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{sessionAuthModule{}}), modelsProvider{}))
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/session", nil)
	request.Header.Set("Authorization", "Bearer must-not-leak")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"user_id":"operator-1"`) || !strings.Contains(body, `"capabilities":["api_docs","inference","team_directory"]`) || !strings.Contains(body, `"allowed_models":["model-a","model-b"]`) {
		t.Fatalf("unexpected session: status=%d body=%s", response.Code, body)
	}
	if strings.Contains(body, "must-not-leak") {
		t.Fatalf("bearer token leaked in session response: %s", body)
	}
}

func TestAdminSessionIncludesAdminCapability(t *testing.T) {
	handler := Routes(NewHandler(modulesPipeline("admin"), modelsProvider{}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/session", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"capabilities":["admin","api_docs","inference","team_directory"]`) {
		t.Fatalf("unexpected admin session: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAdminSessionRejectsInvalidCredential(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{sessionAuthModule{err: modules.ErrUnauthorized}}), modelsProvider{}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/session", nil))
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "api key") && strings.Contains(response.Body.String(), "Bearer") {
		t.Fatalf("unexpected auth failure: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOrganizationConsoleCapabilitiesRequirePinnedOrganization(t *testing.T) {
	for _, test := range []struct {
		role, organization string
		allowed            bool
	}{{"org_admin", "org-a", true}, {"org_admin", "", false}, {"user", "org-a", false}, {"team_admin", "org-a", false}} {
		capabilities := organizationConsoleCapabilities([]string{test.role}, test.organization)
		for _, capability := range []string{"organization_reports", "organization_keys"} {
			found := false
			for _, actual := range capabilities {
				if actual == capability {
					found = true
				}
			}
			if found != test.allowed {
				t.Errorf("role=%s org=%q %s=%v want=%v", test.role, test.organization, capability, found, test.allowed)
			}
		}
		for _, capability := range capabilities {
			if capability == "admin" {
				t.Fatal("organization role received platform capability")
			}
		}
	}
}

func TestOrganizationSessionKeepsLegacyCapabilityEnumStable(t *testing.T) {
	handler := Routes(organizationReportHandler("org-a", "org_admin"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/session", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"capabilities":["api_docs","inference"]`) || !strings.Contains(body, `"organization_capabilities":["organization_keys","organization_reports"]`) {
		t.Fatalf("session capability compatibility lost: %d %s", w.Code, body)
	}
}
