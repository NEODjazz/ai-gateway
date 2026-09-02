package gateway

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

const adminUICSP = "default-src 'none'; base-uri 'none'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; script-src 'self'; style-src 'self'"

//go:embed adminui/*
var adminUIAssets embed.FS

func registerAdminUI(mux *http.ServeMux) {
	mux.Handle("GET /ui", adminUISecurityHeaders(false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
	})))
	index := adminUISecurityHeaders(false, adminUIAsset("adminui/index.html", "text/html; charset=utf-8"))
	mux.Handle("GET /ui/{$}", index)
	mux.Handle("GET /ui/assets/app.css", adminUISecurityHeaders(false, adminUIAsset("adminui/assets/app.css", "text/css; charset=utf-8")))
	mux.Handle("GET /ui/assets/app.js", adminUISecurityHeaders(false, adminUIAsset("adminui/assets/app.js", "text/javascript; charset=utf-8")))
	mux.Handle("GET /ui/assets/{path...}", adminUISecurityHeaders(true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/ui/assets/")
		if name == "" || !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		contentType := mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		adminUIAsset("adminui/assets/"+name, contentType).ServeHTTP(w, r)
	})))
	mux.Handle("GET /ui/{path...}", adminUISecurityHeaders(false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= len("/ui/assets/") && r.URL.Path[:len("/ui/assets/")] == "/ui/assets/" {
			http.NotFound(w, r)
			return
		}
		adminUIAsset("adminui/index.html", "text/html; charset=utf-8").ServeHTTP(w, r)
	})))
}

func adminUIAsset(name, contentType string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload, err := adminUIAssets.ReadFile(name)
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	})
}

func adminUISecurityHeaders(immutable bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", adminUICSP)
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
