package gateway

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
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
	for _, expected := range []string{"AI Gateway Console", "/ui/assets/app.css", "/ui/assets/app.js", `<div id="root"></div>`} {
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
	for _, pattern := range []string{"adminui/assets/ProjectsPage-*.js", "adminui/assets/GuardrailsPage-*.js"} {
		chunks, err := fs.Glob(adminUIAssets, pattern)
		if err != nil || len(chunks) != 1 {
			t.Fatalf("expected one embedded page chunk for %q, got %v (error %v)", pattern, chunks, err)
		}
		chunk := httptest.NewRecorder()
		handler.ServeHTTP(chunk, httptest.NewRequest(http.MethodGet, "/ui/assets/"+path.Base(chunks[0]), nil))
		if chunk.Code != http.StatusOK || chunk.Body.Len() == 0 || chunk.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
			t.Fatalf("chunk asset %s: status=%d type=%q bytes=%d", chunks[0], chunk.Code, chunk.Header().Get("Content-Type"), chunk.Body.Len())
		}
		assertAdminUISecurityHeaders(t, chunk.Header(), "public, max-age=31536000, immutable")
	}
	deepLink := httptest.NewRecorder()
	handler.ServeHTTP(deepLink, httptest.NewRequest(http.MethodGet, "/ui/providers", nil))
	if deepLink.Code != http.StatusOK || !strings.Contains(deepLink.Body.String(), `<div id="root"></div>`) {
		t.Fatalf("SPA deep link was not served: status=%d body=%s", deepLink.Code, deepLink.Body.String())
	}
	missingAsset := httptest.NewRecorder()
	handler.ServeHTTP(missingAsset, httptest.NewRequest(http.MethodGet, "/ui/assets/missing.js", nil))
	if missingAsset.Code != http.StatusNotFound {
		t.Fatalf("missing asset returned %d instead of 404", missingAsset.Code)
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

func TestAdminUIBundleContainsRouteBasedManagementConsole(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}).WithAdminUI())
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/ui/assets/app.js", nil))
	body := asset.Body.String()
	for _, expected := range []string{"/overview", "/providers", "/deployments", "/model-groups", "input_cost_per_1m", "upstream_model", "control-plane-conflict"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("admin UI asset missing %q", expected)
		}
	}
	if strings.Contains(body, "Not cataloged") || strings.Contains(body, "Add pricing") {
		t.Fatalf("admin UI retained split catalog actions")
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
