package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresVirtualKeyLifecycleIntegration(t *testing.T) {
	dsn := os.Getenv("AUTH_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("AUTH_POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, name := range []string{"003_virtual_keys.sql", "004_allowed_tools.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}

	store, err := NewPostgresVirtualKeyStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	oldID, newID, expiredID := "key-old-"+suffix, "key-new-"+suffix, "key-expired-"+suffix
	t.Cleanup(func() {
		ids := []string{newID, oldID, expiredID}
		_, _ = pool.Exec(context.Background(), `UPDATE auth_virtual_keys SET rotated_from_id=NULL,rotated_to_id=NULL WHERE id = ANY($1)`, ids)
		_, _ = pool.Exec(context.Background(), `DELETE FROM auth_virtual_keys WHERE id = ANY($1)`, ids)
	})

	oldToken := "old-token-" + suffix
	old := StoredVirtualKey{ID: oldID, UserID: "user-1", TeamID: "team-1", Roles: []string{"developer"}, AllowedModels: []string{"gpt-*"}, AllowedTools: []string{"mcp.weather.*"}, RateLimitRPM: 10}
	if err := store.Create(ctx, old, credentialLookupHash(oldToken, "pepper")); err != nil {
		t.Fatal(err)
	}
	if found, ok, err := store.Lookup(ctx, credentialLookupHash(oldToken, "pepper")); err != nil || !ok || found.ID != oldID || len(found.AllowedTools) != 1 {
		t.Fatalf("active key lookup failed: key=%+v ok=%v err=%v", found, ok, err)
	}

	expiredAt := time.Now().Add(-time.Minute)
	expiredToken := "expired-token-" + suffix
	if err := store.Create(ctx, StoredVirtualKey{ID: expiredID, UserID: "user-1", ExpiresAt: &expiredAt}, credentialLookupHash(expiredToken, "pepper")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Lookup(ctx, credentialLookupHash(expiredToken, "pepper")); err != nil || ok {
		t.Fatalf("expired key must not authorize: ok=%v err=%v", ok, err)
	}

	newToken := "new-token-" + suffix
	replacement := StoredVirtualKey{ID: newID, UserID: "user-1", TeamID: "team-1", Roles: []string{"developer"}, RateLimitRPM: 20}
	if err := store.Rotate(ctx, oldID, replacement, credentialLookupHash(newToken, "pepper")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Lookup(ctx, credentialLookupHash(oldToken, "pepper")); err != nil || ok {
		t.Fatalf("rotated key must be revoked: ok=%v err=%v", ok, err)
	}
	rotated, ok, err := store.Lookup(ctx, credentialLookupHash(newToken, "pepper"))
	if err != nil || !ok || rotated.ID != newID || rotated.RotatedFromID != oldID || rotated.RotationFamily != oldID {
		t.Fatalf("replacement key lookup failed: key=%+v ok=%v err=%v", rotated, ok, err)
	}
	if revoked, err := store.Revoke(ctx, newID); err != nil || !revoked {
		t.Fatalf("revoke failed: revoked=%v err=%v", revoked, err)
	}
	if _, ok, err := store.Lookup(ctx, credentialLookupHash(newToken, "pepper")); err != nil || ok {
		t.Fatalf("revoked key must not authorize: ok=%v err=%v", ok, err)
	}
	listed, err := store.List(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundMetadata := false
	for _, key := range listed {
		if key.ID == newID {
			foundMetadata = key.UserID == "user-1" && key.RevokedAt != nil && key.RotationFamily == oldID
		}
	}
	if !foundMetadata {
		t.Fatalf("safe lifecycle metadata for %q was not listed: %+v", newID, listed)
	}
}
