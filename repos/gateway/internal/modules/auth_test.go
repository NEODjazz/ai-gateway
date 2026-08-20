package modules

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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
		AllowedModels: []string{"gpt-5.*"}, RateLimitRPM: 10, RateLimitTPM: 5000,
	}})
	req := RequestContext{APIKey: "tenant-secret"}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.UserID != "user-7" || req.TeamID != "team-blue" || req.RateLimitRPM != 10 || req.RateLimitTPM != 5000 {
		t.Fatalf("unexpected virtual key policy: %+v", req)
	}
	if strings.Join(req.AllowedModels, ",") != "gpt-5.*" || req.APIKey != "" || req.CredentialID == "" {
		t.Fatalf("virtual key was not safely applied: %+v", req)
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
