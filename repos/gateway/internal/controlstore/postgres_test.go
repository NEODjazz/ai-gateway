package controlstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"ai-gateway-gateway/internal/provider"
	"github.com/jackc/pgx/v5/pgxpool"
)

type memoryRevisionCache struct {
	values map[string][]byte
}

func (c *memoryRevisionCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	value, found := c.values[key]
	return append([]byte(nil), value...), found, nil
}

func (c *memoryRevisionCache) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	c.values[key] = append([]byte(nil), value...)
	return nil
}

func TestPostgresControlPlaneSnapshotLifecycleIntegration(t *testing.T) {
	dsn := os.Getenv("CONTROL_PLANE_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("CONTROL_PLANE_POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS gateway_control_plane_state
		(singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton), revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0), payload JSONB NOT NULL DEFAULT '{}'::jsonb, updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM gateway_control_plane_state`); err != nil {
		t.Fatal(err)
	}
	cache := &memoryRevisionCache{values: map[string][]byte{}}
	store, err := NewPostgresStore(ctx, dsn, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := provider.ControlPlaneSnapshot{
		Providers:   []provider.ManagedProvider{{ID: "ollama", Type: "ollama", BaseURL: "http://ollama:11434", Enabled: true}},
		Credentials: []provider.EncryptedCredentialSnapshot{{Credential: provider.Credential{ID: "ollama-key"}, Nonce: []byte("nonce"), Ciphertext: []byte("ciphertext")}},
		Deployments: []provider.ModelDeployment{{ID: "local", ProviderID: "ollama", ProviderType: "ollama", Models: []string{"public"}, UpstreamModel: "qwen", Weight: 1, Enabled: true}},
		ModelGroups: []provider.ModelGroup{{ID: "public", DeploymentIDs: []string{"local"}, Strategy: "weighted", Enabled: true}},
	}
	revision, err := store.Save(ctx, 0, snapshot)
	if err != nil || revision != 1 {
		t.Fatalf("save revision=%d err=%v", revision, err)
	}
	loaded, found, err := store.Load(ctx)
	if err != nil || !found || loaded.Revision != 1 || len(loaded.Credentials) != 1 || string(loaded.Credentials[0].Ciphertext) != "ciphertext" {
		t.Fatalf("unexpected loaded snapshot: found=%v err=%v snapshot=%+v", found, err, loaded)
	}
	if _, err := store.Save(ctx, 0, snapshot); !errors.Is(err, provider.ErrControlPlaneConflict) {
		t.Fatalf("expected optimistic conflict, got %v", err)
	}
	if current, err := store.Revision(ctx); err != nil || current != 1 {
		t.Fatalf("revision=%d err=%v", current, err)
	}
}

func TestPostgresControlPlaneRestoresManagedRouterIntegration(t *testing.T) {
	dsn := os.Getenv("CONTROL_PLANE_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("CONTROL_PLANE_POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS gateway_control_plane_state
		(singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton), revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0), payload JSONB NOT NULL DEFAULT '{}'::jsonb, updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM gateway_control_plane_state`); err != nil {
		t.Fatal(err)
	}
	pool.Close()
	store, err := NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("stable-integration-master-key")
	firstRuntime, err := provider.NewWithError(provider.Config{CredentialEncryptionKey: key, ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	first := firstRuntime.(interface {
		provider.ProviderController
		provider.CredentialController
		provider.DeploymentController
		provider.ModelGroupController
	})
	if _, err := first.CreateProvider(provider.ManagedProvider{ID: "persisted", Type: "demo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateCredential(provider.CredentialInput{ID: "persisted-key", ProviderID: "persisted", Secret: "never-plaintext-at-rest"}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateModelDeployment(provider.ModelDeployment{ID: "persisted-deployment", ProviderID: "persisted", CredentialID: "persisted-key", Models: []string{"upstream"}, UpstreamModel: "upstream", Weight: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateModelGroup(provider.ModelGroup{ID: "persisted-public", DeploymentIDs: []string{"persisted-deployment"}, Strategy: "weighted", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	reopened, err := NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	secondRuntime, err := provider.NewWithError(provider.Config{CredentialEncryptionKey: key, ControlPlaneStore: reopened})
	if err != nil {
		t.Fatal(err)
	}
	models := secondRuntime.Models()
	foundPublic := false
	for _, model := range models {
		foundPublic = foundPublic || model.ID == "persisted-public"
	}
	if len(models) != 2 || !foundPublic {
		t.Fatalf("restored router models: %+v", models)
	}
	credentials := secondRuntime.(provider.CredentialController).ListCredentials(ctx)
	if len(credentials) != 1 || credentials[0].ID != "persisted-key" {
		t.Fatalf("restored credentials metadata: %+v", credentials)
	}
}
