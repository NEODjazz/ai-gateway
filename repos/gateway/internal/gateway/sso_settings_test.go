package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type ssoClientStub struct {
	SSOManagementClient
	active       *PrivateSSOProfile
	test         PrivateSSOTest
	view         SSOSettingsView
	err          error
	action       string
	verification SSOTestVerification
}

func (s *ssoClientStub) ActiveSSO(context.Context, ManagementAudit) (*PrivateSSOProfile, error) {
	return s.active, s.err
}
func (s *ssoClientStub) TestSSOProfile(context.Context, ManagementAudit) (PrivateSSOTest, error) {
	return s.test, s.err
}
func (s *ssoClientStub) VerifySSOTest(_ context.Context, _ ManagementAudit, input SSOTestVerification) error {
	s.verification = input
	return s.err
}
func (s *ssoClientStub) GetSSOSettings(context.Context, ManagementAudit) (SSOSettingsView, error) {
	return s.view, s.err
}
func (s *ssoClientStub) ChangeSSO(_ context.Context, _ ManagementAudit, input SSOActionInput) (SSOSettingsView, error) {
	s.action = input.Action
	return s.view, s.err
}
func (s *ssoClientStub) SaveSSODraft(_ context.Context, _ ManagementAudit, input SSODraftInput) (SSOSettingsView, error) {
	s.action = "draft.save"
	return s.view, s.err
}

type ssoJWTAdminModule struct{ managementAuthModule }

func (m ssoJWTAdminModule) Handle(ctx context.Context, req *modules.RequestContext) error {
	if err := m.managementAuthModule.Handle(ctx, req); err != nil {
		return err
	}
	req.Metadata = map[string]string{"auth.method": "jwt"}
	return nil
}
func TestSSOSettingsRequireGlobalAdminAuditAndKeyForTrustChanges(t *testing.T) {
	for _, test := range []struct {
		name      string
		auth      modules.Module
		action    string
		want      int
		auditFail bool
	}{
		{"admin key", managementAuthModule{roles: []string{"admin"}}, "activate", 200, false},
		{"team admin", managementAuthModule{roles: []string{"team_admin"}}, "activate", 403, false},
		{"JWT admin", ssoJWTAdminModule{managementAuthModule{roles: []string{"admin"}}}, "activate", 403, false},
		{"unknown action", managementAuthModule{roles: []string{"admin"}}, "reset", 400, false},
		{"audit failure", managementAuthModule{roles: []string{"admin"}}, "activate", 503, true},
		{"missing audit", managementAuthModule{roles: []string{"admin"}}, "activate", 503, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &ssoClientStub{}
			audit := &recordingAuditClient{}
			if test.auditFail {
				audit.appendErr = errors.New("audit offline")
			}
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{test.auth}), modelsProvider{}).WithIdentityDirectory(&directoryClientStub{}).WithSSOManagement(client).WithAudit(audit))
			if test.name == "missing audit" {
				handler = Routes(NewHandler(modules.NewPipeline([]modules.Module{test.auth}), modelsProvider{}).WithIdentityDirectory(&directoryClientStub{}).WithSSOManagement(client))
			}
			r := httptest.NewRequest("POST", "/admin/v1/sso/action", strings.NewReader(`{"action":"`+test.action+`","expected_revision":2}`))
			r.Header.Set("Authorization", "Bearer fixture")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("status %d body=%s", w.Code, w.Body.String())
			}
			if test.want != 200 && client.action != "" {
				t.Fatal("unauthorized trust change reached Auth")
			}
		})
	}
}
func TestSSOManagedProfileChangesCookieTrustAndFailsClosed(t *testing.T) {
	handle := "agsso_" + strings.Repeat("s", 43)
	profile := &PrivateSSOProfile{ID: "active", SSOProfileConfig: SSOProfileConfig{AuthorizationURL: "https://idp.example/authorize", TokenURL: "https://idp.example/token", ClientID: "console", RedirectURL: "https://gateway.example/auth/sso/callback", SessionTTLSeconds: 3600}, Enabled: true, SessionKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))}
	client := &ssoClientStub{active: profile}
	sso, err := profile.browser(false)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sso.seal(browserSSOSessionCookie, browserSSOSession{Token: handle, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&browserSSOAuthModule{token: handle}}), modelsProvider{}).WithSSOManagement(client))
	request := func(header string) int {
		r := httptest.NewRequest("GET", "/admin/v1/session", nil)
		r.AddCookie(&http.Cookie{Name: browserSSOSessionCookie, Value: sealed})
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if request("") != 200 {
		t.Fatal("managed cookie rejected")
	}
	profile.SessionKey = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("n", 32)))
	if request("") != 401 {
		t.Fatal("old cookie accepted after rotation")
	}
	client.err = errors.New("Auth offline")
	if request("") != 503 {
		t.Fatal("profile outage failed open")
	}
	if request("Bearer "+handle) != 200 {
		t.Fatal("explicit API key blocked by stale cookie or SSO outage")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "/auth/sso/logout", nil))
	if w.Code != 204 || len(w.Result().Cookies()) != 1 {
		t.Fatal("logout unavailable during outage")
	}
}
func TestSSODraftTestUsesPKCEAndNeverCreatesSession(t *testing.T) {
	var challenge string
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("redirect_uri") != "http://ai-gateway.localhost/auth/sso/test/callback" || base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
			t.Error("test PKCE or callback mismatch")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": "fixture-jwt", "access_token": "opaque-access", "token_type": "Bearer", "expires_in": 300})
	}))
	defer identity.Close()
	profile := &PrivateSSOProfile{ID: "draft-1", Enabled: true, SessionKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), SSOProfileConfig: SSOProfileConfig{AuthorizationURL: identity.URL + "/authorize", TokenURL: identity.URL + "/token", RedirectURL: "http://ai-gateway.localhost/auth/sso/callback", ClientID: "console"}}
	ticket := strings.Repeat("a", 43)
	digest := sha256.Sum256([]byte(ticket))
	test := PrivateSSOTest{Profile: profile}
	test.Attempt = &struct {
		TicketHash string `json:"ticket_hash"`
		ExpiresAt  int64  `json:"expires_at"`
		Status     string `json:"status"`
	}{hex.EncodeToString(digest[:]), time.Now().Add(time.Minute).Unix(), "running"}
	client := &ssoClientStub{test: test}
	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}).WithSSOManagement(client))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/auth/sso/test/start?ticket="+ticket, nil))
	if w.Code != 302 {
		t.Fatalf("start=%d", w.Code)
	}
	location, _ := url.Parse(w.Header().Get("Location"))
	challenge = location.Query().Get("code_challenge")
	r := httptest.NewRequest("GET", "/auth/sso/test/callback?code=fixture&state="+location.Query().Get("state"), nil)
	r.AddCookie(w.Result().Cookies()[0])
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 302 || client.verification.ProfileID != "draft-1" || client.verification.Ticket != ticket || client.verification.Token != "fixture-jwt" {
		t.Fatalf("proof submission failed: %d", w.Code)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == browserSSOSessionCookie {
			t.Fatal("test overwrote active browser session")
		}
	}
	profile.ID = "edited-draft"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("edited draft reused old proof")
	}
}
func TestSSOManagementWireRedactsSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Management-Token") != "fixture-shared" || r.Header.Get("X-Actor-Roles") != "admin" {
			t.Error("missing service authentication")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"revision": 1, "draft": map[string]any{"issuer": "https://idp.example", "client_secret": "hidden-secret", "session_key": "hidden-key", "client_secret_configured": true}})
	}))
	defer server.Close()
	client := NewRemoteManagementClient(server.URL, "fixture-shared")
	view, err := client.GetSSOSettings(context.Background(), ManagementAudit{RequestID: "fixture", ActorID: "admin", CredentialID: "key", Roles: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "hidden-") {
		t.Fatal("private fields returned to admin UI")
	}
}

type ssoSessionClientStub struct {
	*ssoClientStub
	login   SSOBrowserLogin
	session SSOBrowserSession
	revoked string
}

func (s *ssoSessionClientStub) CreateSSOBrowserSession(_ context.Context, _ ManagementAudit, login SSOBrowserLogin) (SSOBrowserSession, error) {
	s.login = login
	return s.session, s.err
}
func (s *ssoSessionClientStub) RevokeSSOBrowserSession(_ context.Context, _ ManagementAudit, token string) error {
	s.revoked = token
	return s.err
}

func TestManagedBrowserSSOUsesIDTokenAndOpaqueServerSession(t *testing.T) {
	var nonce string
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": "signed-browser-id-token", "access_token": "opaque-access-token", "token_type": "Bearer", "expires_in": 60})
	}))
	defer identity.Close()
	profile := &PrivateSSOProfile{ID: "connection-1", Enabled: true, SessionKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), SSOProfileConfig: SSOProfileConfig{AuthorizationURL: identity.URL + "/authorize", TokenURL: identity.URL + "/token", ClientID: "console", RedirectURL: "http://localhost/auth/sso/callback", SessionTTLSeconds: 3600}}
	handle := "agsso_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
	client := &ssoSessionClientStub{ssoClientStub: &ssoClientStub{active: profile}, session: SSOBrowserSession{Token: handle, ExpiresAt: time.Now().Add(55 * time.Minute).Unix()}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&browserSSOAuthModule{token: handle}}), modelsProvider{}).WithSSOManagement(client))
	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest("GET", "/auth/sso/start", nil))
	if start.Code != 302 {
		t.Fatal("login start failed")
	}
	location, _ := url.Parse(start.Header().Get("Location"))
	nonce = location.Query().Get("nonce")
	if len(nonce) != 43 {
		t.Fatal("OIDC nonce absent")
	}
	r := httptest.NewRequest("GET", "/auth/sso/callback?code=fixture&state="+location.Query().Get("state"), nil)
	r.AddCookie(start.Result().Cookies()[0])
	callback := httptest.NewRecorder()
	handler.ServeHTTP(callback, r)
	if callback.Code != 302 || client.login.Token != "signed-browser-id-token" || client.login.Nonce != nonce || client.login.AccessToken != "opaque-access-token" || client.login.ProfileID != profile.ID {
		t.Fatalf("ID token not sent to Auth: status=%d", callback.Code)
	}
	sso, err := profile.browser(false)
	if err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for _, value := range callback.Result().Cookies() {
		if value.Name == browserSSOSessionCookie {
			cookie = value
		}
	}
	if cookie == nil {
		t.Fatal("session cookie absent")
	}
	var session browserSSOSession
	if err := sso.open(browserSSOSessionCookie, cookie.Value, &session); err != nil || session.Token != handle {
		t.Fatal("cookie contains upstream token")
	}
	if session.ExpiresAt < time.Now().Add(50*time.Minute).Unix() {
		t.Fatal("server session bounded by upstream access expiry")
	}
	r = httptest.NewRequest("GET", "/admin/v1/session", nil)
	r.AddCookie(cookie)
	current := httptest.NewRecorder()
	handler.ServeHTTP(current, r)
	if current.Code != 200 {
		t.Fatal("opaque session not used for authorization")
	}
	r = httptest.NewRequest("POST", "/auth/sso/logout", nil)
	r.AddCookie(cookie)
	logout := httptest.NewRecorder()
	handler.ServeHTTP(logout, r)
	if logout.Code != 204 || client.revoked != handle {
		t.Fatal("logout did not revoke server session")
	}
	client.err = errors.New("Auth unavailable")
	logout = httptest.NewRecorder()
	handler.ServeHTTP(logout, r)
	if logout.Code != 503 || len(logout.Result().Cookies()) != 1 {
		t.Fatal("logout hid revocation failure or retained cookie")
	}
}

func TestSSOVerifiedIdentityApprovalSurvivesAuthProxy(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprint(approved), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{"revision": 1, "verified_identity": map[string]any{"issuer": "https://identity.example", "subject": "verified-subject", "audience": "console", "user_id": "", "roles": []string{}, "verified_at": 1, "approved": approved}})
			}))
			t.Cleanup(server.Close)
			client := NewRemoteManagementClient(server.URL, "fixture-shared")
			view, err := client.GetSSOSettings(context.Background(), ManagementAudit{ActorID: "admin", Roles: []string{"admin"}})
			if err != nil || view.VerifiedIdentity == nil || view.VerifiedIdentity.Approved != approved {
				t.Fatalf("approval lost: %+v err=%v", view, err)
			}
			body, err := json.Marshal(view)
			if err != nil || !strings.Contains(string(body), `"approved":`+strconv.FormatBool(approved)) {
				t.Fatalf("approval omitted from browser view: %s err=%v", body, err)
			}
		})
	}
}
