package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/api"
	"ai-gateway-gateway/internal/modules"
)

func TestAPIDocsAreDisabledByDefault(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}))
	for _, path := range []string{"/docs", "/docs/", "/docs/_assets/swagger-ui.min.css", "/openapi.yaml"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s: expected 404, got %d", path, recorder.Code)
		}
	}
}

func TestAPIDocsServeEmbeddedAssetsAndCanonicalSpec(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}).WithAPIDocs(false))

	redirect := httptest.NewRecorder()
	handler.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if redirect.Code != http.StatusTemporaryRedirect || redirect.Header().Get("Location") != "/docs/" {
		t.Fatalf("unexpected docs redirect: status=%d location=%q", redirect.Code, redirect.Header().Get("Location"))
	}

	docs := httptest.NewRecorder()
	handler.ServeHTTP(docs, httptest.NewRequest(http.MethodGet, "/docs/", nil))
	if docs.Code != http.StatusOK {
		t.Fatalf("docs status: %d", docs.Code)
	}
	body := docs.Body.String()
	for _, expected := range []string{
		`/docs/_assets/swagger-ui-bundle.js`,
		`url: url`,
		`persistAuthorization: false`,
		`queryConfigEnabled: false`,
		`supportedSubmitMethods: []`,
		`validatorUrl: null`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("docs HTML does not contain %q", expected)
		}
	}
	for _, external := range []string{"cdn.jsdelivr.net", "unpkg.com", "cdnjs.cloudflare.com"} {
		if strings.Contains(body, external) {
			t.Errorf("docs HTML unexpectedly references %q", external)
		}
	}
	assertAPIDocsSecurityHeaders(t, docs.Header())

	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/docs/_assets/swagger-ui.min.css", nil))
	if asset.Code != http.StatusOK || asset.Body.Len() == 0 {
		t.Fatalf("embedded asset: status=%d bytes=%d", asset.Code, asset.Body.Len())
	}
	if got := asset.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("unexpected asset cache policy: %q", got)
	}

	spec := httptest.NewRecorder()
	handler.ServeHTTP(spec, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))
	if spec.Code != http.StatusOK || !bytes.Equal(spec.Body.Bytes(), api.OpenAPI) {
		t.Fatalf("served OpenAPI differs from canonical spec: status=%d", spec.Code)
	}
	if got := spec.Header().Get("Content-Type"); got != "application/yaml; charset=utf-8" {
		t.Fatalf("unexpected spec content type: %q", got)
	}
}

func TestAPIDocsTryItOutMustBeExplicitlyEnabled(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}).WithAPIDocs(true))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/docs/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("docs status: %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `supportedSubmitMethods: ['get', 'put', 'post', 'delete', 'options', 'head', 'patch', 'trace']`) {
		t.Fatalf("Try it out methods were not enabled: %s", body)
	}
}

func TestAPIDocsPathsHaveBoundedObservabilityLabels(t *testing.T) {
	for _, path := range []string{"/docs", "/docs/", "/docs/_assets/arbitrary-user-value.js"} {
		if got := metricPath(path); got != "/docs/{asset}" {
			t.Errorf("metricPath(%q) = %q", path, got)
		}
		if !isInfrastructurePath(path) {
			t.Errorf("%q must be excluded from tracing", path)
		}
	}
	if got := metricPath("/openapi.yaml"); got != "/openapi.yaml" || !isInfrastructurePath("/openapi.yaml") {
		t.Fatalf("unexpected OpenAPI observability mapping: %q", got)
	}
}

func assertAPIDocsSecurityHeaders(t *testing.T, headers http.Header) {
	t.Helper()
	if got := headers.Get("Content-Security-Policy"); got != apiDocsCSP {
		t.Errorf("unexpected CSP: %q", got)
	}
	if got := headers.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("unexpected X-Content-Type-Options: %q", got)
	}
	if got := headers.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("unexpected X-Frame-Options: %q", got)
	}
	if got := headers.Get("Cache-Control"); got != "no-store" {
		t.Errorf("unexpected cache policy: %q", got)
	}
}
