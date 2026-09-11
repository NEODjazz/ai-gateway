package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
	"golang.org/x/net/websocket"
)

type realtimeAuthModule struct {
	allowedModels []string
	tpm           int
}

type realtimeBillingEvent struct {
	phase, requestID string
	input, output    int
	exact            string
}

type realtimeBillingModule struct {
	mu         sync.Mutex
	events     []realtimeBillingEvent
	reserveErr error
}

type realtimeDLPModule struct {
	mu      sync.Mutex
	content string
}

func (*realtimeDLPModule) Name() string              { return "dlp" }
func (*realtimeDLPModule) Required() bool            { return true }
func (*realtimeDLPModule) PostResponseEnabled() bool { return true }
func (m *realtimeDLPModule) Handle(_ context.Context, request *modules.RequestContext) error {
	m.mu.Lock()
	m.content, _ = request.Request.Messages[0].Content.(string)
	m.mu.Unlock()
	return modules.ErrContentRejected
}
func (*realtimeDLPModule) HandlePostResponse(context.Context, *modules.RequestContext) error {
	return nil
}

func (*realtimeBillingModule) Name() string              { return "billing" }
func (*realtimeBillingModule) Required() bool            { return true }
func (*realtimeBillingModule) PostResponseEnabled() bool { return true }
func (m *realtimeBillingModule) Handle(_ context.Context, request *modules.RequestContext) error {
	m.record("reserve", request)
	return m.reserveErr
}
func (m *realtimeBillingModule) HandlePostResponse(_ context.Context, request *modules.RequestContext) error {
	m.record("commit", request)
	return nil
}
func (m *realtimeBillingModule) HandleFailure(_ context.Context, request *modules.RequestContext, _ error) error {
	m.record("cancel", request)
	return nil
}
func (m *realtimeBillingModule) record(phase string, request *modules.RequestContext) {
	event := realtimeBillingEvent{phase: phase, requestID: request.RequestID, exact: request.Metadata["gateway.realtime_usage_exact"]}
	if request.Usage != nil {
		event.input, event.output = request.Usage.PromptTokens, request.Usage.CompletionTokens
	}
	m.mu.Lock()
	m.events = append(m.events, event)
	m.mu.Unlock()
}
func (m *realtimeBillingModule) snapshot() []realtimeBillingEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]realtimeBillingEvent(nil), m.events...)
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
	request.RateLimitTPM = m.tpm
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
	if err := websocket.Message.Receive(connection, &response); err != nil || !strings.Contains(response, `"code":"gateway_error"`) {
		t.Fatalf("binary rejection=%s err=%v", response, err)
	}
	if err := websocket.Message.Receive(connection, &response); err == nil {
		t.Fatal("binary rejection did not close the gateway session")
	}
	if upstreamEvents.Load() != 0 {
		t.Fatal("binary event reached the provider")
	}
}

func TestRealtimeBillingTracksContextUsesUniqueExecutionsAndSettlesUsage(t *testing.T) {
	billing := &realtimeBillingModule{}
	tracker := newRealtimeBillingTracker(modules.NewPipeline([]modules.Module{billing}), modules.RequestContext{RequestID: "session-execution", CredentialID: "credential-1", Metadata: map[string]string{"provider.endpoint.name": "realtime"}}, "public-model", nil)
	session := json.RawMessage(`{"instructions":"answer briefly","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`)
	item := json.RawMessage(`{"id":"item_1","type":"message","role":"user","content":[{"type":"input_text","text":"weather in Paris"}]}`)
	response := json.RawMessage(`{"max_output_tokens":42,"tools":[{"type":"function","name":"other","parameters":{"type":"object"}}]}`)
	if err := tracker.ClientEvent(t.Context(), append([]byte(`{"type":"session.update","session":`), append(session, '}')...)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ClientEvent(t.Context(), append([]byte(`{"type":"conversation.item.create","item":`), append(item, '}')...)); err != nil {
		t.Fatal(err)
	}
	create := append([]byte(`{"event_id":"same-client-id","type":"response.create","response":`), append(response, '}')...)
	if err := tracker.ClientEvent(t.Context(), create); err != nil {
		t.Fatal(err)
	}
	events := billing.snapshot()
	wantInput := saturatedRealtimeTokens(realtimeRawTokens(session), realtimeRawTokens(item))
	wantInput = saturatedRealtimeTokens(wantInput, realtimeRawTokens(response))
	if len(events) != 1 || events[0].phase != "reserve" || events[0].requestID == "session-execution" || events[0].input != wantInput || events[0].output != 42 || events[0].exact != "false" {
		t.Fatalf("reserve events=%+v want_input=%d", events, wantInput)
	}
	firstExecution := events[0].requestID
	if err := tracker.ProviderEvent(t.Context(), []byte(`{"type":"response.created","response":{"id":"resp_1"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ProviderEvent(t.Context(), []byte(`{"type":"response.done","response":{"id":"resp_1","usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ClientEvent(t.Context(), create); err != nil {
		t.Fatal(err)
	}
	tracker.Close(t.Context(), errors.New("client disconnected"))
	events = billing.snapshot()
	if len(events) != 4 || events[1] != (realtimeBillingEvent{phase: "commit", requestID: firstExecution, input: 9, output: 4, exact: "true"}) || events[2].requestID == firstExecution || events[2].requestID == "session-execution" || events[3].phase != "cancel" || events[3].requestID != events[2].requestID {
		t.Fatalf("billing lifecycle=%+v", events)
	}
}

func TestRealtimeBillingBoundsPendingResponsesAndRejectsInvalidUsage(t *testing.T) {
	billing := &realtimeBillingModule{}
	tracker := newRealtimeBillingTracker(modules.NewPipeline([]modules.Module{billing}), modules.RequestContext{Metadata: map[string]string{}}, "model", nil)
	for index := 0; index < maxRealtimePendingResponses; index++ {
		if err := tracker.ClientEvent(t.Context(), []byte(`{"type":"response.create"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tracker.ClientEvent(t.Context(), []byte(`{"type":"response.create"}`)); err == nil {
		t.Fatal("pending response limit was not enforced")
	}
	if err := tracker.ProviderEvent(t.Context(), []byte(`{"type":"response.created","response":{"id":"resp_1"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ProviderEvent(t.Context(), []byte(`{"type":"response.done","response":{"id":"resp_1","usage":{"input_tokens":9,"output_tokens":4,"total_tokens":12}}}`)); err == nil {
		t.Fatal("inconsistent provider usage was accepted")
	}
	tracker.Close(t.Context(), errors.New("closed"))
}

func TestRealtimeBillingFailureStopsEventBeforeProvider(t *testing.T) {
	billing := &realtimeBillingModule{reserveErr: modules.ErrBudgetExceeded}
	var upstreamEvents atomic.Int32
	upstream := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		var event string
		if websocket.Message.Receive(connection, &event) == nil {
			upstreamEvents.Add(1)
		}
	}))
	t.Cleanup(upstream.Close)
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "realtime", Type: "openai", BaseURL: upstream.URL, Models: []string{"model"}, Capabilities: []string{"realtime"}}}})
	pipeline := modules.NewPipeline([]modules.Module{realtimeAuthModule{allowedModels: []string{"model"}}, billing})
	gateway := httptest.NewServer(Routes(NewHandler(pipeline, router)))
	t.Cleanup(gateway.Close)

	connection := dialGatewayRealtime(t, gateway.URL, "/v1/realtime?model=model", "gateway-key")
	if err := websocket.Message.Send(connection, `{"type":"response.create","response":{"max_output_tokens":20}}`); err != nil {
		t.Fatal(err)
	}
	var event string
	if err := websocket.Message.Receive(connection, &event); err != nil || !strings.Contains(event, `"code":"budget_exceeded"`) {
		t.Fatalf("event=%s err=%v", event, err)
	}
	if upstreamEvents.Load() != 0 {
		t.Fatal("budget-rejected response reached the provider")
	}
}

func TestRealtimeCredentialTPMRejectsResponseBeforeProvider(t *testing.T) {
	var upstreamEvents atomic.Int32
	upstream := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		var event string
		if websocket.Message.Receive(connection, &event) == nil {
			upstreamEvents.Add(1)
		}
	}))
	t.Cleanup(upstream.Close)
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "realtime", Type: "openai", BaseURL: upstream.URL, Models: []string{"model"}, Capabilities: []string{"realtime"}}}})
	pipeline := modules.NewPipeline([]modules.Module{realtimeAuthModule{allowedModels: []string{"model"}, tpm: 10}})
	gateway := httptest.NewServer(Routes(NewHandler(pipeline, router)))
	t.Cleanup(gateway.Close)

	connection := dialGatewayRealtime(t, gateway.URL, "/v1/realtime?model=model", "gateway-key")
	if err := websocket.Message.Send(connection, `{"type":"response.create","response":{"max_output_tokens":20}}`); err != nil {
		t.Fatal(err)
	}
	var event string
	if err := websocket.Message.Receive(connection, &event); err != nil || !strings.Contains(event, `"code":"rate_limit_exceeded"`) {
		t.Fatalf("event=%s err=%v", event, err)
	}
	if upstreamEvents.Load() != 0 {
		t.Fatal("credential TPM-rejected response reached the provider")
	}
}

func TestRealtimeInputDLPRejectsTextBeforeProvider(t *testing.T) {
	var upstreamEvents atomic.Int32
	upstream := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
		var event string
		if websocket.Message.Receive(connection, &event) == nil {
			upstreamEvents.Add(1)
		}
	}))
	t.Cleanup(upstream.Close)
	dlp := &realtimeDLPModule{}
	router := provider.New(provider.Config{
		Modules: modules.NewPipeline([]modules.Module{dlp}),
		Endpoints: []config.ProviderEndpointConfig{{
			Name: "realtime", Type: "openai", BaseURL: upstream.URL, Models: []string{"model"},
			Capabilities: []string{"realtime"}, DLPEnabled: true,
		}},
	})
	pipeline := modules.NewPipeline([]modules.Module{realtimeAuthModule{allowedModels: []string{"model"}}, dlp})
	gateway := httptest.NewServer(Routes(NewHandler(pipeline, router)))
	t.Cleanup(gateway.Close)

	connection := dialGatewayRealtime(t, gateway.URL, "/v1/realtime?model=model", "gateway-key")
	if err := websocket.Message.Send(connection, `{"type":"conversation.item.create","item":{"type":"message","content":[{"type":"input_text","text":"secret"},{"type":"input_audio","audio":"not-text"}]}}`); err != nil {
		t.Fatal(err)
	}
	var event string
	if err := websocket.Message.Receive(connection, &event); err != nil || !strings.Contains(event, `"code":"gateway_error"`) {
		t.Fatalf("event=%s err=%v", event, err)
	}
	dlp.mu.Lock()
	content := dlp.content
	dlp.mu.Unlock()
	if !strings.Contains(content, "secret") || strings.Contains(content, "not-text") {
		t.Fatalf("DLP projection=%q", content)
	}
	if upstreamEvents.Load() != 0 {
		t.Fatal("DLP-rejected realtime input reached the provider")
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
