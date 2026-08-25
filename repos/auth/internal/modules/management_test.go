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

func TestCreateVirtualKeyReturnsSecretOnceAndPersistsOnlyHash(t *testing.T) {
	store := &managementStore{}
	module := NewAuthModuleWithStore(true, store, "hash-secret", false)
	expires := time.Now().Add(time.Hour).UTC()
	issued, err := module.CreateVirtualKey(context.Background(), ManagedVirtualKey{
		UserID: "user-1", TeamID: "team-1", Roles: []string{"developer"},
		AllowedModels: []string{"gpt-*"}, AllowedTools: []string{"mcp.weather.*"}, RateLimitRPM: 10, RateLimitTPM: 100, ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.ID, "vk_") || !strings.HasPrefix(issued.Token, "sk-ag-") {
		t.Fatalf("unexpected issued credential: %+v", issued)
	}
	if store.created.ID != issued.ID || store.created.UserID != "user-1" || store.created.TeamID != "team-1" || len(store.created.AllowedTools) != 1 {
		t.Fatalf("policy was not persisted: %+v", store.created)
	}
	if store.createdHash == "" || store.createdHash == issued.Token || store.createdHash != credentialLookupHash(issued.Token, "hash-secret") {
		t.Fatalf("plaintext or invalid token hash persisted: %q", store.createdHash)
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
		t.Fatal("missing user_id was accepted")
	}
	past := time.Now().Add(-time.Minute)
	if _, err := module.CreateVirtualKey(context.Background(), ManagedVirtualKey{UserID: "user", ExpiresAt: &past}); err == nil {
		t.Fatal("expired key was accepted")
	}
	if _, err := module.CreateVirtualKey(context.Background(), ManagedVirtualKey{UserID: "user", RateLimitRPM: -1}); err == nil {
		t.Fatal("negative rate limit was accepted")
	}
}

func TestListVirtualKeysReturnsSafeMetadata(t *testing.T) {
	module := NewAuthModuleWithStore(true, &managementStore{}, "hash-secret", false)
	keys, err := module.ListVirtualKeys(context.Background(), 100)
	if err != nil || len(keys) != 1 || keys[0].ID != "vk_safe123" {
		t.Fatalf("unexpected keys=%+v err=%v", keys, err)
	}
	if _, err := module.ListVirtualKeys(context.Background(), 0); !errors.Is(err, ErrInvalidVirtualKey) {
		t.Fatalf("invalid limit error=%v", err)
	}
}
