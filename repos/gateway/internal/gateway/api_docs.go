package gateway

import (
	"net/http"

	"ai-gateway-gateway/api"
	specui "github.com/oaswrap/spec-ui"
	specuiconfig "github.com/oaswrap/spec-ui/config"
	"github.com/oaswrap/spec-ui/swaggeruiemb"
)

const apiDocsCSP = "default-src 'none'; base-uri 'none'; connect-src 'self'; font-src 'self'; form-action 'none'; frame-ancestors 'none'; img-src 'self' data:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'"

type apiDocsConfig struct {
	enabled         bool
	tryItOutEnabled bool
}

func registerAPIDocs(mux *http.ServeMux, cfg apiDocsConfig) {
	supportedMethods := "[]"
	if cfg.tryItOutEnabled {
		supportedMethods = "['get', 'put', 'post', 'delete', 'options', 'head', 'patch', 'trace']"
	}
	docs := specui.NewHandler(
		specui.WithTitle("AI Gateway API"),
		specui.WithDocsPath("/docs/"),
		specui.WithSpecPath("/openapi.yaml"),
		specui.WithAssetsPath("/docs/_assets"),
		swaggeruiemb.WithUI(specuiconfig.SwaggerUI{
			Layout:                   specuiconfig.SwaggerLayoutStandalone,
			DefaultModelsExpandDepth: 1,
			UIConfig: map[string]string{
				"persistAuthorization":   "false",
				"queryConfigEnabled":     "false",
				"supportedSubmitMethods": supportedMethods,
			},
		}),
	)

	mux.Handle("GET /docs", apiDocsSecurityHeaders(false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusTemporaryRedirect)
	})))
	mux.Handle("GET /docs/{$}", apiDocsSecurityHeaders(false, docs.Docs()))
	mux.Handle("GET /docs/_assets/", apiDocsSecurityHeaders(true, docs.Assets()))
	mux.Handle("GET /openapi.yaml", apiDocsSecurityHeaders(false, http.HandlerFunc(serveOpenAPI)))
}

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(api.OpenAPI)
}

func apiDocsSecurityHeaders(immutable bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", apiDocsCSP)
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if immutable {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
