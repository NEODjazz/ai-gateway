package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/realtimestate"
	"golang.org/x/net/websocket"
)

type browserTicketAuth struct {
	revoked atomic.Bool
	changed atomic.Bool
	denied  atomic.Bool
	allowed []string
}

func (*browserTicketAuth) Name() string   { return "auth" }
func (*browserTicketAuth) Required() bool { return true }
func (a *browserTicketAuth) Handle(ctx context.Context, req *modules.RequestContext) error {
	if a.revoked.Load() {
		return modules.ErrUnauthorized
	}
	if err := (realtimeAuthModule{allowedModels: a.allowed}).Handle(ctx, req); err != nil {
		return err
	}
	if a.denied.Load() {
		req.AllowedModels = []string{"another-model"}
	}
	if a.changed.Load() {
		req.OrganizationID = "another-organization"
	}
	// Production authentication modules intentionally erase the input token.
	req.APIKey = ""
	return nil
}

type browserTicketCapture struct {
	realtimestate.Store
	mu     sync.Mutex
	ticket realtimestate.Ticket
	fail   bool
}

func (s *browserTicketCapture) CreateRealtimeTicket(ctx context.Context, ticket realtimestate.Ticket) error {
	if s.fail {
		return realtimestate.ErrUnavailable
	}
	s.mu.Lock()
	s.ticket = ticket
	s.ticket.Payload = append([]byte(nil), ticket.Payload...)
	s.mu.Unlock()
	return s.Store.CreateRealtimeTicket(ctx, ticket)
}
func (s *browserTicketCapture) captured() realtimestate.Ticket {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ticket
}

func ticketHandler(t *testing.T, auth modules.Module, store realtimestate.Store, key string, llm provider.Provider) Handler {
	t.Helper()
	h, err := NewHandler(modules.NewPipeline([]modules.Module{auth}), llm).WithRealtimeBrowserTickets(store, []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func createBrowserTicket(t *testing.T, h Handler) string {
	return createBrowserTicketDialect(t, h, "")
}

func createBrowserTicketDialect(t *testing.T, h Handler, dialect string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/realtime/browser-tickets", strings.NewReader(`{"model":"public-model","origin":"http://gateway.test","dialect":"`+dialect+`"}`))
	r.Header.Set("Authorization", "Bearer gateway-key")
	r.Header.Set("Origin", "http://gateway.test")
	w := httptest.NewRecorder()
	h.CreateRealtimeBrowserTicket(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("ticket status=%d", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("ticket response is cacheable")
	}
	var reply struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Ticket) != 43 || bytes.Contains(w.Body.Bytes(), []byte("gateway-key")) {
		t.Fatal("invalid ticket response or exposed gateway credential")
	}
	return reply.Ticket
}

func ticketUpgrade(value, model, origin string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/realtime/browser?model="+model, nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Sec-WebSocket-Protocol", realtimeBrowserProtocol+", "+realtimeBrowserTicketPrefix+value)
	return r
}

func TestRealtimeBrowserTicketRevalidatesCredentialAndIdentity(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "identity changed"}[changed], func(t *testing.T) {
			auth := &browserTicketAuth{allowed: []string{"public-model"}}
			capture := &browserTicketCapture{Store: realtimestate.NewMemoryStore(10, 8)}
			h := ticketHandler(t, auth, capture, strings.Repeat("a", 32), provider.New(provider.Config{}))
			value := createBrowserTicket(t, h)
			stored := capture.captured()
			if bytes.Contains(stored.Payload, []byte("gateway-key")) || stored.Hash == value {
				t.Fatal("ticket storage exposes credential or reusable ticket")
			}
			if changed {
				auth.changed.Store(true)
			} else {
				auth.revoked.Store(true)
			}
			w := httptest.NewRecorder()
			h.RealtimeBrowser(w, ticketUpgrade(value, "public-model", "http://gateway.test"))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("revalidation status=%d", w.Code)
			}
			w = httptest.NewRecorder()
			h.RealtimeBrowser(w, ticketUpgrade(value, "public-model", "http://gateway.test"))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("replay status=%d", w.Code)
			}
		})
	}
}

func TestRealtimeBrowserTicketFailsClosedOnWrongScopeOrKey(t *testing.T) {
	auth := &browserTicketAuth{allowed: []string{"public-model"}}
	store := realtimestate.NewMemoryStore(10, 8)
	h := ticketHandler(t, auth, store, strings.Repeat("a", 32), provider.New(provider.Config{}))
	value := createBrowserTicket(t, h)
	for _, r := range []*http.Request{ticketUpgrade(value, "other-model", "http://gateway.test"), ticketUpgrade(value, "public-model", "http://other.test"), ticketUpgrade(value, "public-model", "http://gateway.test")} {
		if r.URL.Query().Get("model") == "public-model" && r.Header.Get("Origin") == "http://gateway.test" {
			r.URL.RawQuery += "&api_key=forbidden"
		}
		w := httptest.NewRecorder()
		h.RealtimeBrowser(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("scope rejection status=%d", w.Code)
		}
	}
	other := ticketHandler(t, auth, store, strings.Repeat("b", 32), provider.New(provider.Config{}))
	w := httptest.NewRecorder()
	other.RealtimeBrowser(w, ticketUpgrade(value, "public-model", "http://gateway.test"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key status=%d", w.Code)
	}
	if _, err := store.ConsumeRealtimeTicket(t.Context(), realtimeTicketHash(value), "public-model", "http://gateway.test"); !errors.Is(err, realtimestate.ErrNotFound) {
		t.Fatalf("failed decrypt ticket reused: %v", err)
	}
}

func TestRealtimeBrowserTicketMintValidationAndAdmission(t *testing.T) {
	auth := &browserTicketAuth{allowed: []string{"public-model"}}
	h := ticketHandler(t, auth, realtimestate.NewMemoryStore(1, 1), strings.Repeat("a", 32), provider.New(provider.Config{}))
	for _, test := range []struct {
		body, key string
		status    int
	}{
		{`{"model":"public-model","origin":"http://gateway.test"}`, "", http.StatusUnauthorized},
		{`{"model":"other","origin":"http://gateway.test"}`, "gateway-key", http.StatusForbidden},
		{`{"model":"public-model","origin":"http://other.test"}`, "gateway-key", http.StatusBadRequest},
		{`{"model":"public-model","origin":"http://gateway.test","dialect":"invalid"}`, "gateway-key", http.StatusBadRequest},
		{`{"model":"public-model","origin":"http://gateway.test","extra":true}`, "gateway-key", http.StatusBadRequest},
		{`{"model":"public-model","origin":"http://gateway.test"} {}`, "gateway-key", http.StatusBadRequest},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/realtime/browser-tickets", strings.NewReader(test.body))
		r.Header.Set("Authorization", "Bearer "+test.key)
		w := httptest.NewRecorder()
		h.CreateRealtimeBrowserTicket(w, r)
		if w.Code != test.status {
			t.Fatalf("mint validation status=%d want=%d", w.Code, test.status)
		}
	}
	_ = createBrowserTicket(t, h)
	r := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/realtime/browser-tickets", strings.NewReader(`{"model":"public-model","origin":"http://gateway.test"}`))
	r.Header.Set("Authorization", "Bearer gateway-key")
	w := httptest.NewRecorder()
	h.CreateRealtimeBrowserTicket(w, r)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "30" {
		t.Fatalf("capacity status=%d", w.Code)
	}
	failed := ticketHandler(t, auth, &browserTicketCapture{Store: realtimestate.NewMemoryStore(10, 8), fail: true}, strings.Repeat("a", 32), provider.New(provider.Config{}))
	r = httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/realtime/browser-tickets", strings.NewReader(`{"model":"public-model","origin":"http://gateway.test"}`))
	r.Header.Set("Authorization", "Bearer gateway-key")
	w = httptest.NewRecorder()
	failed.CreateRealtimeBrowserTicket(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("storage outage status=%d", w.Code)
	}
	if _, err := h.WithRealtimeBrowserTickets(realtimestate.NewMemoryStore(1, 1), []byte("short")); err == nil {
		t.Fatal("short encryption key accepted")
	}
}

func TestRealtimeBrowserTicketProxiesAcrossReplicasWithoutExposingTicket(t *testing.T) {
	for _, dialect := range []string{"", "legacy", "current"} {
		t.Run("dialect="+dialect, func(t *testing.T) {
			upstreamErrors := make(chan error, 1)
			upstream := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
				if strings.Contains(conn.Request().Header.Get("Sec-WebSocket-Protocol"), realtimeBrowserTicketPrefix) || conn.Request().Header.Get("Authorization") != "Bearer provider-key" {
					upstreamErrors <- errors.New("browser ticket or gateway credential reached provider")
					return
				}
				wantBeta := "realtime=v1"
				if dialect == "current" {
					wantBeta = ""
				}
				if conn.Request().Header.Get("OpenAI-Beta") != wantBeta {
					upstreamErrors <- errors.New("selected browser dialect did not reach the provider")
					return
				}
				var event string
				if err := websocket.Message.Receive(conn, &event); err != nil {
					upstreamErrors <- err
					return
				}
				upstreamErrors <- websocket.Message.Send(conn, `{"type":"session.updated"}`)
			}))
			t.Cleanup(upstream.Close)
			router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "realtime", Type: "openai-compatible", BaseURL: upstream.URL, APIKey: "provider-key", Models: []string{"public-model"}, Capabilities: []string{"realtime"}}}})
			store := realtimestate.NewMemoryStore(10, 8)
			auth := &browserTicketAuth{allowed: []string{"public-model"}}
			issuer := ticketHandler(t, auth, store, strings.Repeat("a", 32), router)
			replica := ticketHandler(t, auth, store, strings.Repeat("a", 32), router)
			value := createBrowserTicketDialect(t, issuer, dialect)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { r.Host = "gateway.test"; Routes(replica).ServeHTTP(w, r) }))
			t.Cleanup(server.Close)
			config, err := websocket.NewConfig("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/realtime/browser?model=public-model", "http://gateway.test")
			if err != nil {
				t.Fatal(err)
			}
			config.Protocol = []string{realtimeBrowserProtocol, realtimeBrowserTicketPrefix + value}
			// Browser WebSocket handshakes include origin cookies even when ticket minting
			// used an explicit test credential with fetch credentials=omit.
			config.Header = http.Header{"Cookie": []string{browserSSOSessionCookie + "=invalid!.opaque"}}
			conn, err := websocket.DialConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if len(conn.Config().Protocol) != 1 || conn.Config().Protocol[0] != realtimeBrowserProtocol {
				t.Fatal("handshake exposed ticket or omitted stable protocol")
			}
			if err := websocket.Message.Send(conn, `{"type":"session.update","session":{"instructions":"Synthetic fixture"}}`); err != nil {
				t.Fatal(err)
			}
			var event string
			if err := websocket.Message.Receive(conn, &event); err != nil {
				t.Fatal(err)
			}
			if event != `{"type":"session.updated"}` {
				t.Fatal("unexpected realtime event")
			}
			if err := <-upstreamErrors; err != nil {
				t.Fatal(err)
			}
			if _, err := websocket.DialConfig(config); err == nil {
				t.Fatal("browser ticket replay connected")
			}
		})
	}
}

type mutatedBrowserTicket struct {
	realtimestate.Store
	mutate func(*realtimestate.Ticket)
}

func (s mutatedBrowserTicket) ConsumeRealtimeTicket(ctx context.Context, hash, model, origin string) (realtimestate.Ticket, error) {
	ticket, err := s.Store.ConsumeRealtimeTicket(ctx, hash, model, origin)
	if err == nil {
		s.mutate(&ticket)
	}
	return ticket, err
}
func TestRealtimeBrowserTicketRejectsTamperedCiphertextAndBinding(t *testing.T) {
	for name, mutate := range map[string]func(*realtimestate.Ticket){
		"payload": func(ticket *realtimestate.Ticket) { ticket.Payload[len(ticket.Payload)-1] ^= 1 },
		"owner":   func(ticket *realtimestate.Ticket) { ticket.OwnerKey = strings.Repeat("b", 64) },
		"model":   func(ticket *realtimestate.Ticket) { ticket.Model = "another-model" },
		"origin":  func(ticket *realtimestate.Ticket) { ticket.Origin = "http://other.test" },
		"expiry":  func(ticket *realtimestate.Ticket) { ticket.ExpiresAt = ticket.ExpiresAt.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			auth := &browserTicketAuth{allowed: []string{"public-model"}}
			store := realtimestate.NewMemoryStore(10, 8)
			issuer := ticketHandler(t, auth, store, strings.Repeat("a", 32), provider.New(provider.Config{}))
			value := createBrowserTicket(t, issuer)
			consumer := ticketHandler(t, auth, mutatedBrowserTicket{store, mutate}, strings.Repeat("a", 32), provider.New(provider.Config{}))
			w := httptest.NewRecorder()
			consumer.RealtimeBrowser(w, ticketUpgrade(value, "public-model", "http://gateway.test"))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("tampered ticket status=%d", w.Code)
			}
		})
	}
}
func TestRealtimeBrowserTicketRevalidatesModelGrants(t *testing.T) {
	auth := &browserTicketAuth{allowed: []string{"public-model"}}
	h := ticketHandler(t, auth, realtimestate.NewMemoryStore(10, 8), strings.Repeat("a", 32), provider.New(provider.Config{}))
	value := createBrowserTicket(t, h)
	auth.denied.Store(true)
	w := httptest.NewRecorder()
	h.RealtimeBrowser(w, ticketUpgrade(value, "public-model", "http://gateway.test"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("revoked model grant status=%d", w.Code)
	}
}
