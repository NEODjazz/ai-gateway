package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
	"golang.org/x/net/websocket"
)

type realtimeAuthModule struct {
	allowedModels []string
}

func (realtimeAuthModule) Name() string   { return "auth" }
func (realtimeAuthModule) Required() bool { return true }
func (m realtimeAuthModule) Handle(_ context.Context, request *modules.RequestContext) error {
	if request.APIKey != "gateway-key" {
		return modules.ErrUnauthorized
	}
	request.CredentialID = "credential-1"
	request.UserID = "user-1"
	request.AllowedModels = append([]string(nil), m.allowedModels...)
	return nil
}

func TestRealtimeRouteAuthenticatesRoutesAndProxiesTextEvents(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstreamErr := make(chan error, 1)
	upstream := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		upstreamCalls.Add(1)
		request := connection.Request()
		if request.URL.Query().Get("model") != "upstream-model" || request.Header.Get("Authorization") != "Bearer provider-key" {
			upstreamErr <- errors.New("gateway did not apply the selected deployment")
			return
		}
		var event string
		if err := websocket.Message.Receive(connection, &event); err != nil {
			upstreamErr <- err
			return
		}
		if event != `{"type":"session.update","session":{"instructions":"hello"}}` {
			upstreamErr <- errors.New("unexpected proxied event")
			return
		}
		upstreamErr <- websocket.Message.Send(connection, `{"type":"session.updated","event_id":"evt_1"}`)
	}))
	t.Cleanup(upstream.Close)
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "realtime", Type: "openai-compatible", BaseURL: upstream.URL, APIKey: "provider-key",
		Models: []string{"public-model"}, ModelAliases: map[string]string{"public-model": "upstream-model"}, Capabilities: []string{"realtime"},
	}}})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{realtimeAuthModule{allowedModels: []string{"public-model"}}}), router))
	gateway := httptest.NewServer(handler)
	t.Cleanup(gateway.Close)

	connection := dialGatewayRealtime(t, gateway.URL, "/v1/realtime?model=public-model", "gateway-key")
	if err := websocket.Message.Send(connection, `{"type":"session.update","session":{"instructions":"hello"}}`); err != nil {
		t.Fatal(err)
	}
	var event string
	if err := websocket.Message.Receive(connection, &event); err != nil {
		t.Fatal(err)
	}
	if event != `{"type":"session.updated","event_id":"evt_1"}` {
		t.Fatalf("unexpected gateway event: %s", event)
	}
	if err := <-upstreamErr; err != nil {
		t.Fatal(err)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls=%d", upstreamCalls.Load())
	}
}

func TestRealtimeRouteRejectsUnauthorizedAndDisallowedModelsBeforeDial(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { upstreamCalls.Add(1) }))
	t.Cleanup(upstream.Close)
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "realtime", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"public-model"}, Capabilities: []string{"realtime"}}}})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{realtimeAuthModule{allowedModels: []string{"other-model"}}}), router))

	for _, test := range []struct {
		name, path, key string
		status          int
	}{
		{name: "unauthorized", path: "/v1/realtime?model=public-model", status: http.StatusUnauthorized},
		{name: "disallowed", path: "/v1/realtime?model=public-model", key: "gateway-key", status: http.StatusForbidden},
		{name: "missing model", path: "/v1/realtime", key: "gateway-key", status: http.StatusBadRequest},
		{name: "unknown query", path: "/v1/realtime?model=public-model&extra=1", key: "gateway-key", status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.key != "" {
				request.Header.Set("Authorization", "Bearer "+test.key)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("unauthorized request reached upstream %d times", upstreamCalls.Load())
	}
}

func TestRealtimeRouteRejectsBinaryClientEvents(t *testing.T) {
	var upstreamEvents atomic.Int32
	upstream := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		var event string
		if websocket.Message.Receive(connection, &event) == nil {
			upstreamEvents.Add(1)
		}
	}))
	t.Cleanup(upstream.Close)
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "realtime", Type: "openai", BaseURL: upstream.URL, Models: []string{"model"}, Capabilities: []string{"realtime"}}}})
	gateway := httptest.NewServer(Routes(NewHandler(modules.NewPipeline([]modules.Module{realtimeAuthModule{allowedModels: []string{"model"}}}), router)))
	t.Cleanup(gateway.Close)

	connection := dialGatewayRealtime(t, gateway.URL, "/v1/realtime?model=model", "gateway-key")
	if err := websocket.Message.Send(connection, []byte(`{"type":"session.update"}`)); err != nil {
		t.Fatal(err)
	}
	var response string
	if err := websocket.Message.Receive(connection, &response); err == nil {
		t.Fatal("binary event did not close the gateway session")
	}
	if upstreamEvents.Load() != 0 {
		t.Fatal("binary event reached the provider")
	}
}

func dialGatewayRealtime(t *testing.T, gatewayURL, path, key string) *websocket.Conn {
	t.Helper()
	websocketURL := "ws" + strings.TrimPrefix(gatewayURL, "http") + path
	config, err := websocket.NewConfig(websocketURL, gatewayURL)
	if err != nil {
		t.Fatal(err)
	}
	config.Header.Set("Authorization", "Bearer "+key)
	connection, err := config.DialContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}
