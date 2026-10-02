package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"
)

const browserSSOTestStateCookie = "ai_gateway_sso_test_state"

func validBrowserSSOTest(profile PrivateSSOTest, ticket string, now time.Time) bool {
	if profile.Profile == nil || profile.Attempt == nil || profile.Attempt.Status != "running" || profile.Attempt.ExpiresAt <= now.Unix() || len(ticket) != 43 {
		return false
	}
	digest := sha256.Sum256([]byte(ticket))
	return subtle.ConstantTimeCompare([]byte(profile.Attempt.TicketHash), []byte(hex.EncodeToString(digest[:]))) == 1
}

func (h Handler) StartBrowserSSOTest(w http.ResponseWriter, r *http.Request) {
	h, err := h.withRequestSSOConnection(r)
	if err != nil {
		writeSSOFailure(w, err)
		return
	}
	id, _ := requestSSOConnection(r)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	query := r.URL.Query()
	if h.ssoManagement == nil || (len(query) != 1 && !(len(query) == 2 && id != "")) || len(query["ticket"]) != 1 {
		writeError(w, 400, "invalid_sso_test", "SSO test is invalid or expired.")
		return
	}
	profile, err := h.ssoManagement.TestSSOProfile(r.Context(), ssoServiceAudit(r))
	if err != nil || !validBrowserSSOTest(profile, query.Get("ticket"), time.Now()) {
		writeError(w, 400, "invalid_sso_test", "SSO test is invalid or expired.")
		return
	}
	sso, err := profile.Profile.browser(true)
	if err != nil {
		writeError(w, 503, "sso_unavailable", "Browser sign-in is unavailable.")
		return
	}
	if id != "" && id != "default" {
		sso.config.ConnectionID = id
	}
	h.browserSSO = sso
	h.startBrowserSSO(w, r, profile.Profile.ID, query.Get("ticket"))
}

func (h Handler) CompleteBrowserSSOTest(w http.ResponseWriter, r *http.Request) {
	h, err := h.withRequestSSOConnection(r)
	if err != nil {
		writeSSOFailure(w, err)
		return
	}
	id, _ := requestSSOConnection(r)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if h.ssoManagement == nil {
		writeError(w, 400, "invalid_sso_test", "SSO test is invalid or expired.")
		return
	}
	profile, err := h.ssoManagement.TestSSOProfile(r.Context(), ssoServiceAudit(r))
	if err != nil || profile.Profile == nil {
		writeError(w, 400, "invalid_sso_test", "SSO test is invalid or expired.")
		return
	}
	sso, err := profile.Profile.browser(true)
	if err != nil {
		writeError(w, 503, "sso_unavailable", "Browser sign-in is unavailable.")
		return
	}
	if id != "" && id != "default" {
		sso.config.ConnectionID = id
	}
	// Verify the sealed ticket and profile before submitting an authorization code.
	cookie, err := r.Cookie(browserSSOTestStateCookie)
	var state browserSSOState
	if err != nil || sso.open(browserSSOTestStateCookie, cookie.Value, &state) != nil || state.ProfileID != profile.Profile.ID || !validBrowserSSOTest(profile, state.TestTicket, sso.now()) {
		writeError(w, 400, "invalid_sso_test", "SSO test is invalid or expired.")
		return
	}
	token, _, state, ok := sso.callbackToken(w, r, browserSSOTestStateCookie)
	if !ok {
		return
	}
	err = h.ssoManagement.VerifySSOTest(r.Context(), ssoServiceAudit(r), SSOTestVerification{ProfileID: state.ProfileID, Ticket: state.TestTicket, Token: token, Nonce: state.Nonce, AccessToken: state.AccessToken})
	if err != nil {
		writeSSOFailure(w, err)
		return
	}
	// The test never creates or overwrites an active browser session.
	http.Redirect(w, r, "/ui/settings?sso_test=passed"+ssoConnectionQueryForID(id), http.StatusFound)
}

func ssoConnectionQueryForID(id string) string {
	if id == "" || id == "default" {
		return ""
	}
	return "&connection=" + id
}
