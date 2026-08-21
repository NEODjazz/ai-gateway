package modules

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type virtualKeyManager interface {
	Create(ctx context.Context, key StoredVirtualKey, tokenHash string) error
	Revoke(ctx context.Context, id string) (bool, error)
	Rotate(ctx context.Context, oldID string, replacement StoredVirtualKey, tokenHash string) error
}

type ManagedVirtualKey struct {
	UserID        string     `json:"user_id"`
	TeamID        string     `json:"team_id,omitempty"`
	Roles         []string   `json:"roles,omitempty"`
	AllowedModels []string   `json:"allowed_models,omitempty"`
	RateLimitRPM  int        `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM  int        `json:"rate_limit_tpm,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

type IssuedVirtualKey struct {
	ID        string     `json:"id"`
	Token     string     `json:"token"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

var ErrVirtualKeyNotFound = errors.New("virtual key not found")
var ErrInvalidVirtualKey = errors.New("invalid virtual key policy")

func (m AuthModule) CreateVirtualKey(ctx context.Context, spec ManagedVirtualKey) (IssuedVirtualKey, error) {
	manager, err := m.keyManager()
	if err != nil {
		return IssuedVirtualKey{}, err
	}
	key, token, err := m.newStoredVirtualKey(spec, "")
	if err != nil {
		return IssuedVirtualKey{}, err
	}
	if err := manager.Create(ctx, key, credentialLookupHash(token, m.keyHashSecret)); err != nil {
		return IssuedVirtualKey{}, err
	}
	return IssuedVirtualKey{ID: key.ID, Token: token, ExpiresAt: key.ExpiresAt}, nil
}

func (m AuthModule) RotateVirtualKey(ctx context.Context, oldID string, spec ManagedVirtualKey) (IssuedVirtualKey, error) {
	manager, err := m.keyManager()
	if err != nil {
		return IssuedVirtualKey{}, err
	}
	if oldID == "" {
		return IssuedVirtualKey{}, fmt.Errorf("%w: id is required", ErrInvalidVirtualKey)
	}
	key, token, err := m.newStoredVirtualKey(spec, oldID)
	if err != nil {
		return IssuedVirtualKey{}, err
	}
	if err := manager.Rotate(ctx, oldID, key, credentialLookupHash(token, m.keyHashSecret)); err != nil {
		return IssuedVirtualKey{}, err
	}
	return IssuedVirtualKey{ID: key.ID, Token: token, ExpiresAt: key.ExpiresAt}, nil
}

func (m AuthModule) RevokeVirtualKey(ctx context.Context, id string) (bool, error) {
	manager, err := m.keyManager()
	if err != nil {
		return false, err
	}
	if id == "" {
		return false, fmt.Errorf("%w: id is required", ErrInvalidVirtualKey)
	}
	return manager.Revoke(ctx, id)
}

func (m AuthModule) keyManager() (virtualKeyManager, error) {
	if m.initErr != nil {
		return nil, m.initErr
	}
	if m.keyHashSecret == "" {
		return nil, errors.New("auth key hash secret is required")
	}
	manager, ok := m.store.(virtualKeyManager)
	if !ok || manager == nil {
		return nil, errors.New("persistent virtual key management is unavailable")
	}
	return manager, nil
}

func (m AuthModule) newStoredVirtualKey(spec ManagedVirtualKey, rotatedFrom string) (StoredVirtualKey, string, error) {
	spec.UserID = strings.TrimSpace(spec.UserID)
	spec.TeamID = strings.TrimSpace(spec.TeamID)
	if spec.UserID == "" || len(spec.UserID) > 256 || len(spec.TeamID) > 256 {
		return StoredVirtualKey{}, "", fmt.Errorf("%w: user_id is required", ErrInvalidVirtualKey)
	}
	if !validPolicyStrings(spec.Roles) || !validPolicyStrings(spec.AllowedModels) {
		return StoredVirtualKey{}, "", fmt.Errorf("%w: invalid roles or model grants", ErrInvalidVirtualKey)
	}
	if spec.RateLimitRPM < 0 || spec.RateLimitTPM < 0 {
		return StoredVirtualKey{}, "", fmt.Errorf("%w: rate limits cannot be negative", ErrInvalidVirtualKey)
	}
	if spec.ExpiresAt != nil && !spec.ExpiresAt.After(time.Now()) {
		return StoredVirtualKey{}, "", fmt.Errorf("%w: expires_at must be in the future", ErrInvalidVirtualKey)
	}
	idBytes := make([]byte, 16)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		return StoredVirtualKey{}, "", err
	}
	if _, err := rand.Read(tokenBytes); err != nil {
		return StoredVirtualKey{}, "", err
	}
	id := "vk_" + hex.EncodeToString(idBytes)
	token := "sk-ag-" + base64.RawURLEncoding.EncodeToString(tokenBytes)
	return StoredVirtualKey{
		ID: id, UserID: spec.UserID, TeamID: spec.TeamID,
		Roles: append([]string(nil), spec.Roles...), AllowedModels: append([]string(nil), spec.AllowedModels...),
		RateLimitRPM: spec.RateLimitRPM, RateLimitTPM: spec.RateLimitTPM,
		RotatedFromID: rotatedFrom, ExpiresAt: spec.ExpiresAt,
	}, token, nil
}

func validPolicyStrings(values []string) bool {
	if len(values) > 64 {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return false
		}
	}
	return true
}
