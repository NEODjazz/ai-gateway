package gateway

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
)

const (
	browserSSOStateCookie   = "ai_gateway_sso_state"
	browserSSOSessionCookie = "ai_gateway_sso_session"
	browserSSOMaxTokenBytes = 2800
)

type BrowserSSOConfig struct {
	AuthorizationURL string
	TokenURL         string
	ClientID         string
	ClientSecret     string
	RedirectURL      string
	Scopes           []string
	SessionKey       []byte
	SessionTTL       time.Duration
}

type BrowserSSO struct {
	config BrowserSSOConfig
	aead   cipher.AEAD
	client *http.Client
	now    func() time.Time
}

type browserSSOState struct {
	State     string `json:"state"`
	Verifier  string `json:"verifier"`
	ExpiresAt int64  `json:"expires_at"`
}

type browserSSOSession struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

func NewBrowserSSO(config BrowserSSOConfig) (*BrowserSSO, error) {
	if !validBrowserSSOURL(config.AuthorizationURL) || !validBrowserSSOURL(config.TokenURL) || !validBrowserSSOURL(config.RedirectURL) || strings.TrimSpace(config.ClientID) == "" || len(config.ClientID) > 512 || len(config.ClientSecret) > 4096 || len(config.SessionKey) < 32 {
		return nil, errors.New("invalid browser SSO configuration")
	}
	if len(config.Scopes) == 0 {
		config.Scopes = []string{"openid", "profile", "email"}
	}
	if config.SessionTTL <= 0 || config.SessionTTL > 24*time.Hour {
		config.SessionTTL = 8 * time.Hour
	}
	key := sha256.Sum256(config.SessionKey)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &BrowserSSO{config: config, aead: aead, client: &http.Client{Timeout: 10 * time.Second}, now: time.Now}, nil
}

func validBrowserSSOURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.Hostname() == "" {
		return false
	}
	return parsed.Scheme == "https" || parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost" || parsed.Hostname() == "::1")
}

func (h Handler) GetBrowserSSOConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"enabled": h.browserSSO != nil, "start_url": "/auth/sso/start"})
}

func (h Handler) StartBrowserSSO(w http.ResponseWriter, r *http.Request) {
	if h.browserSSO == nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	state, err := randomBrowserSSOValue(32)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "sso_unavailable", "browser sign-in is unavailable")
		return
	}
	verifier, err := randomBrowserSSOValue(48)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "sso_unavailable", "browser sign-in is unavailable")
		return
	}
	sealed, err := h.browserSSO.seal(browserSSOStateCookie, browserSSOState{State: state, Verifier: verifier, ExpiresAt: h.browserSSO.now().Add(5 * time.Minute).Unix()})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "sso_unavailable", "browser sign-in is unavailable")
		return
	}
	h.browserSSO.setCookie(w, browserSSOStateCookie, sealed, "/auth/sso", 5*time.Minute, http.SameSiteLaxMode)
	authorization, _ := url.Parse(h.browserSSO.config.AuthorizationURL)
	query := authorization.Query()
	query.Set("response_type", "code")
	query.Set("client_id", h.browserSSO.config.ClientID)
	query.Set("redirect_uri", h.browserSSO.config.RedirectURL)
	query.Set("scope", strings.Join(h.browserSSO.config.Scopes, " "))
	query.Set("state", state)
	challenge := sha256.Sum256([]byte(verifier))
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	query.Set("code_challenge_method", "S256")
	authorization.RawQuery = query.Encode()
	http.Redirect(w, r, authorization.String(), http.StatusFound)
}

func (h Handler) CompleteBrowserSSO(w http.ResponseWriter, r *http.Request) {
	if h.browserSSO == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	if len(query["state"]) != 1 || len(query["code"]) != 1 || len(query["error"]) > 1 {
		writeError(w, http.StatusBadRequest, "invalid_sso_callback", "browser sign-in callback is invalid")
		return
	}
	cookie, err := r.Cookie(browserSSOStateCookie)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_sso_state", "browser sign-in state is invalid")
		return
	}
	var state browserSSOState
	if err := h.browserSSO.open(browserSSOStateCookie, cookie.Value, &state); err != nil || state.ExpiresAt < h.browserSSO.now().Unix() || state.State == "" || state.State != r.URL.Query().Get("state") || state.Verifier == "" {
		writeError(w, http.StatusBadRequest, "invalid_sso_state", "browser sign-in state is invalid")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 4096 || r.URL.Query().Get("error") != "" {
		writeError(w, http.StatusBadRequest, "invalid_sso_callback", "browser sign-in callback is invalid")
		return
	}
	token, expiresIn, err := h.browserSSO.exchange(r.Context(), code, state.Verifier)
	if err != nil {
		writeError(w, http.StatusBadGateway, "sso_exchange_failed", "identity provider token exchange failed")
		return
	}
	req := modules.RequestContext{APIKey: token, RequestID: requestID(r)}
	if err := h.pipeline.Run(r.Context(), &req); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "identity provider credential is not authorized")
		return
	}
	ttl := min(h.browserSSO.config.SessionTTL, expiresIn)
	sealed, err := h.browserSSO.seal(browserSSOSessionCookie, browserSSOSession{Token: token, ExpiresAt: h.browserSSO.now().Add(ttl).Unix()})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "sso_unavailable", "browser sign-in is unavailable")
		return
	}
	h.browserSSO.clearCookie(w, browserSSOStateCookie, "/auth/sso", http.SameSiteLaxMode)
	h.browserSSO.setCookie(w, browserSSOSessionCookie, sealed, "/", ttl, http.SameSiteStrictMode)
	http.Redirect(w, r, "/ui/", http.StatusFound)
}

func (h Handler) EndBrowserSSO(w http.ResponseWriter, _ *http.Request) {
	if h.browserSSO != nil {
		h.browserSSO.clearCookie(w, browserSSOSessionCookie, "/", http.SameSiteStrictMode)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *BrowserSSO) exchange(ctx context.Context, code, verifier string) (string, time.Duration, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {s.config.ClientID}, "code": {code}, "redirect_uri": {s.config.RedirectURL}, "code_verifier": {verifier}}
	if s.config.ClientSecret != "" {
		form.Set("client_secret", s.config.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return "", 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return "", 0, errors.New("token endpoint rejected authorization code")
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if decoder.Decode(&payload) != nil || decoder.Decode(&struct{}{}) != io.EOF || payload.AccessToken == "" || len(payload.AccessToken) > browserSSOMaxTokenBytes || !strings.EqualFold(payload.TokenType, "Bearer") || payload.ExpiresIn < 1 || payload.ExpiresIn > 86400 {
		return "", 0, errors.New("token endpoint returned an invalid response")
	}
	return payload.AccessToken, time.Duration(payload.ExpiresIn) * time.Second, nil
}

func (s *BrowserSSO) authorizeRequest(r *http.Request) *http.Request {
	if r.Header.Get("Authorization") != "" {
		return r
	}
	cookie, err := r.Cookie(browserSSOSessionCookie)
	if err != nil {
		return r
	}
	var session browserSSOSession
	if s.open(browserSSOSessionCookie, cookie.Value, &session) != nil || session.ExpiresAt < s.now().Unix() || session.Token == "" {
		return r
	}
	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+session.Token)
	return clone
}

func browserSSOAuthMiddleware(sso *BrowserSSO, next http.Handler) http.Handler {
	if sso == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, sso.authorizeRequest(r)) })
}

func (s *BrowserSSO) seal(purpose string, value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(append(nonce, s.aead.Seal(nil, nonce, payload, []byte(purpose))...)), nil
}

func (s *BrowserSSO) open(purpose, encoded string, destination any) error {
	if len(encoded) > 64<<10 {
		return errors.New("encrypted browser state is too large")
	}
	value, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(value) <= s.aead.NonceSize() {
		return errors.New("invalid encrypted browser state")
	}
	payload, err := s.aead.Open(nil, value[:s.aead.NonceSize()], value[s.aead.NonceSize():], []byte(purpose))
	if err != nil {
		return errors.New("invalid encrypted browser state")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("invalid encrypted browser state")
	}
	return nil
}

func (s *BrowserSSO) setCookie(w http.ResponseWriter, name, value, path string, ttl time.Duration, sameSite http.SameSite) {
	redirect, _ := url.Parse(s.config.RedirectURL)
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, HttpOnly: true, Secure: redirect.Scheme == "https", SameSite: sameSite, MaxAge: int(ttl.Seconds()), Expires: s.now().Add(ttl)})
}

func (s *BrowserSSO) clearCookie(w http.ResponseWriter, name, path string, sameSite http.SameSite) {
	redirect, _ := url.Parse(s.config.RedirectURL)
	http.SetCookie(w, &http.Cookie{Name: name, Path: path, HttpOnly: true, Secure: redirect.Scheme == "https", SameSite: sameSite, MaxAge: -1})
}

func randomBrowserSSOValue(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
