package modules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresSSOSettingsEncryptionAndReplicaCAS(t *testing.T) {
	dsn := os.Getenv("AUTH_POSTGRES_TEST_DSN")
	if dsn == "" {
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("AUTH_POSTGRES_TEST_DSN is required")
		}
		t.Skip("AUTH_POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	id, err := ssoRandom()
	if err != nil {
		t.Fatal(err)
	}
	// A private schema keeps the singleton fixture independent of other tests.
	schema := "sso_test_" + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(id, "-", ""), "_", ""))
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "postgres", "014_sso_settings.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := isolated.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	store := &PostgresVirtualKeyStore{pool: isolated}
	first, err := NewSSOManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSSOManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	secret := "fixture-encrypted-secret"
	input := SSODraftInput{SSOProfileConfig: testSSOConfig(), ClientSecret: &secret}
	var wins atomic.Int64
	var wg sync.WaitGroup
	for _, manager := range []*SSOManager{first, second} {
		wg.Add(1)
		go func(m *SSOManager) {
			defer wg.Done()
			_, err := m.SaveDraft(ctx, input)
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrSSOConflict) {
				t.Error(err)
			}
		}(manager)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("replicas both changed the same revision")
	}
	state, revision, err := second.Load(ctx)
	if err != nil || revision != 1 || state.Draft.ClientSecret != secret {
		t.Fatal("replica could not read persisted draft")
	}
	var plaintext bool
	if err := isolated.QueryRow(ctx, "SELECT position(convert_to($1,'UTF8') in payload)>0 FROM auth_sso_settings WHERE id=1", secret).Scan(&plaintext); err != nil || plaintext {
		t.Fatal("secret persisted in plaintext")
	}
	if _, err := isolated.Exec(ctx, "INSERT INTO auth_sso_settings(id,revision,payload) VALUES (2,1,$1)", []byte("invalid")); err == nil {
		t.Fatal("unbounded profile rows accepted")
	}
	input.ExpectedRevision = 1
	input.ClientSecret = nil
	if _, err := second.SaveDraft(ctx, input); err != nil {
		t.Fatal(err)
	}
	state, revision, err = first.Load(ctx)
	if err != nil || revision != 2 || state.Draft.ClientSecret != secret {
		t.Fatal("replica used stale settings")
	}
	// Existing rows encrypted with the old hash secret migrate once across replicas.
	newKey := strings.Repeat("n", 32)
	var migrants []*SSOManager
	for i := 0; i < 2; i++ {
		manager, err := newRuntimeSSOManager(store, newKey, strings.Repeat("k", 32))
		if err != nil {
			t.Fatal(err)
		}
		migrants = append(migrants, manager)
	}
	for _, manager := range migrants {
		wg.Add(1)
		go func(m *SSOManager) {
			defer wg.Done()
			state, revision, err := m.Load(ctx)
			if err != nil || revision != 3 || state.Draft.ClientSecret != secret {
				t.Error("replica migration failed")
			}
		}(manager)
	}
	wg.Wait()
	canonical, err := NewSSOManager(store, newKey)
	if err != nil {
		t.Fatal(err)
	}
	migrated, revision, err := canonical.Load(ctx)
	if err != nil || revision != 3 || migrated.Draft.ID != state.Draft.ID || migrated.Draft.SessionKey != state.Draft.SessionKey {
		t.Fatal("migration changed identity or cookie key")
	}
	if _, _, err := first.Load(ctx); !errors.Is(err, ErrSSOUnavailable) {
		t.Fatal("old key still reads migrated settings")
	}

}
