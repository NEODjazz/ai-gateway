package modules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBackgroundJWTAuthorizationRequiresInternalService(t *testing.T) {
	r := RequestContext{JWTIdentity: &JWTIdentity{Issuer: "issuer", Subject: "sub", Audience: "gateway", PolicyDigest: "digest"}, CredentialID: "jwt:principal", UserID: "user", RequestID: "execution", Roles: []string{"user"}}
	if err := NewPipeline(nil).ReauthorizeBackground(t.Context(), r); !errors.Is(err, ErrBackgroundAuthorizationUnavailable) {
		t.Fatal(err)
	}
	r.JWTIdentity = nil
	r.ModelAccessRestricted = true
	if err := NewPipeline(nil).ReauthorizeBackground(t.Context(), r); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if err := NewPipeline(nil).ReauthorizeBackground(t.Context(), RequestContext{CredentialID: "virtual-key"}); err != nil {
		t.Fatal(err)
	}
}
func TestRemoteJWTReauthorizationDoesNotForwardJWT(t *testing.T) {
	for _, status := range []int{200, 401, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/v1/jwt-principals:reauthorize" || r.Header.Get("X-Management-Token") != "internal" || r.Header.Get("X-Actor-Credential-ID") != "jwt:principal" {
					t.Error("missing protected identity channel")
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if _, exists := body["token"]; exists {
					t.Error("raw JWT persisted/forwarded")
				}
				w.WriteHeader(status)
				if status == 200 {
					_, _ = w.Write([]byte(`{"authorized":true}`))
				}
			}))
			defer server.Close()
			m := NewRemoteAuthModule(true, server.URL+"/authorize").WithJWTReauthorization(server.URL, "internal")
			err := m.ReauthorizeBackground(context.Background(), RequestContext{APIKey: "raw-private-token", JWTIdentity: &JWTIdentity{Subject: "sub"}, CredentialID: "jwt:principal", UserID: "user", RequestID: "execution"})
			if status == 200 && err != nil {
				t.Fatal(err)
			}
			if status == 401 && !errors.Is(err, ErrUnauthorized) {
				t.Fatal(err)
			}
			if status == 503 && (err == nil || errors.Is(err, ErrUnauthorized)) {
				t.Fatal("directory outage treated as revocation")
			}
		})
	}
}
