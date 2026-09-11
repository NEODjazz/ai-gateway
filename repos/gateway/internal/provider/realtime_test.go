package provider

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"golang.org/x/net/websocket"
)

func TestOpenAICompatibleRealtimeHandshakeAndRoundTrip(t *testing.T) {
	serverErr := make(chan error, 1)
	server := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		request := connection.Request()
		if request.URL.Path != "/v1/realtime" {
			serverErr <- errors.New("unexpected realtime path: " + request.URL.Path)
			return
		}
		if request.URL.Query().Get("model") != "model with/slash" {
			serverErr <- errors.New("model query was not preserved")
			return
		}
		if request.Header.Get("Authorization") != "Bearer provider-key" {
			serverErr <- errors.New("provider authorization was not forwarded")
			return
		}
		if request.Header.Get("OpenAI-Beta") != "realtime=v1" {
			serverErr <- errors.New("realtime beta header was not forwarded")
			return
		}
		var event string
		if err := websocket.Message.Receive(connection, &event); err != nil {
			serverErr <- err
			return
		}
		if event != `{"type":"session.update","session":{"instructions":"be concise"}}` {
			serverErr <- errors.New("unexpected client event: " + event)
			return
		}
		serverErr <- websocket.Message.Send(connection, `{"type":"session.updated","event_id":"evt_1"}`)
	}))
	t.Cleanup(server.Close)

	client := NewOpenAICompatible(server.URL+"/v1", "provider-key", false)
	connection, err := client.OpenRealtime(t.Context(), "model with/slash")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	if err := connection.Send([]byte(`{"type":"session.update","session":{"instructions":"be concise"}}`)); err != nil {
		t.Fatal(err)
	}
	payload, err := connection.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"type":"session.updated","event_id":"evt_1"}` {
		t.Fatalf("unexpected server event: %s", payload)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestRealtimeRejectsInvalidOutboundEventsBeforeWriting(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "empty", payload: nil},
		{name: "malformed", payload: []byte(`{"type":`)},
		{name: "missing type", payload: []byte(`{"event_id":"evt_1"}`)},
		{name: "empty type", payload: []byte(`{"type":" "}`)},
		{name: "trailing json", payload: []byte(`{"type":"session.update"}{}`)},
		{name: "oversized", payload: append([]byte(`{"type":"session.update","padding":"`), append(bytes.Repeat([]byte("x"), MaxRealtimeEventBytes), []byte(`"}`)...)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRealtimeEvent(test.payload); err == nil {
				t.Fatal("expected invalid realtime event to be rejected")
			}
		})
	}
}

func TestRealtimeRejectsOversizedAndBinaryInboundFrames(t *testing.T) {
	tests := []struct {
		name string
		send func(*websocket.Conn) error
	}{
		{
			name: "oversized",
			send: func(connection *websocket.Conn) error {
				return websocket.Message.Send(connection, string(append([]byte(`{"type":"response.text.delta","delta":"`), append(bytes.Repeat([]byte("x"), MaxRealtimeEventBytes), []byte(`"}`)...)...)))
			},
		},
		{
			name: "binary",
			send: func(connection *websocket.Conn) error {
				return websocket.Message.Send(connection, []byte(`{"type":"response.done"}`))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
				_ = test.send(connection)
			}))
			t.Cleanup(server.Close)

			connection, err := NewOpenAICompatible(server.URL, "", false).OpenRealtime(t.Context(), "model")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connection.Close() })
			if _, err := connection.Receive(); err == nil {
				t.Fatal("expected inbound frame to be rejected")
			}
		})
	}
}

func TestOpenAICompatibleRealtimeValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		model string
	}{
		{name: "missing model", base: "https://example.test/v1"},
		{name: "long model", base: "https://example.test/v1", model: strings.Repeat("x", 257)},
		{name: "invalid base", base: "://invalid", model: "model"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewOpenAICompatible(test.base, "", false).OpenRealtime(context.Background(), test.model); err == nil {
				t.Fatal("expected configuration to be rejected")
			}
		})
	}
}

func TestOpenAICompatibleRealtimeHonorsDialCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewOpenAICompatible(server.URL, "", false).OpenRealtime(ctx, "model"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestRealtimeRouterRequiresCapabilityPinsAdmissionAndAppliesAlias(t *testing.T) {
	var skippedCalls atomic.Int32
	skipped := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		skippedCalls.Add(1)
	}))
	t.Cleanup(skipped.Close)
	var selectedCalls atomic.Int32
	var aliasApplied atomic.Bool
	selected := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		selectedCalls.Add(1)
		aliasApplied.Store(connection.Request().URL.Query().Get("model") == "upstream-model")
		var event string
		_ = websocket.Message.Receive(connection, &event)
	}))
	t.Cleanup(selected.Close)

	router := New(Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "missing-capability", Type: "openai", BaseURL: skipped.URL, Models: []string{"public-model"}, Priority: 1, Capabilities: []string{"chat"}},
		{Name: "realtime", Type: "openai-compatible", BaseURL: selected.URL, Models: []string{"public-model"}, ModelAliases: map[string]string{"public-model": "upstream-model"}, Priority: 2, Capabilities: []string{"realtime"}, MaxParallelRequests: 1},
	}})
	runtime, ok := router.(RealtimeProvider)
	if !ok {
		t.Fatal("router does not expose realtime sessions")
	}
	identity := modules.RequestContext{RequestID: "execution-1", Metadata: map[string]string{"credential.id": "credential-1"}}
	first, attempt, err := runtime.OpenRealtime(t.Context(), identity, "public-model")
	if err != nil {
		t.Fatal(err)
	}
	if skippedCalls.Load() != 0 || selectedCalls.Load() != 1 || !aliasApplied.Load() {
		t.Fatalf("routing skipped=%d selected=%d alias=%v", skippedCalls.Load(), selectedCalls.Load(), aliasApplied.Load())
	}
	if attempt.Request.Model != "upstream-model" || attempt.Metadata["provider.endpoint.name"] != "realtime" || attempt.Metadata["gateway.api_type"] != "realtime" {
		t.Fatalf("attempt=%+v metadata=%v", attempt.Request, attempt.Metadata)
	}
	if second, _, err := runtime.OpenRealtime(t.Context(), identity, "public-model"); err == nil {
		_ = second.Close()
		t.Fatal("deployment admission limit did not reject a concurrent session")
	} else {
		var admissionErr *AdmissionError
		if !errors.As(err, &admissionErr) {
			t.Fatalf("expected admission error, got %v", err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, _, err := runtime.OpenRealtime(t.Context(), identity, "public-model")
	if err != nil {
		t.Fatalf("admission lease was not released: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}
