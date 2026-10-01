package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-auth/internal/modules"
)

func TestManagementLogFingerprintsExternalRequestID(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })

	const externalID = "private-request-id"
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/keys", nil)
	request.Header.Set("X-Request-ID", externalID)
	request.Header.Set("X-Actor-ID", "operator")
	request.Header.Set("X-Actor-Credential-ID", "credential")
	logManagementAction(request, "virtual_key.create", "key-1")

	message := output.String()
	expected := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(externalID)))
	if !strings.Contains(message, expected) || strings.Contains(message, externalID) {
		t.Fatalf("management log leaked external request ID: %q", message)
	}
	if !strings.Contains(message, `"actor_id":"operator"`) || !strings.Contains(message, `"target_id":"key-1"`) {
		t.Fatalf("management log lost audit identity: %q", message)
	}
}

type commandManagementStore struct {
	created modules.StoredVirtualKey
	listed  []modules.VirtualKeyMetadata
	query   modules.VirtualKeyListQuery
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
func (s *commandManagementStore) List(context.Context, int) ([]modules.VirtualKeyMetadata, error) {
	return s.listed, nil
}
func (s *commandManagementStore) ListPage(_ context.Context, query modules.VirtualKeyListQuery) (modules.VirtualKeyPage, error) {
	s.query = query
	return modules.VirtualKeyPage{Data: s.listed, Total: 27, Limit: query.Limit, Offset: query.Offset}, nil
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

	request := httptest.NewRequest(http.MethodPost, "/internal/v1/keys", strings.NewReader(`{"user_id":"user-1","roles":["developer"],"access_group_ids":["platform"]}`))
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
	if issued.Token == "" || store.created.UserID != "user-1" || len(store.created.AccessGroupIDs) != 1 || store.created.AccessGroupIDs[0] != "platform" {
		t.Fatalf("key was not issued/persisted: issued=%+v stored=%+v", issued, store.created)
	}
}

func TestInternalManagementListsOnlySafeVirtualKeyMetadata(t *testing.T) {
	store := &commandManagementStore{listed: []modules.VirtualKeyMetadata{{ID: "vk_safe123", UserID: "user-1", RotationFamily: "vk_safe123"}}}
	module := modules.NewAuthModuleWithStore(true, store, "hash-secret", false)
	mux := http.NewServeMux()
	registerManagementRoutes(mux, &module, "internal-secret")
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/keys?limit=25&offset=25&search=prod&organization_id=org-1&team_id=team-1&user_id=user-1&key_id=safe&access_group_id=platform&access_group_id=regulated&status=non_revoked&sort_by=alias&sort_order=asc", nil)
	request.Header.Set(managementTokenHeader, "internal-secret")
	request.Header.Set("X-Request-ID", "req-list")
	request.Header.Set("X-Actor-ID", "admin-user")
	request.Header.Set("X-Actor-Credential-ID", "fingerprint")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"vk_safe123"`) {
		t.Fatalf("unexpected list response %d: %s", response.Code, response.Body.String())
	}
	if store.query.Offset != 25 || store.query.Search != "prod" || store.query.OrganizationID != "org-1" || len(store.query.AccessGroupIDs) != 2 || store.query.AccessGroupIDs[0] != "platform" || store.query.AccessGroupIDs[1] != "regulated" || store.query.Status != "non_revoked" || store.query.SortBy != "alias" || store.query.SortOrder != "asc" || !strings.Contains(response.Body.String(), `"total":27`) {
		t.Fatalf("list query was not propagated: query=%+v body=%s", store.query, response.Body.String())
	}
	for _, forbidden := range []string{"token_hash", `"token"`} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("list response exposed secret field %q: %s", forbidden, response.Body.String())
		}
	}
	invalid := httptest.NewRequest(http.MethodGet, "/internal/v1/keys?limit=25&sort_by=token_hash", nil)
	invalid.Header = request.Header.Clone()
	invalidResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid sort was accepted: %d", invalidResponse.Code)
	}
}

func (s *commandManagementStore) PutJWTPrincipal(_ context.Context, p modules.JWTPrincipalPolicy) (modules.JWTPrincipalPolicy, error) {
	return p, nil
}
func (s *commandManagementStore) ListJWTPrincipals(_ context.Context, _ string, offset, limit int) (modules.JWTPrincipalPage, error) {
	return modules.JWTPrincipalPage{Data: []modules.JWTPrincipalPolicy{}, Total: 7, Offset: offset, Limit: limit}, nil
}
func TestInternalJWTPrincipalManagementProtection(t *testing.T) {
	store := &commandManagementStore{}
	module := modules.NewAuthModuleWithStore(true, store, "hash-secret", false)
	mux := http.NewServeMux()
	registerManagementRoutes(mux, &module, "internal-secret")
	for _, test := range []struct {
		name, body, secret string
		want               int
	}{
		{"no secret", `{}`, "", 401},
		{"valid", `{"issuer":"https://idp.test/realm","subject":"external","audience":"gateway","user_id":"user-1","enabled":true}`, "internal-secret", 200},
		{"unknown fields", `{"issuer":"https://idp.test/realm","subject":"external","audience":"gateway","user_id":"user-1","enabled":true,"roles":["admin"]}`, "internal-secret", 400},
		{"invalid", `{"issuer":"https://idp.test/realm","subject":"external","audience":"gateway","user_id":"user-1","rate_limit_tpm":-1}`, "internal-secret", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, "/internal/v1/jwt-principals", strings.NewReader(test.body))
			r.Header.Set(managementTokenHeader, test.secret)
			r.Header.Set("X-Request-ID", "req")
			r.Header.Set("X-Actor-ID", "operator")
			r.Header.Set("X-Actor-Credential-ID", "key")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("got %d want %d", w.Code, test.want)
			}
		})
	}
}
