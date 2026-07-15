package modules

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

type AuthModule struct {
	required  bool
	jwtConfig JWTAuthConfig
}

func NewAuthModule(required bool) AuthModule {
	return AuthModule{required: required, jwtConfig: JWTAuthConfigFromEnv()}
}

func NewAuthModuleWithJWT(required bool, cfg JWTAuthConfig) AuthModule {
	return AuthModule{required: required, jwtConfig: cfg}
}

type JWTAuthConfig struct {
	Secret   string
	Issuer   string
	Audience string
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

type jwtClaims struct {
	Subject   string   `json:"sub"`
	Roles     []string `json:"roles"`
	Role      string   `json:"role"`
	ExpiresAt int64    `json:"exp"`
	NotBefore int64    `json:"nbf"`
	Issuer    string   `json:"iss"`
	Audience  any      `json:"aud"`
}

func JWTAuthConfigFromEnv() JWTAuthConfig {
	return JWTAuthConfig{
		Secret:   os.Getenv("AUTH_JWT_SECRET"),
		Issuer:   os.Getenv("AUTH_JWT_ISSUER"),
		Audience: os.Getenv("AUTH_JWT_AUDIENCE"),
	}
}

func (m AuthModule) Name() string {
	return "auth"
}

func (m AuthModule) Required() bool {
	return m.required
}

func (m AuthModule) Handle(_ context.Context, req *RequestContext) error {
	switch req.APIKey {
	case "demo-admin-key":
		req.UserID = "demo-admin"
		req.Roles = []string{"admin", "developer"}
		return nil
	case "demo-user-key":
		req.UserID = "demo-user"
		req.Roles = []string{"developer"}
		return nil
	}

	if err := m.authorizeJWT(req); err == nil {
		return nil
	}

	return ErrUnauthorized
}

func (m AuthModule) authorizeJWT(req *RequestContext) error {
	if m.jwtConfig.Secret == "" {
		return errors.New("jwt auth disabled")
	}

	claims, err := verifyJWT(req.APIKey, m.jwtConfig, time.Now())
	if err != nil {
		return err
	}

	req.UserID = claims.Subject
	req.Roles = normalizeRoles(claims)
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["auth.method"] = "jwt"
	req.Metadata["auth.issuer"] = claims.Issuer
	return nil
}

func verifyJWT(token string, cfg JWTAuthConfig, now time.Time) (jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, errors.New("invalid jwt format")
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwtClaims{}, err
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return jwtClaims{}, err
	}
	if header.Algorithm != "HS256" {
		return jwtClaims{}, errors.New("unsupported jwt alg")
	}

	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(cfg.Secret))
	_, _ = mac.Write([]byte(signingInput))
	expected := mac.Sum(nil)

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwtClaims{}, err
	}
	if !hmac.Equal(signature, expected) {
		return jwtClaims{}, errors.New("invalid jwt signature")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, err
	}
	var claims jwtClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return jwtClaims{}, err
	}
	if claims.Subject == "" {
		return jwtClaims{}, errors.New("missing sub")
	}
	if claims.ExpiresAt > 0 && now.Unix() >= claims.ExpiresAt {
		return jwtClaims{}, errors.New("jwt expired")
	}
	if claims.NotBefore > 0 && now.Unix() < claims.NotBefore {
		return jwtClaims{}, errors.New("jwt not active")
	}
	if cfg.Issuer != "" && claims.Issuer != cfg.Issuer {
		return jwtClaims{}, errors.New("invalid jwt issuer")
	}
	if cfg.Audience != "" && !claimHasAudience(claims.Audience, cfg.Audience) {
		return jwtClaims{}, errors.New("invalid jwt audience")
	}

	return claims, nil
}

func normalizeRoles(claims jwtClaims) []string {
	if len(claims.Roles) > 0 {
		return claims.Roles
	}
	if claims.Role != "" {
		return []string{claims.Role}
	}
	return []string{"user"}
}

func claimHasAudience(value any, expected string) bool {
	switch typed := value.(type) {
	case string:
		return typed == expected
	case []any:
		for _, item := range typed {
			if item == expected {
				return true
			}
		}
	}
	return false
}
