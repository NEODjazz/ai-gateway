package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestLoggingModuleDeliversMetadataOnly(t *testing.T) {
	received := make(chan map[string]any, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["authorization"] = r.Header.Get("Authorization")
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	registry := NewLoggingRegistry(server.Client())
	_, err := registry.Put("security-log", LoggingDestination{Name: "Security log", Type: "webhook", URL: server.URL, EventTypes: []string{"request_outcome"}, Enabled: true}, "destination-secret")
	if err != nil {
		t.Fatal(err)
	}
	module := NewLoggingModule(registry).(interface {
		HandlePostResponse(context.Context, *modules.RequestContext) error
	})
	req := &modules.RequestContext{RequestID: "req-1", SessionID: "session-1", CredentialID: "vk_1", UserID: "user-1", TeamID: "team-1", Request: openai.ChatCompletionRequest{Model: "gpt-test", Messages: []openai.Message{{Role: "user", Content: "sensitive fixture"}}}, Metadata: map[string]string{"provider.id": "azure", "provider.endpoint.name": "primary"}, Usage: &openai.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}}
	if err := module.HandlePostResponse(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-received:
		encoded, _ := json.Marshal(body)
		if strings.Contains(string(encoded), "sensitive fixture") || body["request_id"] != "req-1" || body["authorization"] != "Bearer destination-secret" || body["status"] != "ok" {
			t.Fatalf("unsafe logging payload: %s", encoded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("logging delivery timed out")
	}
}

func TestLoggingDestinationAdminAPIHidesSecretAndProbes(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	audit := &recordingAuditClient{}
	registry := NewLoggingRegistry(server.Client())
	handler := NewHandler(modulesPipeline("admin"), nil).WithLoggingRegistry(registry).WithAudit(audit)
	router := Routes(handler)
	put := httptest.NewRecorder()
	router.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/admin/v1/logging/destinations/security", strings.NewReader(`{"name":"Security","type":"webhook","url":"`+server.URL+`","event_types":["request_outcome"],"enabled":true,"secret":"do-not-return"}`)))
	if put.Code != http.StatusOK || strings.Contains(put.Body.String(), "do-not-return") || !strings.Contains(put.Body.String(), `"secret_configured":true`) || len(audit.events) != 2 {
		t.Fatalf("unsafe put response: status=%d body=%s audit=%+v", put.Code, put.Body.String(), audit.events)
	}
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/v1/logging/destinations", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "do-not-return") || !strings.Contains(list.Body.String(), `"content_stored":false`) {
		t.Fatalf("unsafe list response: status=%d body=%s", list.Code, list.Body.String())
	}
	probe := httptest.NewRecorder()
	router.ServeHTTP(probe, httptest.NewRequest(http.MethodPost, "/admin/v1/logging/destinations/security/test", nil))
	if probe.Code != http.StatusOK || !strings.Contains(probe.Body.String(), `"content_sent":false`) {
		t.Fatalf("probe status=%d body=%s", probe.Code, probe.Body.String())
	}
	if len(audit.events) != 4 || audit.events[2].Action != "logging_destination.test" || audit.events[2].Outcome != "attempted" || audit.events[3].Outcome != "succeeded" {
		t.Fatalf("probe audit=%+v", audit.events)
	}
}

func TestLoggingDestinationRejectsUnsafeURL(t *testing.T) {
	registry := NewLoggingRegistry(nil)
	for _, endpoint := range []string{"http://logs.example.test", "https://user:secret@logs.example.test", "https://logs.example.test?token=secret", "https://logs.example.test#secret"} {
		if _, err := registry.Put("unsafe", LoggingDestination{Name: "Unsafe", Type: "webhook", URL: endpoint, EventTypes: []string{"request_outcome"}}, ""); err == nil {
			t.Fatalf("unsafe URL accepted: %s", endpoint)
		}
	}
}

func TestLoggingDestinationProbeFailsClosedOnAuditAndRecordsDeliveryFailure(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	registry := NewLoggingRegistry(server.Client())
	if _, err := registry.Put("security", LoggingDestination{Name: "Security", Type: "webhook", URL: server.URL, EventTypes: []string{"request_outcome"}, Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	audit := &recordingAuditClient{appendErr: errors.New("audit unavailable")}
	router := Routes(NewHandler(modulesPipeline("admin"), nil).WithLoggingRegistry(registry).WithAudit(audit))
	blocked := httptest.NewRecorder()
	router.ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, "/admin/v1/logging/destinations/security/test", nil))
	if blocked.Code != http.StatusServiceUnavailable || requests != 0 {
		t.Fatalf("audit preflight did not block probe: status=%d requests=%d", blocked.Code, requests)
	}
	audit.appendErr = nil
	failed := httptest.NewRecorder()
	router.ServeHTTP(failed, httptest.NewRequest(http.MethodPost, "/admin/v1/logging/destinations/security/test", nil))
	if failed.Code != http.StatusBadGateway || requests != 1 || len(audit.events) != 2 || audit.events[0].Outcome != "attempted" || audit.events[1].Outcome != "failed" {
		t.Fatalf("failed probe status=%d requests=%d audit=%+v", failed.Code, requests, audit.events)
	}
}
