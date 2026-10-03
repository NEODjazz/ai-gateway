package gateway

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/realtimestate"
)

const realtimeBrowserProtocol = "ai-gateway.realtime.v1"
const realtimeBrowserTicketPrefix = "ai-gateway.realtime.ticket."

type realtimeBrowserBindingKey struct{}
type realtimeBrowserBinding struct{ ownerKey string }
type realtimeBrowserSecret struct {
	APIKey    string `json:"api_key"`
	SessionID string `json:"session_id"`
}

func (h Handler) WithRealtimeBrowserTickets(store realtimestate.Store, encryptionKey []byte) (Handler, error) {
	if store == nil || len(encryptionKey) < 16 {
		return h, errors.New("realtime browser tickets require storage and a credential encryption key of at least 16 bytes")
	}
	key := sha256.Sum256(append([]byte("ai-gateway/realtime-browser-ticket/v1\x00"), encryptionKey...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return h, fmt.Errorf("create realtime ticket cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return h, fmt.Errorf("create realtime ticket encryption: %w", err)
	}
	h.realtimeTickets, h.realtimeTicketAEAD = store, aead
	return h, nil
}

func realtimeTicketHash(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}
func realtimeTicketOwner(req modules.RequestContext) string {
	encoded, _ := json.Marshal([]string{req.CredentialID, req.UserID, req.OrganizationID, req.TeamID})
	return realtimeTicketHash(string(encoded))
}
func realtimeTicketAAD(ticket realtimestate.Ticket) []byte {
	encoded, _ := json.Marshal([]any{"realtime-browser-ticket-v1", ticket.Hash, ticket.OwnerKey, ticket.Model, ticket.Origin, ticket.ExpiresAt.UnixMicro()})
	return encoded
}

func realtimeBrowserOrigin(r *http.Request, raw string) (string, bool) {
	if len(raw) > 2048 {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.EqualFold(parsed.Host, r.Host) {
		return "", false
	}
	return parsed.Scheme + "://" + strings.ToLower(parsed.Host), true
}

func (h Handler) CreateRealtimeBrowserTicket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Vary", "Authorization, Cookie, Origin")
	if h.realtimeTickets == nil || h.realtimeTicketAEAD == nil {
		writeError(w, http.StatusServiceUnavailable, "realtime_browser_unavailable", "Realtime browser tickets require configured credential encryption and storage")
		return
	}
	var input struct {
		Model  string `json:"model"`
		Origin string `json:"origin"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	var extra any
	if r.URL.RawQuery != "" || decoder.Decode(&input) != nil || decoder.Decode(&extra) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid realtime browser ticket request")
		return
	}
	input.Model = strings.TrimSpace(input.Model)
	origin, valid := realtimeBrowserOrigin(r, input.Origin)
	if input.Model == "" || len(input.Model) > 256 || !valid || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin) {
		writeError(w, http.StatusBadRequest, "invalid_request", "A valid model and same-host browser origin are required")
		return
	}
	apiKey := bearerToken(r.Header.Get("Authorization"))
	identity := modules.RequestContext{APIKey: apiKey, RequestID: executionID(w), SessionID: sessionID(r), Request: realtimeModelRequest(input.Model), Metadata: map[string]string{"gateway.api_type": "realtime_browser_ticket"}}
	if err := h.pipeline.RunAuthentication(r.Context(), &identity); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid gateway credential")
			return
		}
		writeError(w, http.StatusBadGateway, "module_failed", "Authentication failed")
		return
	}
	if h.adminState != nil && h.adminState.Refresh(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "admin_state_unavailable", "Realtime configuration is unavailable")
		return
	}
	if !h.prepareAccessGroups(w, &identity) || !h.authorizeModel(w, identity, input.Model) {
		return
	}
	if apiKey == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "A gateway credential is required")
		return
	}
	opaque := make([]byte, 32)
	if _, err := rand.Read(opaque); err != nil {
		writeError(w, http.StatusServiceUnavailable, "realtime_browser_unavailable", "Could not create a browser ticket")
		return
	}
	value := base64.RawURLEncoding.EncodeToString(opaque)
	ticket := realtimestate.Ticket{Hash: realtimeTicketHash(value), OwnerKey: realtimeTicketOwner(identity), Model: input.Model, Origin: origin, ExpiresAt: time.Now().UTC().Add(realtimestate.TicketTTL).Truncate(time.Microsecond)}
	plaintext, err := json.Marshal(realtimeBrowserSecret{APIKey: apiKey, SessionID: identity.SessionID})
	if err != nil || len(plaintext)+h.realtimeTicketAEAD.NonceSize()+h.realtimeTicketAEAD.Overhead() > realtimestate.MaxPayloadBytes {
		writeError(w, http.StatusBadRequest, "invalid_request", "The credential exceeds the browser ticket limit")
		return
	}
	nonce := make([]byte, h.realtimeTicketAEAD.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		writeError(w, http.StatusServiceUnavailable, "realtime_browser_unavailable", "Could not encrypt a browser ticket")
		return
	}
	ticket.Payload = append(nonce, h.realtimeTicketAEAD.Seal(nil, nonce, plaintext, realtimeTicketAAD(ticket))...)
	if err := h.realtimeTickets.CreateRealtimeTicket(r.Context(), ticket); err != nil {
		if errors.Is(err, realtimestate.ErrCapacity) {
			w.Header().Set("Retry-After", "30")
			writeError(w, http.StatusTooManyRequests, "realtime_browser_capacity", "Too many pending realtime browser tickets")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "realtime_browser_unavailable", "Browser ticket storage is unavailable")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ticket": value, "expires_at": ticket.ExpiresAt.Format(time.RFC3339Nano), "protocol": realtimeBrowserProtocol, "socket_path": "/v1/realtime/browser"})
}

func (h Handler) RealtimeBrowser(w http.ResponseWriter, r *http.Request) {
	if h.realtimeTickets == nil || h.realtimeTicketAEAD == nil {
		writeError(w, http.StatusServiceUnavailable, "realtime_browser_unavailable", "Realtime browser tickets are unavailable")
		return
	}
	query, queryErr := url.ParseQuery(r.URL.RawQuery)
	origin, validOrigin := realtimeBrowserOrigin(r, r.Header.Get("Origin"))
	protocols := strings.Split(strings.Join(r.Header.Values("Sec-WebSocket-Protocol"), ","), ",")
	value, stable := "", false
	if len(protocols) == 2 {
		for _, protocol := range protocols {
			protocol = strings.TrimSpace(protocol)
			if protocol == realtimeBrowserProtocol {
				stable = true
			} else if strings.HasPrefix(protocol, realtimeBrowserTicketPrefix) {
				value = strings.TrimPrefix(protocol, realtimeBrowserTicketPrefix)
			}
		}
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if queryErr != nil || !validOrigin || len(query) != 1 || len(query["model"]) != 1 || query.Get("model") == "" || len(query.Get("model")) > 256 || !stable || err != nil || len(decoded) != 32 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "A valid origin-bound browser ticket is required")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !headerToken(r.Header.Get("Connection"), "upgrade") {
		writeError(w, http.StatusBadRequest, "invalid_request", "A WebSocket upgrade is required")
		return
	}
	ticket, err := h.realtimeTickets.ConsumeRealtimeTicket(r.Context(), realtimeTicketHash(value), query.Get("model"), origin)
	if err != nil {
		if errors.Is(err, realtimestate.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Browser ticket is absent, expired or already used")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "realtime_browser_unavailable", "Browser ticket storage is unavailable")
		return
	}
	if len(ticket.Payload) <= h.realtimeTicketAEAD.NonceSize() {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid browser ticket")
		return
	}
	plaintext, err := h.realtimeTicketAEAD.Open(nil, ticket.Payload[:h.realtimeTicketAEAD.NonceSize()], ticket.Payload[h.realtimeTicketAEAD.NonceSize():], realtimeTicketAAD(ticket))
	var secret realtimeBrowserSecret
	if err != nil || json.Unmarshal(plaintext, &secret) != nil || secret.APIKey == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid browser ticket")
		return
	}
	clone := r.Clone(context.WithValue(r.Context(), realtimeBrowserBindingKey{}, realtimeBrowserBinding{ownerKey: ticket.OwnerKey}))
	clone.Header = r.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+secret.APIKey)
	clone.Header.Set("Sec-WebSocket-Protocol", realtimeBrowserProtocol)
	if secret.SessionID != "" {
		clone.Header.Set("X-Session-ID", secret.SessionID)
	}
	h.Realtime(w, clone)
}

func headerToken(header, token string) bool {
	for _, value := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(value), token) {
			return true
		}
	}
	return false
}
