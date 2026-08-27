package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

func TestAdminUIIsDisabledUnlessEnabledOnHandler(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected disabled UI to return 404, got %d", response.Code)
	}
}

func TestAdminUIServesEmbeddedSameOriginAssets(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}).WithAdminUI())
	redirect := httptest.NewRecorder()
	handler.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if redirect.Code != http.StatusTemporaryRedirect || redirect.Header().Get("Location") != "/ui/" {
		t.Fatalf("unexpected redirect: status=%d location=%q", redirect.Code, redirect.Header().Get("Location"))
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("UI status=%d body=%s", page.Code, page.Body.String())
	}
	for _, expected := range []string{"AI Gateway Console", "/ui/assets/app.css?v=22", "/ui/assets/app.js?v=22", "Admin bearer token", "Overview", "Usage &amp; spend", "Customer insights", "customer-filter-form", "customer-budgets-table", "customer-keys-table", "Request logs", "Virtual key", "request-log-credential", "Routing", "Playground", "Max completion tokens", "Temperature (optional)", "Virtual keys", "Users", "Teams", "Organizations", "organizations-table", "organization-dialog", "organization-team-dialog", "Projects &amp; access", "projects-table", "access-groups-table", "project-dialog", "access-group-dialog", "AI Hub &amp; optimization", "ai-hub-table", "cost-recommendations-table", "Scoped RBAC", "users-table", "teams-table", "membership-dialog", "Models", "Providers &amp; models", "providers-table", "credentials-table", "deployments-table", "model-groups-table", "provider-dialog", "credential-dialog", "deployment-dialog", "deployment-health-dialog", "model-group-dialog", "Write-only secrets", "Guardrails", "guardrails-table", "guardrail-monitor-total", "guardrail-events-table", "Metadata only", "Compliance playground", "compliance-form", "guardrail-dialog", "MCP registry", "mcp-servers-table", "Agent templates", "tool-policies-table", "agent-profiles-table", "tool-policy-dialog", "agent-profile-dialog", "Caching &amp; logging", "cache-exact-hits", "cache-semantic-hits", "logging-table", "logging-dialog", "mcp-toolsets-table", "mcp-server-dialog", "mcp-toolset-dialog", "Budgets", "Audit log", "routing-cards", "playground-form", "usage-chart", "usage-models-table", "request-log-filter-form", "request-logs-table", "Content storage off", "Add catalog entry", "Create budget", "Create virtual key", "key-alias", "key-dialog", "issued-key-dialog", "model-dialog", "budget-dialog", "request-log-dialog", "confirm-dialog"} {
		if !strings.Contains(page.Body.String(), expected) {
			t.Errorf("UI HTML missing %q", expected)
		}
	}
	for _, external := range []string{"https://", "http://", "cdn.", "unpkg.com"} {
		if strings.Contains(page.Body.String(), external) {
			t.Errorf("UI HTML unexpectedly references %q", external)
		}
	}
	assertAdminUISecurityHeaders(t, page.Header(), "no-store")

	for path, contentType := range map[string]string{"/ui/assets/app.css": "text/css; charset=utf-8", "/ui/assets/app.js": "text/javascript; charset=utf-8"} {
		asset := httptest.NewRecorder()
		handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, path, nil))
		if asset.Code != http.StatusOK || asset.Body.Len() == 0 || asset.Header().Get("Content-Type") != contentType {
			t.Errorf("asset %s: status=%d type=%q bytes=%d", path, asset.Code, asset.Header().Get("Content-Type"), asset.Body.Len())
		}
		assertAdminUISecurityHeaders(t, asset.Header(), "no-store")
	}
}

func TestAdminUIPathsHaveBoundedObservabilityLabels(t *testing.T) {
	for _, path := range []string{"/ui", "/ui/", "/ui/assets/arbitrary-value.js"} {
		if got := metricPath(path); got != "/ui/{asset}" {
			t.Errorf("metricPath(%q)=%q", path, got)
		}
		if !isInfrastructurePath(path) {
			t.Errorf("%q must be excluded from inference tracing", path)
		}
	}
}

func TestAdminUIUsesCanonicalProviderIdentityForModelCatalogActions(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}).WithAdminUI())
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/ui/assets/app.js", nil))
	body := asset.Body.String()
	for _, expected := range []string{"modelProviderAliases", "canonicalModelProvider", "Edit model metadata", `remove.textContent = "Delete"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("admin UI asset missing %q", expected)
		}
	}
	if strings.Contains(body, "Not cataloged") || strings.Contains(body, "Add pricing") {
		t.Fatalf("admin UI retained split catalog actions")
	}
}

func TestAdminUIUsesCanonicalUsageDimensions(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}).WithAdminUI())
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if !strings.Contains(page.Body.String(), "Usage by provider") || strings.Contains(page.Body.String(), "Usage by endpoint") {
		t.Fatalf("admin UI retained endpoint-labelled provider aggregation")
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/ui/assets/app.js", nil))
	if !strings.Contains(asset.Body.String(), "log.upstream_model") {
		t.Fatalf("admin UI does not distinguish the upstream model in request logs")
	}
}

func assertAdminUISecurityHeaders(t *testing.T, headers http.Header, cacheControl string) {
	t.Helper()
	if got := headers.Get("Content-Security-Policy"); got != adminUICSP {
		t.Errorf("unexpected CSP: %q", got)
	}
	if got := headers.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("unexpected frame policy: %q", got)
	}
	if got := headers.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("unexpected content-type policy: %q", got)
	}
	if got := headers.Get("Cache-Control"); got != cacheControl {
		t.Errorf("unexpected cache policy: %q", got)
	}
}
