package modules

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

type AuthModule struct {
	required       bool
	jwtConfig      JWTAuthConfig
	jwtVerifier    *jwtVerifier
	virtualKeys    map[string]VirtualKey
	store          VirtualKeyStore
	keyHashSecret  string
	staticFallback bool
	demoKeys       bool
	initErr        error
}

func NewAuthModule(required bool) AuthModule {
	settings := SettingsFromEnv()
	jwtConfig := JWTAuthConfigFromEnv()
	verifier, jwtErr := newJWTVerifier(jwtConfig)
	module := AuthModule{
		required: required, jwtConfig: jwtConfig, jwtVerifier: verifier, virtualKeys: VirtualKeysFromEnv(),
		keyHashSecret: settings.KeyHashSecret, staticFallback: settings.StaticKeyFallback,
		demoKeys: settings.DemoKeysEnabled, initErr: jwtErr,
	}
	if settings.PostgresKeysEnabled {
		if settings.KeyHashSecret == "" {
			module.initErr = errors.New("auth key hash secret is required")
			return module
		}
		store, storeErr := NewPostgresVirtualKeyStore(settings.PostgresDSN)
		module.store = store
		module.initErr = errors.Join(module.initErr, storeErr)
	}
	return module
}

func NewAuthModuleWithJWT(required bool, cfg JWTAuthConfig) AuthModule {
	verifier, err := newJWTVerifier(cfg)
	if verifier != nil {
		cfg = verifier.config
	}
	return AuthModule{required: required, jwtConfig: cfg, jwtVerifier: verifier, staticFallback: true, demoKeys: true, initErr: err}
}

type VirtualKey struct {
	Token          string   `json:"token"`
	UserID         string   `json:"user_id,omitempty"`
	TeamID         string   `json:"team_id,omitempty"`
	OrganizationID string   `json:"organization_id,omitempty"`
	Roles          []string `json:"roles,omitempty"`
	AccessGroupIDs []string `json:"access_group_ids,omitempty"`
	AllowedModels  []string `json:"allowed_models,omitempty"`
	AllowedTools   []string `json:"allowed_tools,omitempty"`
	RateLimitRPM   int      `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM   int      `json:"rate_limit_tpm,omitempty"`
}

func NewAuthModuleWithVirtualKeys(required bool, keys []VirtualKey) AuthModule {
	cfg := JWTAuthConfigFromEnv()
	verifier, err := newJWTVerifier(cfg)
	return AuthModule{required: required, jwtConfig: cfg, jwtVerifier: verifier, virtualKeys: indexVirtualKeys(keys), staticFallback: true, demoKeys: true, initErr: err}
}

func NewAuthModuleWithStore(required bool, store VirtualKeyStore, hashSecret string, staticFallback bool) AuthModule {
	cfg := JWTAuthConfigFromEnv()
	verifier, err := newJWTVerifier(cfg)
	return AuthModule{
		required: required, jwtConfig: cfg, jwtVerifier: verifier, virtualKeys: VirtualKeysFromEnv(),
		store: store, keyHashSecret: hashSecret, staticFallback: staticFallback,
		initErr: err,
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
		if key.Token == "" || (key.UserID == "" && key.TeamID == "" && key.OrganizationID == "") {
			continue
		}
		fingerprint := credentialFingerprint(key.Token)
		key.Token = ""
		indexed[fingerprint] = key
	}
	return indexed
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
		if err := m.store.Ready(ctx); err != nil {
			return err
		}
	}
	return m.jwtVerifier.Ready(ctx)
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

	if err := m.authorizeJWT(ctx, req); err == nil {
		req.APIKey = ""
		return nil
	} else if errors.Is(err, ErrJWTUnavailable) {
		return err
	}

	return ErrUnauthorized
}

func applyVirtualKey(req *RequestContext, key VirtualKey) {
	req.UserID = key.UserID
	if req.UserID == "" {
		req.UserID = "virtual-key:" + credentialFingerprint(req.APIKey)
	}
	req.TeamID = key.TeamID
	req.OrganizationID = key.OrganizationID
	req.Roles = append([]string(nil), key.Roles...)
	req.AccessGroupIDs = append([]string(nil), key.AccessGroupIDs...)
	req.AllowedModels = append([]string(nil), key.AllowedModels...)
	req.AllowedTools = append([]string(nil), key.AllowedTools...)
	req.RateLimitRPM = key.RateLimitRPM
	req.RateLimitTPM = key.RateLimitTPM
	req.CredentialID = credentialFingerprint(req.APIKey)
	req.APIKey = ""
}

func applyStoredVirtualKey(req *RequestContext, key StoredVirtualKey) {
	req.UserID = key.UserID
	if req.UserID == "" {
		req.UserID = "virtual-key:" + key.ID
	}
	req.TeamID = key.TeamID
	req.OrganizationID = key.OrganizationID
	req.Roles = append([]string(nil), key.Roles...)
	req.AccessGroupIDs = append([]string(nil), key.AccessGroupIDs...)
	req.AllowedModels = append([]string(nil), key.AllowedModels...)
	req.AllowedTools = append([]string(nil), key.AllowedTools...)
	req.RateLimitRPM = key.RateLimitRPM
	req.RateLimitTPM = key.RateLimitTPM
	req.CredentialID = key.ID
	req.CredentialAlias = key.Alias
	req.Tags = append([]string(nil), key.Tags...)
	req.APIKey = ""
}

func (m AuthModule) authorizeJWT(ctx context.Context, req *RequestContext) error {
	claims, err := m.jwtVerifier.Verify(ctx, req.APIKey)
	if err != nil {
		return err
	}

	req.UserID = claims.Subject
	req.TeamID = claimString(claims.Raw, m.jwtConfig.TeamIDClaim)
	req.Roles = normalizeRoles(claims, m.jwtConfig.RolesClaim)
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
