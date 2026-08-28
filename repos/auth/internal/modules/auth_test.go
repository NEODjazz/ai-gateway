package modules

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-gateway-auth/internal/openai"
)

type fakeVirtualKeyStore struct {
	key      StoredVirtualKey
	found    bool
	err      error
	lastHash string
}

func (s *fakeVirtualKeyStore) Lookup(_ context.Context, hash string) (StoredVirtualKey, bool, error) {
	s.lastHash = hash
	return s.key, s.found, s.err
}
func (*fakeVirtualKeyStore) Ready(context.Context) error { return nil }
func (*fakeVirtualKeyStore) Close()                      {}

func TestAuthModuleAcceptsJWT(t *testing.T) {
	secret := "test-secret"
	token := testJWT(t, secret, map[string]any{
		"sub":   "user-123",
		"roles": []string{"developer", "admin"},
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iss":   "ai-gateway-tests",
		"aud":   "ai-gateway",
	})

	module := NewAuthModuleWithJWT(true, JWTAuthConfig{
		Secret:   secret,
		Issuer:   "ai-gateway-tests",
		Audience: "ai-gateway",
	})
	req := RequestContext{APIKey: token}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.UserID != "user-123" {
		t.Fatalf("unexpected user id: %s", req.UserID)
	}
	if strings.Join(req.Roles, ",") != "developer,admin" {
		t.Fatalf("unexpected roles: %v", req.Roles)
	}
}

func TestAuthModuleRejectsExpiredJWT(t *testing.T) {
	secret := "test-secret"
	token := testJWT(t, secret, map[string]any{
		"sub": "user-123",
		"exp": time.Now().Add(-time.Minute).Unix(),
	})

	module := NewAuthModuleWithJWT(true, JWTAuthConfig{Secret: secret})
	req := RequestContext{APIKey: token}

	if err := module.Handle(context.Background(), &req); err == nil {
		t.Fatal("expected expired jwt to be rejected")
	}
}

func TestAuthModuleRejectsInvalidJWTSignature(t *testing.T) {
	token := testJWT(t, "right-secret", map[string]any{
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	module := NewAuthModuleWithJWT(true, JWTAuthConfig{Secret: "wrong-secret"})
	req := RequestContext{APIKey: token}

	if err := module.Handle(context.Background(), &req); err == nil {
		t.Fatal("expected jwt with invalid signature to be rejected")
	}
}

func TestAuthModuleAppliesVirtualKeyPolicy(t *testing.T) {
	module := NewAuthModuleWithVirtualKeys(true, []VirtualKey{{
		Token: "tenant-secret", UserID: "user-7", TeamID: "team-blue",
		Roles: []string{"developer"}, AllowedModels: []string{"gpt-5.*"}, AllowedTools: []string{"mcp.weather.*"},
		RateLimitRPM: 10, RateLimitTPM: 5000,
	}})
	req := RequestContext{APIKey: "tenant-secret"}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.UserID != "user-7" || req.TeamID != "team-blue" || req.RateLimitRPM != 10 || req.RateLimitTPM != 5000 {
		t.Fatalf("unexpected virtual key policy: %+v", req)
	}
	if strings.Join(req.AllowedModels, ",") != "gpt-5.*" || strings.Join(req.AllowedTools, ",") != "mcp.weather.*" || req.APIKey != "" || req.CredentialID == "" {
		t.Fatalf("virtual key was not safely applied: %+v", req)
	}
	if stored := module.virtualKeys[req.CredentialID]; stored.Token != "" {
		t.Fatal("virtual key plaintext must not remain in the in-memory index")
	}
}

func TestAuthUsesPersistentVirtualKeyPolicy(t *testing.T) {
	store := &fakeVirtualKeyStore{found: true, key: StoredVirtualKey{
		ID: "key-id-1", UserID: "user-1", TeamID: "team-1", Roles: []string{"developer"},
		AllowedModels: []string{"gpt-*"}, AllowedTools: []string{"mcp:weather"}, RateLimitRPM: 10, RateLimitTPM: 1000,
	}}
	module := NewAuthModuleWithStore(true, store, "pepper", false)
	req := RequestContext{APIKey: "secret-token"}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if store.lastHash == "" || store.lastHash == "secret-token" || len(store.lastHash) != 64 {
		t.Fatalf("store received an unsafe token lookup value: %q", store.lastHash)
	}
	if req.CredentialID != "key-id-1" || req.UserID != "user-1" || req.TeamID != "team-1" || req.APIKey != "" || req.RateLimitRPM != 10 || len(req.AllowedTools) != 1 {
		t.Fatalf("stored key policy was not applied: %+v", req)
	}
}

func TestAuthAppliesOrganizationOwnedKeyWithSyntheticPrincipal(t *testing.T) {
	store := &fakeVirtualKeyStore{found: true, key: StoredVirtualKey{ID: "vk-org-1", OrganizationID: "org-1", Roles: []string{"developer"}}}
	module := NewAuthModuleWithStore(true, store, "pepper", false)
	req := RequestContext{APIKey: "organization-secret"}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.UserID != "virtual-key:vk-org-1" || req.OrganizationID != "org-1" || req.CredentialID != "vk-org-1" {
		t.Fatalf("organization key scope was not applied: %+v", req)
	}
}

func TestAuthPersistentStoreFailureIsFailClosed(t *testing.T) {
	store := &fakeVirtualKeyStore{err: errors.New("database unavailable")}
	module := NewAuthModuleWithStore(true, store, "pepper", true)
	req := RequestContext{APIKey: "demo-admin-key"}
	if err := module.Handle(context.Background(), &req); err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected dependency failure, got %v", err)
	}
}

func TestAuthCanDisableStaticKeyFallback(t *testing.T) {
	t.Setenv("AUTH_VIRTUAL_KEYS_JSON", `[{"token":"static-token","user_id":"static-user"}]`)
	store := &fakeVirtualKeyStore{}
	module := NewAuthModuleWithStore(true, store, "pepper", false)
	if err := module.Handle(context.Background(), &RequestContext{APIKey: "static-token"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected static key rejection, got %v", err)
	}
}

func TestAuthRequestContextAcceptsMultipartMessageContent(t *testing.T) {
	payload := []byte(`{
		"api_key": "demo-admin-key",
		"request": {
			"model": "gpt-5.3",
			"messages": [
				{
					"role": "user",
					"content": [
						{"type": "text", "text": "hello auth"},
						{"type": "image_url", "image_url": {"url": "data:image/png;base64,abc"}}
					]
				}
			]
		}
	}`)
	var req RequestContext
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatal(err)
	}
	if openai.ContentText(req.Request.Messages[0].Content) != "hello auth" {
		t.Fatalf("unexpected multipart text: %q", openai.ContentText(req.Request.Messages[0].Content))
	}
}

func testJWT(t *testing.T, secret string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "HS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}

	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := encodedHeader + "." + encodedClaims

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + signature
}
