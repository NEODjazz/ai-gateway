package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-billing/internal/modules"
)

type fakeAuditStore struct {
	appended modules.ManagementAuditEvent
	events   []modules.ManagementAuditEvent
}

func (f *fakeAuditStore) Append(_ context.Context, event modules.ManagementAuditEvent) (modules.ManagementAuditEvent, error) {
	event.ID = 1
	f.appended = event
	return event, nil
}
func (f *fakeAuditStore) List(context.Context, modules.AuditFilter) ([]modules.ManagementAuditEvent, error) {
	return f.events, nil
}
func (f *fakeAuditStore) Ready(context.Context) error { return nil }
func (f *fakeAuditStore) Close()                      {}

func TestAuditManagementAppendsAuthoritativeIdentity(t *testing.T) {
	store := &fakeAuditStore{}
	mux := http.NewServeMux()
	registerAuditManagement(mux, store, nil, "secret")
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/audit/events", strings.NewReader(`{"action":"budget.update","target_type":"budget","target_id":"42","outcome":"attempted"}`))
	request.Header.Set("X-Management-Token", "secret")
	request.Header.Set("X-Request-ID", "req-1")
	request.Header.Set("X-Actor-ID", "admin")
	request.Header.Set("X-Actor-Credential-ID", "credential")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || store.appended.ActorID != "admin" || store.appended.RequestID != "req-1" {
		t.Fatalf("status=%d event=%+v", response.Code, store.appended)
	}
}

func TestAuditManagementRejectsMissingIdentityAndBadFilter(t *testing.T) {
	store := &fakeAuditStore{}
	mux := http.NewServeMux()
	registerAuditManagement(mux, store, nil, "secret")
	missing := httptest.NewRequest(http.MethodGet, "/internal/v1/audit/events", nil)
	missing.Header.Set("X-Management-Token", "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, missing)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing identity status=%d", response.Code)
	}
	bad := httptest.NewRequest(http.MethodGet, "/internal/v1/audit/events?limit=nope", nil)
	bad.Header.Set("X-Management-Token", "secret")
	bad.Header.Set("X-Request-ID", "r")
	bad.Header.Set("X-Actor-ID", "a")
	bad.Header.Set("X-Actor-Credential-ID", "c")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, bad)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("bad filter status=%d", response.Code)
	}
}
