package modules

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type managementStore struct {
	created     StoredVirtualKey
	createdHash string
	rotatedOld  string
	revokedID   string
	updatedID   string
	disabledID  string
	disabled    bool
	listQuery   VirtualKeyListQuery
}

func (s *managementStore) Lookup(context.Context, string) (StoredVirtualKey, bool, error) {
	return StoredVirtualKey{}, false, nil
}
func (s *managementStore) Ready(context.Context) error { return nil }
func (s *managementStore) Close()                      {}
func (s *managementStore) Create(_ context.Context, key StoredVirtualKey, tokenHash string) error {
	s.created, s.createdHash = key, tokenHash
	return nil
}
func (s *managementStore) Revoke(_ context.Context, id string) (bool, error) {
	s.revokedID = id
	return true, nil
}
func (s *managementStore) Rotate(_ context.Context, oldID string, replacement StoredVirtualKey, tokenHash string) error {
	s.rotatedOld, s.created, s.createdHash = oldID, replacement, tokenHash
	return nil
}
func (s *managementStore) List(context.Context, int) ([]VirtualKeyMetadata, error) {
	return []VirtualKeyMetadata{{ID: "vk_safe123", UserID: "user-1", RotationFamily: "vk_safe123"}}, nil
}
func (s *managementStore) ListPage(_ context.Context, query VirtualKeyListQuery) (VirtualKeyPage, error) {
	s.listQuery = query
	return VirtualKeyPage{Data: []VirtualKeyMetadata{{ID: "vk_safe123", UserID: "user-1", RotationFamily: "vk_safe123"}}, Total: 17, Limit: query.Limit, Offset: query.Offset}, nil
}
func (s *managementStore) Update(_ context.Context, id string, key StoredVirtualKey) (bool, error) {
	s.updatedID, s.created = id, key
	return true, nil
}
func (s *managementStore) SetDisabled(_ context.Context, id string, disabled bool) (bool, error) {
	s.disabledID, s.disabled = id, disabled
	return true, nil
}

func TestCreateVirtualKeyReturnsSecretOnceAndPersistsOnlyHash(t *testing.T) {
	store := &managementStore{}
	module := NewAuthModuleWithStore(true, store, "hash-secret", false)
	expires := time.Now().Add(time.Hour).UTC()
	issued, err := module.CreateVirtualKey(context.Background(), ManagedVirtualKey{
		UserID: "user-1", TeamID: "team-1", Roles: []string{"developer"}, Tags: []string{" production ", "production", "cost-center-a"},
		AccessGroupIDs: []string{" platform ", "platform", "regulated"}, AllowedModels: []string{"gpt-*"}, AllowedTools: []string{"mcp.weather.*"}, RateLimitRPM: 10, RateLimitTPM: 100, ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.ID, "vk_") || !strings.HasPrefix(issued.Token, "sk-ag-") {
		t.Fatalf("unexpected issued credential: %+v", issued)
	}
	if store.created.ID != issued.ID || store.created.UserID != "user-1" || store.created.TeamID != "team-1" || len(store.created.AllowedTools) != 1 || len(store.created.Tags) != 2 || store.created.Tags[0] != "production" || len(store.created.AccessGroupIDs) != 2 || store.created.AccessGroupIDs[0] != "platform" {
		t.Fatalf("policy was not persisted: %+v", store.created)
	}
	if store.createdHash == "" || store.createdHash == issued.Token || store.createdHash != credentialLookupHash(issued.Token, "hash-secret") {
		t.Fatalf("plaintext or invalid token hash persisted: %q", store.createdHash)
	}
}

func TestUpdateDisableAndEnableVirtualKey(t *testing.T) {
	store := &managementStore{}
	module := NewAuthModuleWithStore(true, store, "hash-secret", false)
	updated, err := module.UpdateVirtualKey(context.Background(), "vk_existing", ManagedVirtualKey{Alias: "production", Description: "owner: platform", Tags: []string{"prod"}, UserID: "user-1", RateLimitRPM: 20})
	if err != nil || !updated || store.updatedID != "vk_existing" || store.created.Alias != "production" || len(store.created.Tags) != 1 {
		t.Fatalf("update failed: updated=%v store=%+v err=%v", updated, store, err)
	}
	if disabled, err := module.SetVirtualKeyDisabled(context.Background(), "vk_existing", true); err != nil || !disabled || store.disabledID != "vk_existing" || !store.disabled {
		t.Fatalf("disable failed: disabled=%v store=%+v err=%v", disabled, store, err)
	}
	if enabled, err := module.SetVirtualKeyDisabled(context.Background(), "vk_existing", false); err != nil || !enabled || store.disabled {
		t.Fatalf("enable failed: enabled=%v store=%+v err=%v", enabled, store, err)
	}
}

func TestRotateAndRevokeVirtualKeyUsePersistentManager(t *testing.T) {
	store := &managementStore{}
	module := NewAuthModuleWithStore(true, store, "hash-secret", false)
	issued, err := module.RotateVirtualKey(context.Background(), "vk_old", ManagedVirtualKey{UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if store.rotatedOld != "vk_old" || store.created.RotatedFromID != "vk_old" || issued.Token == "" {
		t.Fatalf("rotation was not atomic: old=%q key=%+v", store.rotatedOld, store.created)
	}
	if revoked, err := module.RevokeVirtualKey(context.Background(), issued.ID); err != nil || !revoked || store.revokedID != issued.ID {
		t.Fatalf("revoke failed: revoked=%v id=%q err=%v", revoked, store.revokedID, err)
	}
}

func TestManagedVirtualKeyValidation(t *testing.T) {
	module := NewAuthModuleWithStore(true, &managementStore{}, "hash-secret", false)
	if _, err := module.CreateVirtualKey(context.Background(), ManagedVirtualKey{}); err == nil {
		t.Fatal("missing owner was accepted")
	}
	if _, err := module.CreateVirtualKey(context.Background(), ManagedVirtualKey{TeamID: "team-1"}); err != nil {
		t.Fatalf("team-owned key was rejected: %v", err)
	}
	if _, err := module.CreateVirtualKey(context.Background(), ManagedVirtualKey{OrganizationID: "org-1"}); err != nil {
		t.Fatalf("organization-owned key was rejected: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	if _, err := module.CreateVirtualKey(context.Background(), ManagedVirtualKey{UserID: "user", ExpiresAt: &past}); err == nil {
		t.Fatal("expired key was accepted")
	}
	if _, err := module.CreateVirtualKey(context.Background(), ManagedVirtualKey{UserID: "user", RateLimitRPM: -1}); err == nil {
		t.Fatal("negative rate limit was accepted")
	}
	if _, err := module.CreateVirtualKey(context.Background(), ManagedVirtualKey{UserID: "user", AccessGroupIDs: []string{""}}); err == nil {
		t.Fatal("empty access-group id was accepted")
	}
}

func TestListVirtualKeysReturnsSafeMetadata(t *testing.T) {
	store := &managementStore{}
	module := NewAuthModuleWithStore(true, store, "hash-secret", false)
	keys, err := module.ListVirtualKeys(context.Background(), 100)
	if err != nil || len(keys) != 1 || keys[0].ID != "vk_safe123" {
		t.Fatalf("unexpected keys=%+v err=%v", keys, err)
	}
	if _, err := module.ListVirtualKeys(context.Background(), 0); !errors.Is(err, ErrInvalidVirtualKey) {
		t.Fatalf("invalid limit error=%v", err)
	}
	page, err := module.ListVirtualKeysPage(context.Background(), VirtualKeyListQuery{Limit: 25, Offset: 50, Search: " prod ", OrganizationID: "org-1", TeamID: "team-1", UserID: "user-1", KeyID: "safe", AccessGroupIDs: []string{" platform ", "regulated", "platform"}, Status: "non_revoked", SortBy: "alias", SortOrder: "asc"})
	if err != nil || page.Total != 17 || store.listQuery.Search != "prod" || store.listQuery.Offset != 50 || store.listQuery.OrganizationID != "org-1" || len(store.listQuery.AccessGroupIDs) != 2 || store.listQuery.AccessGroupIDs[0] != "platform" || store.listQuery.AccessGroupIDs[1] != "regulated" || store.listQuery.Status != "non_revoked" {
		t.Fatalf("unexpected page=%+v query=%+v err=%v", page, store.listQuery, err)
	}
	for _, invalid := range []VirtualKeyListQuery{{Limit: 25, Offset: -1}, {Limit: 25, Status: "unknown"}, {Limit: 25, AccessGroupID: "bad/group"}, {Limit: 25, AccessGroupIDs: []string{"good", "bad/group"}}, {Limit: 25, SortBy: "token_hash"}, {Limit: 25, SortOrder: "sideways"}} {
		if _, err := module.ListVirtualKeysPage(context.Background(), invalid); !errors.Is(err, ErrInvalidVirtualKey) {
			t.Fatalf("invalid query was accepted: %+v err=%v", invalid, err)
		}
	}
}
