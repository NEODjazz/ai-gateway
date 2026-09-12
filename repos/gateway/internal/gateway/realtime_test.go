package gateway

import (
	"context"
	"encoding/base64"
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
	"ai-gateway-gateway/internal/openai"
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
	inputAudio       int
	outputAudio      int
	cached           int
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

type realtimeAVModule struct {
	mu          sync.Mutex
	attachments []openai.ImageAttachment
	reject      bool
}

func (*realtimeAVModule) Name() string              { return "av" }
func (*realtimeAVModule) Required() bool            { return true }
func (*realtimeAVModule) PostResponseEnabled() bool { return false }
func (m *realtimeAVModule) Handle(_ context.Context, request *modules.RequestContext) error {
	m.mu.Lock()
	m.attachments = append([]openai.ImageAttachment(nil), request.Attachments...)
	m.mu.Unlock()
	if m.reject {
		return modules.ErrContentRejected
	}
	return nil
}
func (*realtimeAVModule) HandlePostResponse(context.Context, *modules.RequestContext) error {
	return nil
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
		if request.Usage.PromptTokensDetails != nil {
			event.inputAudio = request.Usage.PromptTokensDetails.AudioTokens
			event.cached = request.Usage.PromptTokensDetails.CachedTokens
		}
		if request.Usage.CompletionTokensDetails != nil {
			event.outputAudio = request.Usage.CompletionTokensDetails.AudioTokens
		}
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

func TestRealtimeBillingSettlesClientCancellationAndIgnoresLateDone(t *testing.T) {
	billing := &realtimeBillingModule{}
	tracker := newRealtimeBillingTracker(modules.NewPipeline([]modules.Module{billing}), modules.RequestContext{Metadata: map[string]string{}}, "model", nil)
	if err := tracker.ClientEvent(t.Context(), []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ProviderEvent(t.Context(), []byte(`{"type":"response.created","response":{"id":"resp_1"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ClientEvent(t.Context(), []byte(`{"type":"response.cancel","response_id":"resp_1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ProviderEvent(t.Context(), []byte(`{"type":"response.done","response":{"id":"resp_1","status":"cancelled","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)); err != nil {
		t.Fatalf("late cancellation terminal event was not idempotent: %v", err)
	}
	tracker.Close(t.Context(), errors.New("closed"))
	events := billing.snapshot()
	if len(events) != 2 || events[0].phase != "reserve" || events[1].phase != "cancel" || events[1].requestID != events[0].requestID {
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

func TestRealtimeAudioLifecycleScansAndAccountsCommittedInput(t *testing.T) {
	billing := &realtimeBillingModule{}
	av := &realtimeAVModule{}
	metadata := map[string]string{
		"provider.realtime_audio_input.enabled":  "true",
		"provider.realtime_audio_output.enabled": "true",
		"provider.modules.av.enabled":            "true",
	}
	tracker := newRealtimeBillingTracker(modules.NewPipeline([]modules.Module{billing, av}), modules.RequestContext{Metadata: metadata}, "model", nil)
	session := json.RawMessage(`{"audio":{"input":{"format":{"type":"audio/pcm","rate":24000}},"output":{"format":{"type":"audio/pcmu"}}}}`)
	if err := tracker.ClientEvent(t.Context(), append([]byte(`{"type":"session.update","session":`), append(session, '}')...)); err != nil {
		t.Fatal(err)
	}
	audio := base64.StdEncoding.EncodeToString(make([]byte, 4800))
	if err := tracker.ClientEvent(t.Context(), []byte(`{"type":"input_audio_buffer.append","audio":"`+audio+`"}`)); err != nil {
		t.Fatal(err)
	}
	av.mu.Lock()
	attachments := append([]openai.ImageAttachment(nil), av.attachments...)
	av.mu.Unlock()
	if len(attachments) != 1 || attachments[0].MediaType != "audio/pcm" || attachments[0].Data != audio {
		t.Fatalf("AV attachments=%+v", attachments)
	}
	if err := tracker.ClientEvent(t.Context(), []byte(`{"type":"input_audio_buffer.commit"}`)); err != nil {
		t.Fatal(err)
	}
	tracker.mu.Lock()
	if tracker.conversationTokens != 1 || tracker.unnamedItems != 1 || len(tracker.pendingAudio) != 1 {
		t.Fatalf("committed audio tokens=%d unnamed=%d pending=%v", tracker.conversationTokens, tracker.unnamedItems, tracker.pendingAudio)
	}
	tracker.mu.Unlock()
	if err := tracker.ProviderEvent(t.Context(), []byte(`{"type":"input_audio_buffer.committed","item_id":"audio_1"}`)); err != nil {
		t.Fatal(err)
	}
	response := json.RawMessage(`{"max_output_tokens":2}`)
	if err := tracker.ClientEvent(t.Context(), append([]byte(`{"type":"response.create","response":`), append(response, '}')...)); err != nil {
		t.Fatal(err)
	}
	wantInput := saturatedRealtimeTokens(realtimeRawTokens(session), 1)
	wantInput = saturatedRealtimeTokens(wantInput, realtimeRawTokens(response))
	events := billing.snapshot()
	if len(events) != 1 || events[0].input != wantInput || events[0].output != 2 {
		t.Fatalf("audio reserve=%+v want_input=%d", events, wantInput)
	}
	tracker.deleteConversationItem("audio_1")
	tracker.mu.Lock()
	if tracker.conversationTokens != 0 || tracker.unnamedItems != 0 || len(tracker.pendingAudio) != 0 {
		t.Fatalf("deleted audio tokens=%d unnamed=%d pending=%v", tracker.conversationTokens, tracker.unnamedItems, tracker.pendingAudio)
	}
	tracker.mu.Unlock()
	tracker.Close(t.Context(), errors.New("closed"))
}

func TestRealtimeAudioServerCommitAndLimits(t *testing.T) {
	if provider.MaxRealtimeEventBytes <= base64.StdEncoding.EncodedLen(maxRealtimeAudioAppendBytes)+64 {
		t.Fatalf("realtime event limit %d cannot carry a 15 MiB audio append", provider.MaxRealtimeEventBytes)
	}
	metadata := map[string]string{"provider.realtime_audio_input.enabled": "true"}
	tracker := newRealtimeBillingTracker(modules.NewPipeline(nil), modules.RequestContext{Metadata: metadata}, "model", func(context.Context, int) error { return nil })
	audio := base64.StdEncoding.EncodeToString(make([]byte, 800))
	if err := tracker.ClientEvent(t.Context(), []byte(`{"type":"session.update","session":{"input_audio_format":"g711_ulaw"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ClientEvent(t.Context(), []byte(`{"type":"input_audio_buffer.append","audio":"`+audio+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.ProviderEvent(t.Context(), []byte(`{"type":"input_audio_buffer.committed","item_id":"server_audio"}`)); err != nil {
		t.Fatal(err)
	}
	tracker.mu.Lock()
	if tracker.conversationItems["server_audio"] != 1 || tracker.conversationTokens != 1 || tracker.audioBufferBytes != 0 {
		t.Fatalf("server commit items=%v tokens=%d buffered=%d", tracker.conversationItems, tracker.conversationTokens, tracker.audioBufferBytes)
	}
	tracker.audioBufferBytes = maxRealtimeAudioBufferBytes
	tracker.mu.Unlock()
	if err := tracker.ClientEvent(t.Context(), []byte(`{"type":"input_audio_buffer.append","audio":"YQ=="}`)); err == nil || !strings.Contains(err.Error(), "buffer exceeds") {
		t.Fatalf("buffer overflow error=%v", err)
	}
}

func TestRealtimeAudioRejectsUnsupportedInvalidAndUnsafeEvents(t *testing.T) {
	withoutAudio := newRealtimeBillingTracker(modules.NewPipeline(nil), modules.RequestContext{Metadata: map[string]string{}}, "model", func(context.Context, int) error { return nil })
	if err := withoutAudio.ClientEvent(t.Context(), []byte(`{"type":"input_audio_buffer.append","audio":"YQ=="}`)); err == nil || !strings.Contains(err.Error(), "does not support audio input") {
		t.Fatalf("unsupported input error=%v", err)
	}
	if err := withoutAudio.ClientEvent(t.Context(), []byte(`{"type":"session.update","session":{"modalities":["audio"]}}`)); err == nil || !strings.Contains(err.Error(), "does not support audio output") {
		t.Fatalf("unsupported output error=%v", err)
	}
	if err := withoutAudio.ProviderEvent(t.Context(), []byte(`{"type":"response.output_audio.delta","delta":"YQ=="}`)); err == nil || !strings.Contains(err.Error(), "unsupported audio output") {
		t.Fatalf("provider capability error=%v", err)
	}

	withAudio := newRealtimeBillingTracker(modules.NewPipeline(nil), modules.RequestContext{Metadata: map[string]string{
		"provider.realtime_audio_input.enabled": "true", "provider.realtime_audio_output.enabled": "true",
	}}, "model", func(context.Context, int) error { return nil })
	if err := withAudio.ClientEvent(t.Context(), []byte(`{"type":"input_audio_buffer.append","audio":"%%%"}`)); err == nil || !strings.Contains(err.Error(), "strict base64") {
		t.Fatalf("invalid input error=%v", err)
	}
	if err := withAudio.ProviderEvent(t.Context(), []byte(`{"type":"response.output_audio.delta","delta":"%%%"}`)); err == nil || !strings.Contains(err.Error(), "strict base64") {
		t.Fatalf("invalid output error=%v", err)
	}
	if err := withAudio.ClientEvent(t.Context(), []byte(`{"type":"input_audio_buffer.commit"}`)); err == nil || !strings.Contains(err.Error(), "buffer is empty") {
		t.Fatalf("empty commit error=%v", err)
	}

	rejectingAV := &realtimeAVModule{reject: true}
	scanned := newRealtimeBillingTracker(modules.NewPipeline([]modules.Module{rejectingAV}), modules.RequestContext{Metadata: map[string]string{
		"provider.realtime_audio_input.enabled": "true", "provider.modules.av.enabled": "true",
	}}, "model", func(context.Context, int) error { return nil })
	if err := scanned.ClientEvent(t.Context(), []byte(`{"type":"input_audio_buffer.append","audio":"YQ=="}`)); err == nil || !errors.Is(err, modules.ErrContentRejected) {
		t.Fatalf("AV rejection error=%v", err)
	}
	scanned.mu.Lock()
	buffered := scanned.audioBufferBytes
	scanned.mu.Unlock()
	if buffered != 0 {
		t.Fatalf("AV-rejected audio buffered=%d", buffered)
	}
}

func TestRealtimeUsagePreservesAudioAndCacheDetails(t *testing.T) {
	responseID, usage, err := realtimeResponseUsage(json.RawMessage(`{"id":"resp_audio","usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17,"input_token_details":{"cached_tokens":4,"text_tokens":7,"audio_tokens":5},"output_token_details":{"text_tokens":2,"audio_tokens":3}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if responseID != "resp_audio" || usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens != 4 || usage.PromptTokensDetails.AudioTokens != 5 || usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.AudioTokens != 3 {
		t.Fatalf("response_id=%q usage=%+v", responseID, usage)
	}
	for _, payload := range []string{
		`{"id":"bad","usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1,"input_token_details":{"audio_tokens":2}}}`,
		`{"id":"bad","usage":{"input_tokens":0,"output_tokens":1,"total_tokens":1,"output_token_details":{"text_tokens":1,"audio_tokens":1}}}`,
	} {
		if _, _, err := realtimeResponseUsage(json.RawMessage(payload)); err == nil {
			t.Fatalf("invalid token details accepted: %s", payload)
		}
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
