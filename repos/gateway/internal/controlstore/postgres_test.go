package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ai-gateway-gateway/internal/mcpstate"
	"ai-gateway-gateway/internal/provider"
	"github.com/jackc/pgx/v5/pgxpool"
)

type memoryRevisionCache struct {
	values map[string][]byte
}

func TestPostgresMCPToolCallIdempotencyIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS gateway_mcp_tool_calls
		(scope_key TEXT NOT NULL, idempotency_key TEXT NOT NULL, request_hash TEXT NOT NULL,
		execution_id TEXT NOT NULL, state TEXT NOT NULL CHECK (state IN ('pending','completed')),
		http_status INTEGER NOT NULL DEFAULT 0, response BYTEA, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY (scope_key, idempotency_key))`)
	if err != nil {
		t.Fatal(err)
	}
	var responseType string
	if err := pool.QueryRow(ctx, `SELECT data_type FROM information_schema.columns WHERE table_name='gateway_mcp_tool_calls' AND column_name='response'`).Scan(&responseType); err != nil {
		t.Fatal(err)
	}
	if responseType != "bytea" {
		if _, err := pool.Exec(ctx, `ALTER TABLE gateway_mcp_tool_calls ALTER COLUMN response TYPE BYTEA USING convert_to(response::text, 'UTF8')`); err != nil {
			t.Fatal(err)
		}
	}
	scope := "integration/" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM gateway_mcp_tool_calls WHERE scope_key=$1`, scope)
	})
	store, err := NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	claim := mcpstate.Record{ScopeKey: scope, IdempotencyKey: "forecast", RequestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ExecutionID: "execution-1"}
	record, owner, err := store.Claim(ctx, claim)
	if err != nil || !owner || record.State != mcpstate.StatePending {
		t.Fatalf("claim=%+v owner=%v err=%v", record, owner, err)
	}
	duplicate, owner, err := store.Claim(ctx, claim)
	if err != nil || owner || duplicate.ExecutionID != claim.ExecutionID {
		t.Fatalf("duplicate=%+v owner=%v err=%v", duplicate, owner, err)
	}
	conflict := claim
	conflict.RequestHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, _, err := store.Claim(ctx, conflict); !errors.Is(err, mcpstate.ErrConflict) {
		t.Fatalf("conflicting payload error=%v", err)
	}
	payload := json.RawMessage(`{"content":[{"type":"text","text":"sunny"}]}`)
	if err := store.Complete(ctx, scope, claim.IdempotencyKey, claim.ExecutionID, 200, payload); err != nil {
		t.Fatal(err)
	}
	replayed, owner, err := store.Claim(ctx, claim)
	if err != nil || owner || replayed.State != mcpstate.StateCompleted || replayed.HTTPStatus != 200 || string(replayed.Response) != string(payload) {
		t.Fatalf("replay=%+v owner=%v err=%v", replayed, owner, err)
	}
	releasable := mcpstate.Record{ScopeKey: scope, IdempotencyKey: "retry", RequestHash: claim.RequestHash, ExecutionID: "execution-2"}
	if _, owner, err := store.Claim(ctx, releasable); err != nil || !owner {
		t.Fatalf("release claim owner=%v err=%v", owner, err)
	}
	if err := store.Release(ctx, scope, releasable.IdempotencyKey, releasable.ExecutionID); err != nil {
		t.Fatal(err)
	}
	if _, owner, err := store.Claim(ctx, releasable); err != nil || !owner {
		t.Fatalf("reclaimed owner=%v err=%v", owner, err)
	}
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
	dsn := requiredPostgresTestDSN(t)
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
		Providers: []provider.ManagedProvider{
			{ID: "ollama", Type: "ollama", BaseURL: "http://ollama:11434", Enabled: true},
			{ID: "foundry-gov", Type: "azure-openai", BaseURL: "https://proxy.example.test/api/projects/project-a", AuthType: "entra", AzureCloud: "usgov", AzureAudience: "foundry", Enabled: true},
		},
		Credentials: []provider.EncryptedCredentialSnapshot{{Credential: provider.Credential{ID: "ollama-key"}, Nonce: []byte("nonce"), Ciphertext: []byte("ciphertext")}},
		Deployments: []provider.ModelDeployment{{ID: "local", ProviderID: "ollama", ProviderType: "ollama", Models: []string{"public"}, UpstreamModel: "qwen", Weight: 1, Enabled: true}},
		ModelGroups: []provider.ModelGroup{{ID: "public", DeploymentIDs: []string{"local"}, Strategy: "weighted", Enabled: true}},
	}
	revision, err := store.Save(ctx, 0, snapshot)
	if err != nil || revision != 1 {
		t.Fatalf("save revision=%d err=%v", revision, err)
	}
	loaded, found, err := store.Load(ctx)
	if err != nil || !found || loaded.Revision != 1 || len(loaded.Credentials) != 1 || string(loaded.Credentials[0].Ciphertext) != "ciphertext" || len(loaded.Providers) != 2 || loaded.Providers[1].AzureCloud != "usgov" || loaded.Providers[1].AzureAudience != "foundry" {
		t.Fatalf("unexpected loaded snapshot: found=%v err=%v snapshot=%+v", found, err, loaded)
	}
	if _, err := store.Save(ctx, 0, snapshot); !errors.Is(err, provider.ErrControlPlaneConflict) {
		t.Fatalf("expected optimistic conflict, got %v", err)
	}
	if current, err := store.Revision(ctx); err != nil || current != 1 {
		t.Fatalf("revision=%d err=%v", current, err)
	}
	cache.values[revisionCacheKey] = []byte("99")
	if current, err := store.Revision(ctx); err != nil || current != 1 || string(cache.values[revisionCacheKey]) != "1" {
		t.Fatalf("postgres revision did not repair cache: revision=%d cache=%q err=%v", current, cache.values[revisionCacheKey], err)
	}
}

func TestPostgresControlPlaneRestoresManagedRouterIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
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
