package gateway

import (
	"embed"
	"encoding/json"
	"html"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
)

var versionedAsset = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.[a-z0-9]+$`)

const adminUICSP = "default-src 'none'; base-uri 'none'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data: blob:; media-src blob:; script-src 'self'; style-src 'self'; worker-src 'self'"

//go:embed adminui/*
var adminUIAssets embed.FS

func registerAdminUI(mux *http.ServeMux, origins ...string) {
	secure := func(immutable bool, next http.Handler) http.Handler {
		return adminUISecurityHeaders(immutable, next, origins...)
	}
	mux.Handle("GET /ui", secure(false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
	})))
	index := secure(false, adminUIIndex(origins))
	mux.Handle("GET /ui/{$}", index)
	mux.Handle("GET /ui/assets/app.css", secure(false, adminUIAsset("adminui/assets/app.css", "text/css; charset=utf-8")))
	mux.Handle("GET /ui/assets/app.js", secure(false, adminUIAsset("adminui/assets/app.js", "text/javascript; charset=utf-8")))
	mux.Handle("GET /ui/assets/{path...}", secure(true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/ui/assets/")
		if name == "" || !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		contentType := mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		secure(versionedAsset.MatchString(name), adminUIAsset("adminui/assets/"+name, contentType)).ServeHTTP(w, r)
	})))
	mux.Handle("GET /ui/{path...}", secure(false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= len("/ui/assets/") && r.URL.Path[:len("/ui/assets/")] == "/ui/assets/" {
			http.NotFound(w, r)
			return
		}
		adminUIIndex(origins).ServeHTTP(w, r)
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

func adminUISecurityHeaders(immutable bool, next http.Handler, origins ...string) http.Handler {
	csp := adminUICSP
	if len(origins) > 0 {
		csp = strings.Replace(csp, "connect-src 'self'", "connect-src 'self' "+strings.Join(origins, " "), 1)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(self)")
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

func adminUIIndex(origins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload, err := adminUIAssets.ReadFile("adminui/index.html")
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		if len(origins) > 0 {
			encoded, err := json.Marshal(origins)
			if err != nil {
				http.Error(w, "UI configuration is unavailable", http.StatusInternalServerError)
				return
			}
			meta := `<meta name="ai-gateway-playground-origins" content="` + html.EscapeString(string(encoded)) + `">`
			payload = []byte(strings.Replace(string(payload), "</head>", meta+"\n</head>", 1))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	})
}
