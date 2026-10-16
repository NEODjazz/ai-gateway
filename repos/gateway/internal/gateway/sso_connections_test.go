package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
)

func TestSSOConnectionRoutingAuthenticatesCookieBeforeCodeExchange(t *testing.T) {
	var exchanged atomic.Int32
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanged.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": "fixture-id", "access_token": "opaque-access", "token_type": "Bearer", "expires_in": 300})
	}))
	defer identity.Close()
	profiles := map[string]*PrivateSSOProfile{}
	for _, id := range []string{"a", "b"} {
		profiles[id] = &PrivateSSOProfile{ID: "profile-" + id, Enabled: true, SessionKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(id, 32))), SSOProfileConfig: SSOProfileConfig{AuthorizationURL: identity.URL + "/authorize", TokenURL: identity.URL + "/token", ClientID: "client-" + id, RedirectURL: "http://ai-gateway.localhost/auth/sso/callback", SessionTTLSeconds: 3600}}
	}
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Management-Token") != "fixture" {
			t.Error("missing service authentication")
		}
		switch r.URL.Path {
		case "/internal/v1/sso/active":
			profile := profiles[r.URL.Query().Get("connection")]
			if profile == nil {
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(profile)
		case "/internal/v1/sso/sessions":
			var input SSOBrowserLogin
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.ProfileID != "profile-a" || input.Token != "fixture-id" || input.Nonce == "" {
				t.Error("connection profile lost at session exchange")
			}
			_ = json.NewEncoder(w).Encode(SSOBrowserSession{Token: "agsso_" + strings.Repeat("s", 43), ExpiresAt: time.Now().Add(time.Hour - time.Second).Unix()})
		default:
			w.WriteHeader(404)
		}
	}))
	defer service.Close()
	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}).WithSSOManagement(NewRemoteManagementClient(service.URL, "fixture")))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/auth/sso/start?connection=a", nil))
	if w.Code != 302 {
		t.Fatal(w.Code, w.Body.String())
	}
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("client_id") != "client-a" {
		t.Fatal("wrong connection selected")
	}
	cookie := w.Result().Cookies()[0]
	if !strings.HasPrefix(cookie.Value, "a.") {
		t.Fatal("connection routing hint absent")
	}
	callback := "/auth/sso/callback?code=fixture&state=" + location.Query().Get("state")
	// Even a routing hint changed to another configured IdP must not send the
	// code there. Both prefix and encrypted purpose are bound to the connection.
	for _, value := range []string{"b." + strings.TrimPrefix(cookie.Value, "a."), "unknown." + strings.TrimPrefix(cookie.Value, "a.")} {
		r := httptest.NewRequest("GET", callback, nil)
		copy := *cookie
		copy.Value = value
		r.AddCookie(&copy)
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code == 302 || exchanged.Load() != 0 {
			t.Fatal("tampered connection exchanged authorization code")
		}
	}
	r := httptest.NewRequest("GET", callback, nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 302 || exchanged.Load() != 1 {
		t.Fatal("valid selected connection failed", w.Code, w.Body.String())
	}
	found := false
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == browserSSOSessionCookie {
			found = strings.HasPrefix(cookie.Value, "a.")
		}
	}
	if !found {
		t.Fatal("session cookie lost verified connection")
	}
}

func TestSSOConnectionHintIsNotOrganizationAuthority(t *testing.T) {
	for _, path := range []string{"/v1/files?connection=b", "/admin/v1/session?connection=b"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("X-Organization-ID", "org-b")
		id, err := requestSSOConnection(r)
		if err != nil || id != "" {
			t.Fatal("public query selected organization", id, err)
		}
	}
	for _, path := range []string{"/auth/sso/start?connection=a&connection=b", "/auth/sso/start?connection=../b", "/auth/sso/start?connection="} {
		if _, err := requestSSOConnection(httptest.NewRequest("GET", path, nil)); err == nil {
			t.Fatal("noncanonical connection hint accepted", path)
		}
	}
}

type connectionSessionAuth struct {
	browserSSOAuthModule
	roles []string
	org   string
}

func (m *connectionSessionAuth) Handle(ctx context.Context, req *modules.RequestContext) error {
	if err := m.browserSSOAuthModule.Handle(ctx, req); err != nil {
		return err
	}
	req.Roles = m.roles
	req.OrganizationID = m.org
	return nil
}
func TestSSOManagementTargetDoesNotSelectAuthenticationConnection(t *testing.T) {
	for _, test := range []struct {
		name, cookie, target string
		roles                []string
		want                 int
	}{
		{"default admin edits tenant", "", "a", []string{"admin"}, 200},
		{"connection admin edits another", "a", "b", []string{"admin"}, 200},
		{"connection admin lists registry", "a", "", []string{"admin"}, 200},
		{"tenant is authenticated then denied", "a", "b", []string{"org_admin"}, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			profiles := map[string]*PrivateSSOProfile{}
			for _, id := range []string{"", "a", "b"} {
				key := id
				if key == "" {
					key = "d"
				}
				profiles[id] = &PrivateSSOProfile{ID: "profile-" + key, Enabled: true, SessionKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(key, 32))), SSOProfileConfig: SSOProfileConfig{ClientID: "console-" + key, AuthorizationURL: "https://idp.example/authorize", TokenURL: "https://idp.example/token", RedirectURL: "http://ai-gateway.localhost/auth/sso/callback", SessionTTLSeconds: 3600}}
			}
			var mu sync.Mutex
			var authConnection, settingsConnection string
			service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Management-Token") != "fixture" {
					t.Error("missing service auth")
				}
				switch r.URL.Path {
				case "/internal/v1/sso/active":
					id := r.URL.Query().Get("connection")
					mu.Lock()
					authConnection = id
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(profiles[id])
				case "/internal/v1/sso/settings":
					mu.Lock()
					settingsConnection = r.URL.Query().Get("connection")
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(SSOSettingsView{Revision: 7})
				case "/internal/v1/sso/connections":
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []SSOConnectionView{}})
				default:
					w.WriteHeader(404)
				}
			}))
			defer service.Close()
			handle := "agsso_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
			sso, err := profiles[test.cookie].browser(false)
			if err != nil {
				t.Fatal(err)
			}
			sso.config.ConnectionID = test.cookie
			sealed, err := sso.seal(browserSSOSessionCookie, browserSSOSession{Token: handle, ExpiresAt: time.Now().Add(time.Hour).Unix()})
			if err != nil {
				t.Fatal(err)
			}
			auth := &connectionSessionAuth{browserSSOAuthModule: browserSSOAuthModule{token: handle}, roles: test.roles, org: "org-a"}
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{auth}), modelsProvider{}).WithIdentityDirectory(&directoryClientStub{}).WithSSOManagement(NewRemoteManagementClient(service.URL, "fixture")))
			path := "/admin/v1/sso/connections"
			if test.target != "" {
				path = "/admin/v1/sso/settings?connection=" + test.target
			}
			r := httptest.NewRequest("GET", path, nil)
			r.AddCookie(&http.Cookie{Name: browserSSOSessionCookie, Value: sealed})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatal(w.Code, w.Body.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if authConnection != test.cookie {
				t.Fatal("management target changed session verification connection")
			}
			if test.want == 200 && test.target != "" && settingsConnection != test.target {
				t.Fatal("management connection selection lost")
			}
		})
	}
}
