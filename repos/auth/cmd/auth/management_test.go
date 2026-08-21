package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-auth/internal/modules"
)

type commandManagementStore struct {
	created modules.StoredVirtualKey
}

func (s *commandManagementStore) Lookup(context.Context, string) (modules.StoredVirtualKey, bool, error) {
	return modules.StoredVirtualKey{}, false, nil
}
func (s *commandManagementStore) Ready(context.Context) error { return nil }
func (s *commandManagementStore) Close()                      {}
func (s *commandManagementStore) Create(_ context.Context, key modules.StoredVirtualKey, _ string) error {
	s.created = key
	return nil
}
func (s *commandManagementStore) Revoke(context.Context, string) (bool, error) { return true, nil }
func (s *commandManagementStore) Rotate(context.Context, string, modules.StoredVirtualKey, string) error {
	return nil
}

func TestInternalManagementRequiresScopedSecretAndAuditIdentity(t *testing.T) {
	store := &commandManagementStore{}
	module := modules.NewAuthModuleWithStore(true, store, "hash-secret", false)
	mux := http.NewServeMux()
	registerManagementRoutes(mux, &module, "internal-secret")

	unauthorized := httptest.NewRequest(http.MethodPost, "/internal/v1/keys", strings.NewReader(`{"user_id":"user-1"}`))
	unauthorizedResponse := httptest.NewRecorder()
	mux.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected unauthorized status: %d", unauthorizedResponse.Code)
	}

	missingAudit := httptest.NewRequest(http.MethodPost, "/internal/v1/keys", strings.NewReader(`{"user_id":"user-1"}`))
	missingAudit.Header.Set(managementTokenHeader, "internal-secret")
	missingAuditResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingAuditResponse, missingAudit)
	if missingAuditResponse.Code != http.StatusBadRequest {
		t.Fatalf("missing audit identity was accepted: %d", missingAuditResponse.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/internal/v1/keys", strings.NewReader(`{"user_id":"user-1","roles":["developer"]}`))
	request.Header.Set(managementTokenHeader, "internal-secret")
	request.Header.Set("X-Request-ID", "req-1")
	request.Header.Set("X-Actor-ID", "admin-user")
	request.Header.Set("X-Actor-Credential-ID", "fingerprint")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("unexpected create status %d: %s", response.Code, response.Body.String())
	}
	var issued modules.IssuedVirtualKey
	if err := json.NewDecoder(response.Body).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	if issued.Token == "" || store.created.UserID != "user-1" {
		t.Fatalf("key was not issued/persisted: issued=%+v stored=%+v", issued, store.created)
	}
}
