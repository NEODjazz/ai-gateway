package gateway

import (
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

func TestPlaygroundOriginsValidateWithoutEchoingInput(t *testing.T) {
	for _, value := range []string{"*", "https://*.example.test", "https:", "https://", "javascript:alert(1)", "http://example.test", "https://user:secret@example.test", "https://example.test/v1", "https://example.test?key=secret", "https://example.test?", "https://example.test#fragment", "https://example.test#", "https://example.test:0", "https://example.test:65536", "https://example.test.", "https://example_test", "https://example.test/;script-src https:", "https://[fe80::1%25en0]", "https://127.1", "https://0x7f000001", "https://host.123", "https://[example]", "https://[::ffff:127.0.0.1]"} {
		t.Run(value, func(t *testing.T) {
			_, err := (Handler{}).WithPlaygroundOrigins([]string{value})
			if err == nil || strings.Contains(err.Error(), value) || strings.Contains(err.Error(), "secret") {
				t.Fatal("invalid origin was accepted or echoed")
			}
		})
	}
	if _, err := (Handler{}).WithPlaygroundOrigins(make([]string, 17)); err == nil {
		t.Fatal("unbounded origin list accepted")
	}
	for _, test := range []struct{ input, want string }{{" https://EXAMPLE.test:443/ ", "https://example.test"}, {"https://example.test:8443", "https://example.test:8443"}, {"http://127.0.0.1:8081", "http://127.0.0.1:8081"}, {"http://localhost:80", "http://localhost"}, {"http://[::1]:8081/", "http://[::1]:8081"}} {
		got, err := normalizePlaygroundOrigin(test.input)
		if err != nil || got != test.want {
			t.Fatalf("normalize=%q error=%v, want %q", got, err, test.want)
		}
	}
}

func TestPlaygroundOriginsScopeOnlyConnectionsAndExposePublicConfiguration(t *testing.T) {
	values := []string{"https://gateway.example.test:443/", "https://gateway.example.test", "http://127.0.0.1:8081"}
	h, err := NewHandler(modules.NewPipeline(nil), modelsProvider{}).WithAdminUI().WithPlaygroundOrigins(values)
	if err != nil {
		t.Fatal(err)
	}
	values[0] = "https://changed.example.test"
	handler := Routes(h)
	for _, path := range []string{"/ui/", "/ui/playground", "/ui/assets/app.js"} {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		if r.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, r.Code)
		}
		want := strings.Replace(adminUICSP, "connect-src 'self'", "connect-src 'self' https://gateway.example.test http://127.0.0.1:8081", 1)
		if r.Header().Get("Content-Security-Policy") != want || r.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("origin trust escaped connection scope or changed CORS")
		}
		if path != "/ui/assets/app.js" {
			body := html.UnescapeString(r.Body.String())
			if !strings.Contains(body, `name="ai-gateway-playground-origins" content="["https://gateway.example.test","http://127.0.0.1:8081"]"`) || r.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("trusted origins were not safely exposed in uncached page metadata")
			}
		}
	}
}
