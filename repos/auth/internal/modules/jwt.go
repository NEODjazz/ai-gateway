package modules

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrJWTUnavailable = errors.New("jwt verification unavailable")

type JWTAuthConfig struct {
	Secret          string
	Issuer          string
	Audience        string
	JWKSURL         string
	JWKSCacheTTL    time.Duration
	ClockSkew       time.Duration
	UserIDClaim     string
	TeamIDClaim     string
	RolesClaim      string
	IdentityMode    string
	RoleMappings    map[string]string
	roleMappingsErr error
}

func JWTAuthConfigFromEnv() JWTAuthConfig {
	config := JWTAuthConfig{
		Secret:       os.Getenv("AUTH_JWT_SECRET"),
		Issuer:       strings.TrimSpace(os.Getenv("AUTH_JWT_ISSUER")),
		Audience:     strings.TrimSpace(os.Getenv("AUTH_JWT_AUDIENCE")),
		JWKSURL:      strings.TrimSpace(os.Getenv("AUTH_JWT_JWKS_URL")),
		JWKSCacheTTL: envDurationSeconds("AUTH_JWT_JWKS_CACHE_TTL_SECONDS", 5*time.Minute),
		ClockSkew:    envDurationSeconds("AUTH_JWT_CLOCK_SKEW_SECONDS", 30*time.Second),
		UserIDClaim:  envString("AUTH_JWT_USER_ID_CLAIM", "sub"),
		TeamIDClaim:  envString("AUTH_JWT_TEAM_ID_CLAIM", "team_id"),
		RolesClaim:   envString("AUTH_JWT_ROLES_CLAIM", "roles"),
		IdentityMode: envString("AUTH_JWT_IDENTITY_MODE", "legacy"),
	}
	if raw := strings.TrimSpace(os.Getenv("AUTH_JWT_ROLE_MAPPINGS_JSON")); raw != "" {
		config.roleMappingsErr = json.Unmarshal([]byte(raw), &config.RoleMappings)
	}
	return config
}

func (c JWTAuthConfig) validate() error {
	if c.roleMappingsErr != nil {
		return errors.New("invalid jwt role mappings")
	}
	if c.IdentityMode != "legacy" && c.IdentityMode != "directory" {
		return errors.New("jwt identity mode must be legacy or directory")
	}
	if c.IdentityMode == "directory" && (!validJWTIdentityValue(c.Issuer, 2048) || !validJWTIdentityValue(c.Audience, 256) || len(c.RoleMappings) == 0) {
		return errors.New("directory jwt requires issuer, audience and explicit role mappings")
	}
	if len(c.RoleMappings) > 128 {
		return errors.New("too many jwt role mappings")
	}
	for external, role := range c.RoleMappings {
		if !validJWTIdentityValue(external, 256) || (role != "user" && role != "developer" && role != "admin" && role != "team_admin" && role != "org_admin") {
			return errors.New("invalid jwt role mapping")
		}
	}
	if c.JWKSURL != "" && (c.Issuer == "" || c.Audience == "") {
		return errors.New("jwt issuer and audience are required with JWKS")
	}
	if c.JWKSURL != "" && c.UserIDClaim == "" {
		return errors.New("jwt user id claim is required")
	}
	return nil
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

type jwtClaims struct {
	Subject   string         `json:"sub"`
	Roles     []string       `json:"roles"`
	Role      string         `json:"role"`
	ExpiresAt int64          `json:"exp"`
	NotBefore int64          `json:"nbf"`
	Issuer    string         `json:"iss"`
	Audience  any            `json:"aud"`
	Raw       map[string]any `json:"-"`
}

type jwkSet struct {
	Keys []jsonWebKey `json:"keys"`
}

type jsonWebKey struct {
	KeyID     string `json:"kid"`
	KeyType   string `json:"kty"`
	Algorithm string `json:"alg"`
	Use       string `json:"use"`
	N         string `json:"n"`
	E         string `json:"e"`
	Curve     string `json:"crv"`
	X         string `json:"x"`
	Y         string `json:"y"`
}

type verificationKey struct {
	algorithm string
	key       any
}

type jwtVerifier struct {
	config             JWTAuthConfig
	client             *http.Client
	mu                 sync.Mutex
	keys               map[string]verificationKey
	fetchedAt          time.Time
	lastRefreshAttempt time.Time
	refresh            *jwtKeyRefresh
	now                func() time.Time
}

func newJWTVerifier(config JWTAuthConfig) (*jwtVerifier, error) {
	if config.IdentityMode == "" {
		config.IdentityMode = "legacy"
	}
	if config.RoleMappings != nil {
		copyMappings := make(map[string]string, len(config.RoleMappings))
		for external, role := range config.RoleMappings {
			copyMappings[external] = role
		}
		config.RoleMappings = copyMappings
	}
	if config.JWKSCacheTTL <= 0 {
		config.JWKSCacheTTL = 5 * time.Minute
	}
	if config.ClockSkew < 0 {
		return nil, errors.New("jwt clock skew must not be negative")
	}
	if config.UserIDClaim == "" {
		config.UserIDClaim = "sub"
	}
	if config.TeamIDClaim == "" {
		config.TeamIDClaim = "team_id"
	}
	if config.RolesClaim == "" {
		config.RolesClaim = "roles"
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &jwtVerifier{config: config, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, keys: map[string]verificationKey{}, now: time.Now}, nil
}

func (v *jwtVerifier) Ready(ctx context.Context) error {
	if v == nil || v.config.JWKSURL == "" {
		return nil
	}
	_, err := v.keysForVerification(ctx, false)
	return err
}

func (v *jwtVerifier) Verify(ctx context.Context, token string) (jwtClaims, error) {
	if v == nil || (v.config.Secret == "" && v.config.JWKSURL == "") {
		return jwtClaims{}, errors.New("jwt auth disabled")
	}
	if len(token) > 32*1024 {
		return jwtClaims{}, errors.New("jwt is too large")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, errors.New("invalid jwt format")
	}
	header, err := decodeJWTHeader(parts[0])
	if err != nil {
		return jwtClaims{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwtClaims{}, errors.New("invalid jwt signature encoding")
	}
	signingInput := []byte(parts[0] + "." + parts[1])
	if v.config.JWKSURL != "" {
		if header.Algorithm != "RS256" && header.Algorithm != "ES256" {
			return jwtClaims{}, errors.New("unsupported jwt alg")
		}
		if header.KeyID == "" {
			return jwtClaims{}, errors.New("missing jwt kid")
		}
		key, keyErr := v.key(ctx, header.KeyID, header.Algorithm)
		if keyErr != nil {
			return jwtClaims{}, keyErr
		}
		if err := verifyAsymmetricJWT(header.Algorithm, key, signingInput, signature); err != nil {
			return jwtClaims{}, err
		}
	} else {
		if header.Algorithm != "HS256" {
			return jwtClaims{}, errors.New("unsupported jwt alg")
		}
		mac := hmac.New(sha256.New, []byte(v.config.Secret))
		_, _ = mac.Write(signingInput)
		if !hmac.Equal(signature, mac.Sum(nil)) {
			return jwtClaims{}, errors.New("invalid jwt signature")
		}
	}
	claims, err := decodeJWTClaims(parts[1])
	if err != nil {
		return jwtClaims{}, err
	}
	claims.Subject = claimString(claims.Raw, v.config.UserIDClaim)
	if err := validateJWTClaims(claims, v.config, v.now()); err != nil {
		return jwtClaims{}, err
	}
	return claims, nil
}

func (v *jwtVerifier) key(ctx context.Context, keyID, algorithm string) (any, error) {
	keys, err := v.keysForVerification(ctx, false)
	if err != nil {
		return nil, err
	}
	if key, found := keys[keyID]; found && (key.algorithm == "" || key.algorithm == algorithm) {
		return key.key, nil
	}
	keys, err = v.keysForVerification(ctx, true)
	if err != nil {
		return nil, err
	}
	key, found := keys[keyID]
	if !found || (key.algorithm != "" && key.algorithm != algorithm) {
		return nil, errors.New("unknown jwt kid")
	}
	return key.key, nil
}

// A refresh result is immutable after done closes. Waiters share one bounded
// network operation and can cancel without acquiring a network-held mutex.
type jwtKeyRefresh struct {
	done chan struct{}
	keys map[string]verificationKey
	err  error
}

func (v *jwtVerifier) keysForVerification(ctx context.Context, force bool) (map[string]verificationKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrJWTUnavailable, err)
	}
	v.mu.Lock()
	now := v.now()
	if len(v.keys) > 0 && !force && now.Sub(v.fetchedAt) < v.config.JWKSCacheTTL {
		keys := cloneVerificationKeys(v.keys)
		v.mu.Unlock()
		return keys, nil
	}
	if flight := v.refresh; flight != nil {
		v.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %w", ErrJWTUnavailable, ctx.Err())
		case <-flight.done:
			return cloneVerificationKeys(flight.keys), flight.err
		}
	}
	if force && len(v.keys) > 0 && now.Sub(v.lastRefreshAttempt) < 5*time.Second {
		keys := cloneVerificationKeys(v.keys)
		v.mu.Unlock()
		return keys, nil
	}
	if force {
		v.lastRefreshAttempt = now
	}
	flight := &jwtKeyRefresh{done: make(chan struct{})}
	v.refresh = flight
	v.mu.Unlock()
	keys, err := v.fetchKeys(ctx)
	v.mu.Lock()
	if err != nil {
		if !force && len(v.keys) > 0 && now.Sub(v.fetchedAt) < v.config.JWKSCacheTTL {
			flight.keys = cloneVerificationKeys(v.keys)
		} else {
			flight.err = fmt.Errorf("%w: %w", ErrJWTUnavailable, err)
		}
	} else {
		v.keys, v.fetchedAt = keys, now
		flight.keys = keys
	}
	v.refresh = nil
	close(flight.done)
	result := cloneVerificationKeys(flight.keys)
	v.mu.Unlock()
	return result, flight.err
}

func (v *jwtVerifier) fetchKeys(ctx context.Context) (map[string]verificationKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.config.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := v.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("JWKS returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 1<<20 {
		return nil, errors.New("JWKS response is too large")
	}
	var set jwkSet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, err
	}
	if len(set.Keys) == 0 || len(set.Keys) > 64 {
		return nil, errors.New("JWKS must contain between 1 and 64 keys")
	}
	keys := make(map[string]verificationKey, len(set.Keys))
	for _, jwk := range set.Keys {
		if jwk.KeyID == "" || (jwk.Use != "" && jwk.Use != "sig") {
			continue
		}
		key, parseErr := parseJWK(jwk)
		if parseErr != nil {
			continue
		}
		if _, duplicate := keys[jwk.KeyID]; duplicate {
			return nil, errors.New("JWKS contains duplicate kid")
		}
		keys[jwk.KeyID] = verificationKey{algorithm: jwk.Algorithm, key: key}
	}
	if len(keys) == 0 {
		return nil, errors.New("JWKS contains no supported signing keys")
	}
	return keys, nil
}

func parseJWK(jwk jsonWebKey) (any, error) {
	switch jwk.KeyType {
	case "RSA":
		n, err := decodeBigInt(jwk.N)
		if err != nil || n.Sign() <= 0 || n.BitLen() < 2048 {
			return nil, errors.New("invalid RSA modulus")
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
		if err != nil || len(eBytes) == 0 || len(eBytes) > 4 {
			return nil, errors.New("invalid RSA exponent")
		}
		e := 0
		for _, value := range eBytes {
			e = e<<8 | int(value)
		}
		if e < 3 || e%2 == 0 {
			return nil, errors.New("invalid RSA exponent")
		}
		return &rsa.PublicKey{N: n, E: e}, nil
	case "EC":
		if jwk.Curve != "P-256" {
			return nil, errors.New("unsupported EC curve")
		}
		x, err := decodeBigInt(jwk.X)
		if err != nil {
			return nil, err
		}
		y, err := decodeBigInt(jwk.Y)
		if err != nil || !elliptic.P256().IsOnCurve(x, y) {
			return nil, errors.New("invalid EC point")
		}
		return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
	default:
		return nil, errors.New("unsupported JWK key type")
	}
}

func verifyAsymmetricJWT(algorithm string, key any, input, signature []byte) error {
	digest := sha256.Sum256(input)
	switch algorithm {
	case "RS256":
		publicKey, ok := key.(*rsa.PublicKey)
		if !ok || rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature) != nil {
			return errors.New("invalid jwt signature")
		}
	case "ES256":
		publicKey, ok := key.(*ecdsa.PublicKey)
		if !ok || len(signature) != 64 {
			return errors.New("invalid jwt signature")
		}
		r := new(big.Int).SetBytes(signature[:32])
		s := new(big.Int).SetBytes(signature[32:])
		if !ecdsa.Verify(publicKey, digest[:], r, s) {
			return errors.New("invalid jwt signature")
		}
	default:
		return errors.New("unsupported jwt alg")
	}
	return nil
}

func decodeJWTHeader(encoded string) (jwtHeader, error) {
	value, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return jwtHeader{}, errors.New("invalid jwt header encoding")
	}
	var header jwtHeader
	if err := json.Unmarshal(value, &header); err != nil {
		return jwtHeader{}, errors.New("invalid jwt header")
	}
	return header, nil
}
func decodeJWTClaims(encoded string) (jwtClaims, error) {
	value, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return jwtClaims{}, errors.New("invalid jwt claims encoding")
	}
	var raw map[string]any
	if err := json.Unmarshal(value, &raw); err != nil {
		return jwtClaims{}, errors.New("invalid jwt claims")
	}
	var claims jwtClaims
	if err := json.Unmarshal(value, &claims); err != nil {
		return jwtClaims{}, errors.New("invalid jwt claims")
	}
	claims.Raw = raw
	return claims, nil
}

func validateJWTClaims(claims jwtClaims, config JWTAuthConfig, now time.Time) error {
	if claims.Subject == "" {
		return errors.New("missing user identity claim")
	}
	if config.JWKSURL != "" && claims.ExpiresAt == 0 {
		return errors.New("missing jwt exp")
	}
	if claims.ExpiresAt > 0 && now.Add(-config.ClockSkew).Unix() >= claims.ExpiresAt {
		return errors.New("jwt expired")
	}
	if claims.NotBefore > 0 && now.Add(config.ClockSkew).Unix() < claims.NotBefore {
		return errors.New("jwt not active")
	}
	if config.Issuer != "" && claims.Issuer != config.Issuer {
		return errors.New("invalid jwt issuer")
	}
	if config.Audience != "" && !claimHasAudience(claims.Audience, config.Audience) {
		return errors.New("invalid jwt audience")
	}
	return nil
}

func normalizeRoles(claims jwtClaims, claimName string) []string {
	if roles := claimStrings(claims.Raw, claimName); len(roles) > 0 {
		return roles
	}
	if len(claims.Roles) > 0 {
		return append([]string(nil), claims.Roles...)
	}
	if claims.Role != "" {
		return []string{claims.Role}
	}
	return []string{"user"}
}
func claimValue(claims map[string]any, path string) any {
	var current any = claims
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[part]
	}
	return current
}
func claimString(claims map[string]any, path string) string {
	value, _ := claimValue(claims, path).(string)
	return strings.TrimSpace(value)
}
func claimStrings(claims map[string]any, path string) []string {
	switch value := claimValue(claims, path).(type) {
	case string:
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return []string{trimmed}
		}
	case []any:
		result := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, strings.TrimSpace(text))
			}
		}
		return result
	}
	return nil
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
func decodeBigInt(value string) (*big.Int, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 {
		return nil, errors.New("invalid JWK integer")
	}
	return new(big.Int).SetBytes(decoded), nil
}
func cloneVerificationKeys(source map[string]verificationKey) map[string]verificationKey {
	result := make(map[string]verificationKey, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
func envDurationSeconds(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
func envString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
