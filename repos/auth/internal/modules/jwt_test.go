package modules

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestJWKSAuthMapsNestedClaimsAndRefreshesRotatedKey(t *testing.T) {
	firstKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	active := atomic.Pointer[rsa.PublicKey]{}
	active.Store(&firstKey.PublicKey)
	var activeID atomic.Value
	activeID.Store("key-1")
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jsonWebKey{rsaJWK(activeID.Load().(string), active.Load())}})
	}))
	defer server.Close()

	module := NewAuthModuleWithJWT(true, JWTAuthConfig{
		JWKSURL: server.URL, Issuer: "https://issuer.example", Audience: "ai-gateway", JWKSCacheTTL: time.Hour,
		UserIDClaim: "identity.uid", TeamIDClaim: "tenant.id", RolesClaim: "realm.roles",
	})
	claims := map[string]any{
		"identity": map[string]any{"uid": "user-42"}, "tenant": map[string]any{"id": "team-blue"},
		"realm": map[string]any{"roles": []string{"developer", "admin"}},
		"iss":   "https://issuer.example", "aud": []string{"other", "ai-gateway"}, "exp": time.Now().Add(time.Hour).Unix(),
	}
	firstToken := signRS256JWT(t, "key-1", firstKey, claims)
	request := RequestContext{APIKey: firstToken}
	if err := module.Handle(context.Background(), &request); err != nil {
		t.Fatal(err)
	}
	if request.UserID != "user-42" || request.TeamID != "team-blue" || len(request.Roles) != 2 || request.CredentialID == "" || request.APIKey != "" {
		t.Fatalf("unexpected mapped identity: %+v", request)
	}

	request = RequestContext{APIKey: firstToken}
	if err := module.Handle(context.Background(), &request); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("JWKS cache was not reused, requests=%d", requests.Load())
	}

	active.Store(&secondKey.PublicKey)
	activeID.Store("key-2")
	request = RequestContext{APIKey: signRS256JWT(t, "key-2", secondKey, claims)}
	if err := module.Handle(context.Background(), &request); err != nil {
		t.Fatalf("rotated signing key was not accepted: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("unknown kid did not force one JWKS refresh, requests=%d", requests.Load())
	}
}

func TestJWKSAuthAcceptsES256(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jsonWebKey{ecJWK("ec-1", &privateKey.PublicKey)}})
	}))
	defer server.Close()
	module := NewAuthModuleWithJWT(true, JWTAuthConfig{JWKSURL: server.URL, Issuer: "issuer", Audience: "audience"})
	token := signES256JWT(t, "ec-1", privateKey, map[string]any{
		"sub": "user", "iss": "issuer", "aud": "audience", "exp": time.Now().Add(time.Hour).Unix(),
	})
	if err := module.Handle(context.Background(), &RequestContext{APIKey: token}); err != nil {
		t.Fatal(err)
	}
}

func TestJWKSAuthFailsClosedForClaimsAlgorithmAndDependency(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	status := atomic.Int64{}
	status.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if current := int(status.Load()); current != http.StatusOK {
			http.Error(w, "unavailable", current)
			return
		}
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jsonWebKey{rsaJWK("key", &privateKey.PublicKey)}})
	}))
	defer server.Close()
	config := JWTAuthConfig{JWKSURL: server.URL, Issuer: "issuer", Audience: "audience"}

	invalidClaims := []map[string]any{
		{"sub": "user", "iss": "wrong", "aud": "audience", "exp": time.Now().Add(time.Hour).Unix()},
		{"sub": "user", "iss": "issuer", "aud": "wrong", "exp": time.Now().Add(time.Hour).Unix()},
		{"sub": "user", "iss": "issuer", "aud": "audience", "exp": time.Now().Add(-time.Minute).Unix()},
		{"sub": "user", "iss": "issuer", "aud": "audience"},
	}
	for _, claims := range invalidClaims {
		module := NewAuthModuleWithJWT(true, config)
		if err := module.Handle(context.Background(), &RequestContext{APIKey: signRS256JWT(t, "key", privateKey, claims)}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("invalid claims were not rejected: claims=%v err=%v", claims, err)
		}
	}
	module := NewAuthModuleWithJWT(true, config)
	hsToken := testJWT(t, "irrelevant", map[string]any{"sub": "user", "iss": "issuer", "aud": "audience", "exp": time.Now().Add(time.Hour).Unix()})
	if err := module.Handle(context.Background(), &RequestContext{APIKey: hsToken}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("HS256 was accepted in JWKS mode: %v", err)
	}

	status.Store(http.StatusServiceUnavailable)
	module = NewAuthModuleWithJWT(true, config)
	if err := module.Ready(context.Background()); !errors.Is(err, ErrJWTUnavailable) {
		t.Fatalf("JWKS outage was not exposed as dependency failure: %v", err)
	}
	valid := signRS256JWT(t, "key", privateKey, map[string]any{"sub": "user", "iss": "issuer", "aud": "audience", "exp": time.Now().Add(time.Hour).Unix()})
	if err := module.Handle(context.Background(), &RequestContext{APIKey: valid}); !errors.Is(err, ErrJWTUnavailable) {
		t.Fatalf("JWKS outage was converted to unauthorized: %v", err)
	}
}

func TestJWKSConfigurationRequiresIssuerAndAudience(t *testing.T) {
	module := NewAuthModuleWithJWT(true, JWTAuthConfig{JWKSURL: "https://issuer.example/jwks"})
	if err := module.Ready(context.Background()); err == nil {
		t.Fatal("incomplete JWKS configuration was accepted")
	}
}

func rsaJWK(keyID string, key *rsa.PublicKey) jsonWebKey {
	return jsonWebKey{KeyID: keyID, KeyType: "RSA", Algorithm: "RS256", Use: "sig",
		N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()), E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}
}

func ecJWK(keyID string, key *ecdsa.PublicKey) jsonWebKey {
	return jsonWebKey{KeyID: keyID, KeyType: "EC", Algorithm: "ES256", Use: "sig", Curve: "P-256",
		X: base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))), Y: base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32)))}
}

func signingInput(t *testing.T, algorithm, keyID string, claims map[string]any) string {
	t.Helper()
	headerJSON, err := json.Marshal(map[string]any{"alg": algorithm, "typ": "JWT", "kid": keyID})
	if err != nil {
		t.Fatal(err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
}

func signRS256JWT(t *testing.T, keyID string, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	input := signingInput(t, "RS256", keyID, claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func signES256JWT(t *testing.T, keyID string, key *ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	input := signingInput(t, "ES256", keyID, claims)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}
