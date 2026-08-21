package modules

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

type AuthModule struct {
	required       bool
	jwtConfig      JWTAuthConfig
	virtualKeys    map[string]VirtualKey
	store          VirtualKeyStore
	keyHashSecret  string
	staticFallback bool
	demoKeys       bool
	initErr        error
}

func NewAuthModule(required bool) AuthModule {
	settings := SettingsFromEnv()
	module := AuthModule{
		required: required, jwtConfig: JWTAuthConfigFromEnv(), virtualKeys: VirtualKeysFromEnv(),
		keyHashSecret: settings.KeyHashSecret, staticFallback: settings.StaticKeyFallback,
		demoKeys: settings.DemoKeysEnabled,
	}
	if settings.PostgresKeysEnabled {
		if settings.KeyHashSecret == "" {
			module.initErr = errors.New("auth key hash secret is required")
			return module
		}
		module.store, module.initErr = NewPostgresVirtualKeyStore(settings.PostgresDSN)
	}
	return module
}

func NewAuthModuleWithJWT(required bool, cfg JWTAuthConfig) AuthModule {
	return AuthModule{required: required, jwtConfig: cfg, staticFallback: true, demoKeys: true}
}

type JWTAuthConfig struct {
	Secret   string
	Issuer   string
	Audience string
}

type VirtualKey struct {
	Token         string   `json:"token"`
	UserID        string   `json:"user_id"`
	TeamID        string   `json:"team_id,omitempty"`
	Roles         []string `json:"roles,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	AllowedTools  []string `json:"allowed_tools,omitempty"`
	RateLimitRPM  int      `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM  int      `json:"rate_limit_tpm,omitempty"`
}

func NewAuthModuleWithVirtualKeys(required bool, keys []VirtualKey) AuthModule {
	return AuthModule{required: required, jwtConfig: JWTAuthConfigFromEnv(), virtualKeys: indexVirtualKeys(keys), staticFallback: true, demoKeys: true}
}

func NewAuthModuleWithStore(required bool, store VirtualKeyStore, hashSecret string, staticFallback bool) AuthModule {
	return AuthModule{
		required: required, jwtConfig: JWTAuthConfigFromEnv(), virtualKeys: VirtualKeysFromEnv(),
		store: store, keyHashSecret: hashSecret, staticFallback: staticFallback,
	}
}

func VirtualKeysFromEnv() map[string]VirtualKey {
	var keys []VirtualKey
	if err := json.Unmarshal([]byte(os.Getenv("AUTH_VIRTUAL_KEYS_JSON")), &keys); err != nil {
		return nil
	}
	return indexVirtualKeys(keys)
}

func indexVirtualKeys(keys []VirtualKey) map[string]VirtualKey {
	indexed := make(map[string]VirtualKey, len(keys))
	for _, key := range keys {
		if key.Token == "" || key.UserID == "" {
			continue
		}
		fingerprint := credentialFingerprint(key.Token)
		key.Token = ""
		indexed[fingerprint] = key
	}
	return indexed
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

func (m AuthModule) Ready(ctx context.Context) error {
	if m.initErr != nil {
		return m.initErr
	}
	if m.store != nil {
		return m.store.Ready(ctx)
	}
	return nil
}

func (m AuthModule) Close() {
	if m.store != nil {
		m.store.Close()
	}
}

func (m AuthModule) Handle(ctx context.Context, req *RequestContext) error {
	if m.initErr != nil {
		return m.initErr
	}
	if strings.TrimSpace(req.APIKey) == "" {
		return ErrUnauthorized
	}
	if m.store != nil {
		key, found, err := m.store.Lookup(ctx, credentialLookupHash(req.APIKey, m.keyHashSecret))
		if err != nil {
			return err
		}
		if found {
			applyStoredVirtualKey(req, key)
			return nil
		}
	}
	if m.staticFallback {
		if key, ok := m.virtualKeys[credentialFingerprint(req.APIKey)]; ok {
			applyVirtualKey(req, key)
			return nil
		}
	}
	if m.demoKeys {
		switch req.APIKey {
		case "demo-admin-key":
			req.UserID = "demo-admin"
			req.Roles = []string{"admin", "developer"}
			req.CredentialID = credentialFingerprint(req.APIKey)
			req.APIKey = ""
			return nil
		case "demo-user-key":
			req.UserID = "demo-user"
			req.Roles = []string{"developer"}
			req.CredentialID = credentialFingerprint(req.APIKey)
			req.APIKey = ""
			return nil
		}
	}

	if err := m.authorizeJWT(req); err == nil {
		req.APIKey = ""
		return nil
	}

	return ErrUnauthorized
}

func applyVirtualKey(req *RequestContext, key VirtualKey) {
	req.UserID = key.UserID
	req.TeamID = key.TeamID
	req.Roles = append([]string(nil), key.Roles...)
	req.AllowedModels = append([]string(nil), key.AllowedModels...)
	req.AllowedTools = append([]string(nil), key.AllowedTools...)
	req.RateLimitRPM = key.RateLimitRPM
	req.RateLimitTPM = key.RateLimitTPM
	req.CredentialID = credentialFingerprint(req.APIKey)
	req.APIKey = ""
}

func applyStoredVirtualKey(req *RequestContext, key StoredVirtualKey) {
	req.UserID = key.UserID
	req.TeamID = key.TeamID
	req.Roles = append([]string(nil), key.Roles...)
	req.AllowedModels = append([]string(nil), key.AllowedModels...)
	req.AllowedTools = append([]string(nil), key.AllowedTools...)
	req.RateLimitRPM = key.RateLimitRPM
	req.RateLimitTPM = key.RateLimitTPM
	req.CredentialID = key.ID
	req.APIKey = ""
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
	req.CredentialID = credentialFingerprint(req.APIKey)
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["auth.method"] = "jwt"
	req.Metadata["auth.issuer"] = claims.Issuer
	return nil
}

func credentialFingerprint(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func credentialLookupHash(value, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
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
