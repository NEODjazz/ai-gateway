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
	Alias          string     `json:"alias,omitempty"`
	Description    string     `json:"description,omitempty"`
	Tags           []string   `json:"tags,omitempty"`
	UserID         string     `json:"user_id,omitempty"`
	TeamID         string     `json:"team_id,omitempty"`
	OrganizationID string     `json:"organization_id,omitempty"`
	Roles          []string   `json:"roles,omitempty"`
	AccessGroupIDs []string   `json:"access_group_ids,omitempty"`
	AllowedModels  []string   `json:"allowed_models,omitempty"`
	AllowedTools   []string   `json:"allowed_tools,omitempty"`
	RateLimitRPM   int        `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM   int        `json:"rate_limit_tpm,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

type IssuedVirtualKey struct {
	ID        string     `json:"id"`
	Token     string     `json:"token"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// VirtualKeyMetadata is the safe management projection of a stored key. It
// deliberately has no plaintext token or token-hash field.
type VirtualKeyMetadata struct {
	ID             string     `json:"id"`
	Alias          string     `json:"alias,omitempty"`
	Description    string     `json:"description,omitempty"`
	Tags           []string   `json:"tags,omitempty"`
	UserID         string     `json:"user_id,omitempty"`
	TeamID         string     `json:"team_id,omitempty"`
	OrganizationID string     `json:"organization_id,omitempty"`
	Roles          []string   `json:"roles,omitempty"`
	AccessGroupIDs []string   `json:"access_group_ids,omitempty"`
	AllowedModels  []string   `json:"allowed_models,omitempty"`
	AllowedTools   []string   `json:"allowed_tools,omitempty"`
	RateLimitRPM   int        `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM   int        `json:"rate_limit_tpm,omitempty"`
	RotationFamily string     `json:"rotation_family_id"`
	RotatedFromID  string     `json:"rotated_from_id,omitempty"`
	RotatedToID    string     `json:"rotated_to_id,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	DisabledAt     *time.Time `json:"disabled_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type VirtualKeyListQuery struct {
	Limit          int
	Offset         int
	Search         string
	OrganizationID string
	TeamID         string
	UserID         string
	KeyID          string
	Status         string
	SortBy         string
	SortOrder      string
}

type VirtualKeyPage struct {
	Data   []VirtualKeyMetadata `json:"data"`
	Total  int                  `json:"total"`
	Limit  int                  `json:"limit"`
	Offset int                  `json:"offset"`
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

func (m AuthModule) UpdateVirtualKey(ctx context.Context, id string, spec ManagedVirtualKey) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("%w: id is required", ErrInvalidVirtualKey)
	}
	key, err := validateStoredVirtualKey(spec)
	if err != nil {
		return false, err
	}
	manager, ok := m.store.(interface {
		Update(context.Context, string, StoredVirtualKey) (bool, error)
	})
	if !ok || manager == nil {
		return false, errors.New("persistent virtual key updates are unavailable")
	}
	return manager.Update(ctx, id, key)
}

func (m AuthModule) SetVirtualKeyDisabled(ctx context.Context, id string, disabled bool) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("%w: id is required", ErrInvalidVirtualKey)
	}
	manager, ok := m.store.(interface {
		SetDisabled(context.Context, string, bool) (bool, error)
	})
	if !ok || manager == nil {
		return false, errors.New("persistent virtual key status updates are unavailable")
	}
	return manager.SetDisabled(ctx, id, disabled)
}

func (m AuthModule) ListVirtualKeys(ctx context.Context, limit int) ([]VirtualKeyMetadata, error) {
	page, err := m.ListVirtualKeysPage(ctx, VirtualKeyListQuery{Limit: limit})
	return page.Data, err
}

func (m AuthModule) ListVirtualKeysPage(ctx context.Context, query VirtualKeyListQuery) (VirtualKeyPage, error) {
	if m.initErr != nil {
		return VirtualKeyPage{}, m.initErr
	}
	query.Search = strings.TrimSpace(query.Search)
	query.OrganizationID = strings.TrimSpace(query.OrganizationID)
	query.TeamID = strings.TrimSpace(query.TeamID)
	query.UserID = strings.TrimSpace(query.UserID)
	query.KeyID = strings.TrimSpace(query.KeyID)
	query.Status = strings.TrimSpace(query.Status)
	query.SortBy = strings.TrimSpace(query.SortBy)
	query.SortOrder = strings.TrimSpace(query.SortOrder)
	if query.SortBy == "" {
		query.SortBy = "created"
	}
	if query.SortOrder == "" {
		query.SortOrder = "desc"
	}
	if query.Limit <= 0 || query.Limit > 500 || query.Offset < 0 || query.Offset > 1_000_000 || len(query.Search) > 128 || len(query.OrganizationID) > 256 || len(query.TeamID) > 256 || len(query.UserID) > 256 || len(query.KeyID) > 256 || !oneOf(query.Status, "", "active", "disabled", "revoked", "expired") || !oneOf(query.SortBy, "key", "alias", "organization", "team", "user", "created", "status") || !oneOf(query.SortOrder, "asc", "desc") {
		return VirtualKeyPage{}, fmt.Errorf("%w: invalid virtual key list query", ErrInvalidVirtualKey)
	}
	if pager, ok := m.store.(interface {
		ListPage(context.Context, VirtualKeyListQuery) (VirtualKeyPage, error)
	}); ok && pager != nil {
		return pager.ListPage(ctx, query)
	}
	lister, ok := m.store.(interface {
		List(context.Context, int) ([]VirtualKeyMetadata, error)
	})
	if !ok || lister == nil || query.Offset != 0 || query.Search != "" || query.OrganizationID != "" || query.TeamID != "" || query.UserID != "" || query.KeyID != "" || query.Status != "" || query.SortBy != "created" || query.SortOrder != "desc" {
		return VirtualKeyPage{}, errors.New("persistent virtual key paginated listing is unavailable")
	}
	data, err := lister.List(ctx, query.Limit)
	return VirtualKeyPage{Data: data, Total: len(data), Limit: query.Limit, Offset: query.Offset}, err
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
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
	key, err := validateStoredVirtualKey(spec)
	if err != nil {
		return StoredVirtualKey{}, "", err
	}
	idBytes := make([]byte, 16)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		return StoredVirtualKey{}, "", err
	}
	if _, err := rand.Read(tokenBytes); err != nil {
		return StoredVirtualKey{}, "", err
	}
	key.ID = "vk_" + hex.EncodeToString(idBytes)
	key.RotatedFromID = rotatedFrom
	return key, "sk-ag-" + base64.RawURLEncoding.EncodeToString(tokenBytes), nil
}

func validateStoredVirtualKey(spec ManagedVirtualKey) (StoredVirtualKey, error) {
	spec.UserID = strings.TrimSpace(spec.UserID)
	spec.TeamID = strings.TrimSpace(spec.TeamID)
	spec.OrganizationID = strings.TrimSpace(spec.OrganizationID)
	spec.Alias = strings.TrimSpace(spec.Alias)
	spec.Description = strings.TrimSpace(spec.Description)
	if (spec.UserID == "" && spec.TeamID == "" && spec.OrganizationID == "") || len(spec.UserID) > 256 || len(spec.TeamID) > 256 || len(spec.OrganizationID) > 256 {
		return StoredVirtualKey{}, fmt.Errorf("%w: organization_id, team_id, or user_id is required", ErrInvalidVirtualKey)
	}
	if len(spec.Alias) > 128 || len(spec.Description) > 1024 || !validPolicyStrings(spec.Tags) || !validPolicyStrings(spec.Roles) || !validAccessGroupIDs(spec.AccessGroupIDs) || !validPolicyStrings(spec.AllowedModels) || !validPolicyStrings(spec.AllowedTools) {
		return StoredVirtualKey{}, fmt.Errorf("%w: invalid metadata or grants", ErrInvalidVirtualKey)
	}
	if spec.RateLimitRPM < 0 || spec.RateLimitTPM < 0 {
		return StoredVirtualKey{}, fmt.Errorf("%w: rate limits cannot be negative", ErrInvalidVirtualKey)
	}
	if spec.ExpiresAt != nil && !spec.ExpiresAt.After(time.Now()) {
		return StoredVirtualKey{}, fmt.Errorf("%w: expires_at must be in the future", ErrInvalidVirtualKey)
	}
	spec.Tags = normalizePolicyStrings(spec.Tags)
	spec.AccessGroupIDs = normalizePolicyStrings(spec.AccessGroupIDs)
	return StoredVirtualKey{
		Alias: spec.Alias, Description: spec.Description, Tags: append([]string(nil), spec.Tags...), UserID: spec.UserID, TeamID: spec.TeamID, OrganizationID: spec.OrganizationID,
		Roles: append([]string(nil), spec.Roles...), AccessGroupIDs: append([]string(nil), spec.AccessGroupIDs...), AllowedModels: append([]string(nil), spec.AllowedModels...),
		AllowedTools: append([]string(nil), spec.AllowedTools...),
		RateLimitRPM: spec.RateLimitRPM, RateLimitTPM: spec.RateLimitTPM,
		ExpiresAt: spec.ExpiresAt,
	}, nil
}

func normalizePolicyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
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

func validAccessGroupIDs(values []string) bool {
	if len(values) > 64 {
		return false
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 {
			return false
		}
		for _, char := range value {
			if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '-' && char != '_' && char != '.' {
				return false
			}
		}
	}
	return true
}
