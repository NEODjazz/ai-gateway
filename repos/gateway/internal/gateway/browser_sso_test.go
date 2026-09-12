package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type browserSSOAuthModule struct {
	token string
}

func (*browserSSOAuthModule) Name() string   { return "auth" }
func (*browserSSOAuthModule) Required() bool { return true }
func (m *browserSSOAuthModule) Handle(_ context.Context, request *modules.RequestContext) error {
	if request.APIKey != m.token {
		return modules.ErrUnauthorized
	}
	request.UserID = "sso-user"
	request.Roles = []string{"admin"}
	request.CredentialID = "jwt:sso-user"
	request.APIKey = ""
	return nil
}

func TestBrowserSSOUsesPKCEAndEncryptedSessionCookie(t *testing.T) {
	const accessToken = "signed-access-token"
	var challenge string
	var identity *httptest.Server
	identity = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "authorization-code" || r.Form.Get("client_id") != "console" || base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
			t.Errorf("invalid token exchange: %+v", r.Form)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "token_type": "Bearer", "expires_in": 3600})
	}))
	defer identity.Close()

	sso, err := NewBrowserSSO(BrowserSSOConfig{
		AuthorizationURL: identity.URL + "/authorize", TokenURL: identity.URL + "/token", ClientID: "console",
		RedirectURL: "http://localhost/auth/sso/callback", SessionKey: []byte("0123456789abcdef0123456789abcdef"), SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&browserSSOAuthModule{token: accessToken}}), modelsProvider{}).WithBrowserSSO(sso))

	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/auth/sso/start", nil))
	if start.Code != http.StatusFound {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	authorization, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	challenge = authorization.Query().Get("code_challenge")
	state := authorization.Query().Get("state")
	if authorization.Path != "/authorize" || authorization.Query().Get("code_challenge_method") != "S256" || challenge == "" || state == "" || strings.Contains(start.Header().Get("Set-Cookie"), "signed-access-token") {
		t.Fatalf("invalid authorization redirect: %s", authorization.String())
	}
	stateCookie := start.Result().Cookies()[0]

	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, "/auth/sso/callback?state="+url.QueryEscape(state)+"&code=authorization-code", nil)
	callbackRequest.AddCookie(stateCookie)
	handler.ServeHTTP(callback, callbackRequest)
	if callback.Code != http.StatusFound || callback.Header().Get("Location") != "/ui/" {
		t.Fatalf("callback status=%d location=%s body=%s", callback.Code, callback.Header().Get("Location"), callback.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == browserSSOSessionCookie {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode || strings.Contains(sessionCookie.Value, accessToken) {
		t.Fatalf("invalid session cookie: %+v", sessionCookie)
	}

	session := httptest.NewRecorder()
	sessionRequest := httptest.NewRequest(http.MethodGet, "/admin/v1/session", nil)
	sessionRequest.AddCookie(sessionCookie)
	handler.ServeHTTP(session, sessionRequest)
	if session.Code != http.StatusOK || !strings.Contains(session.Body.String(), `"user_id":"sso-user"`) || strings.Contains(session.Body.String(), accessToken) {
		t.Fatalf("session status=%d body=%s", session.Code, session.Body.String())
	}
}

func TestBrowserSSORejectsTamperedStateAndSession(t *testing.T) {
	sso, err := NewBrowserSSO(BrowserSSOConfig{AuthorizationURL: "http://localhost/authorize", TokenURL: "http://localhost/token", ClientID: "console", RedirectURL: "http://localhost/auth/sso/callback", SessionKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&browserSSOAuthModule{token: "valid"}}), modelsProvider{}).WithBrowserSSO(sso))
	callback := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/auth/sso/callback?state=state&code=code", nil)
	request.AddCookie(&http.Cookie{Name: browserSSOStateCookie, Value: "tampered"})
	handler.ServeHTTP(callback, request)
	if callback.Code != http.StatusBadRequest {
		t.Fatalf("tampered state status=%d", callback.Code)
	}

	session := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/admin/v1/session", nil)
	request.AddCookie(&http.Cookie{Name: browserSSOSessionCookie, Value: "tampered"})
	handler.ServeHTTP(session, request)
	if session.Code != http.StatusUnauthorized {
		body, _ := io.ReadAll(session.Result().Body)
		t.Fatalf("tampered session status=%d body=%s", session.Code, body)
	}
}

func TestBrowserSSOConfigurationValidation(t *testing.T) {
	for _, config := range []BrowserSSOConfig{
		{},
		{AuthorizationURL: "http://identity.example/authorize", TokenURL: "https://identity.example/token", ClientID: "console", RedirectURL: "https://gateway.example/auth/sso/callback", SessionKey: []byte(strings.Repeat("x", 32))},
		{AuthorizationURL: "https://identity.example/authorize", TokenURL: "https://identity.example/token", ClientID: "console", RedirectURL: "https://gateway.example/auth/sso/callback", SessionKey: []byte("short")},
	} {
		if _, err := NewBrowserSSO(config); err == nil {
			t.Fatal("invalid browser SSO config accepted")
		}
	}
}
