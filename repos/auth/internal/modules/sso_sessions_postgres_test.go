package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func ssoSessionPostgresStore(t *testing.T) *PostgresVirtualKeyStore {
	t.Helper()
	dsn := os.Getenv("AUTH_POSTGRES_TEST_DSN")
	if dsn == "" {
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("AUTH_POSTGRES_TEST_DSN is required")
		}
		t.Skip("AUTH_POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ssoRandom()
	if err != nil {
		t.Fatal(err)
	}
	schema := "sso_sessions_" + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(id, "-", ""), "_", ""))
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	for _, name := range []string{"014_sso_settings.sql", "015_sso_sessions.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	return &PostgresVirtualKeyStore{pool: pool}
}
func TestPostgresSSOSessionsReplicaEncryptionReplayAndLogout(t *testing.T) {
	store := ssoSessionPostgresStore(t)
	module, _, _, profile, key := browserIdentityFixture(t)
	manager, err := NewSSOManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.save(t.Context(), 0, SSOSettingsState{SchemaVersion: 1, APITrustSeparated: true, Active: profile}); err != nil {
		t.Fatal(err)
	}
	module.sso = manager
	nonce := strings.Repeat("n", 43)
	token := signRS256JWT(t, "browser", key, browserIdentityClaims(profile, nonce))
	login := SSOBrowserLogin{ProfileID: profile.ID, Token: token, Nonce: nonce}
	session, err := module.CreateSSOBrowserSession(t.Context(), login)
	if err != nil {
		t.Fatal(err)
	}
	replica := module
	replica.sso, err = NewSSOManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	req := RequestContext{APIKey: session.Token}
	if err := replica.Handle(t.Context(), &req); err != nil {
		t.Fatal("replica rejected persisted session", err)
	}
	if err := replica.ReauthorizeJWTPrincipal(t.Context(), &req); err != nil {
		t.Fatal("browser background reauthorization failed", err)
	}
	var exposed bool
	if err := store.pool.QueryRow(t.Context(), "SELECT position(convert_to($1,'UTF8') in payload)>0 FROM auth_sso_sessions", token).Scan(&exposed); err != nil || exposed {
		t.Fatal("ID token persisted or database query failed")
	}
	if err := replica.RevokeSSOBrowserSession(t.Context(), session.Token); err != nil {
		t.Fatal(err)
	}
	if err := module.Handle(t.Context(), &RequestContext{APIKey: session.Token}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("logout did not revoke session across replicas")
	}
	if err := module.ReauthorizeJWTPrincipal(t.Context(), &req); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("logout did not revoke background authorization")
	}
	if _, err := replica.CreateSSOBrowserSession(t.Context(), login); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("nonce replay accepted after logout: %v", err)
	}
	var count int
	if err := store.pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_sso_logins").Scan(&count); err != nil || count != 1 {
		t.Fatal("replay state not retained")
	}
	raw, _ := json.Marshal(req)
	if strings.Contains(string(raw), session.Token) || strings.Contains(string(raw), token) {
		t.Fatal("token exposed in authorized context")
	}
}
func TestPostgresSSOSessionAdmissionBoundsAndExpiry(t *testing.T) {
	store := ssoSessionPostgresStore(t)
	expiry := time.Now().Add(time.Hour).Unix()
	// Admission for one user is serialized across concurrent replicas.
	var wins atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			hash := fmt.Sprintf("%064x", index+1)
			if err := store.CreateSSOSession(t.Context(), hash, hash, "profile", "one-user", expiry, []byte("fixture")); err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrSSOUnavailable) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 16 {
		t.Fatalf("per-user admission allowed %d sessions", wins.Load())
	}
	if _, err := store.pool.Exec(t.Context(), "UPDATE auth_sso_sessions SET expires_at=now()-interval '1 minute'; UPDATE auth_sso_logins SET expires_at=now()-interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%064x", 100)
	if err := store.CreateSSOSession(t.Context(), hash, hash, "profile", "one-user", expiry, []byte("fixture")); err != nil {
		t.Fatal("expired identities not evicted", err)
	}
	for _, test := range []struct {
		name          string
		count         int
		profile, user string
	}{
		{"profile", 1000, "bounded-profile", "unique-user"},
		{"global", 10000, "new-profile", "unique-user"},
		{"nonce", 20000, "new-profile", "unique-user"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.pool.Exec(t.Context(), "DELETE FROM auth_sso_sessions; DELETE FROM auth_sso_logins"); err != nil {
				t.Fatal(err)
			}
			if test.name == "nonce" {
				_, err := store.pool.Exec(t.Context(), `INSERT INTO auth_sso_logins(nonce_hash,expires_at) SELECT lpad(i::text,64,'0'),now()+interval '10 minutes' FROM generate_series(1,$1)i`, test.count)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				profile := "'profile-'||i::text"
				if test.name == "profile" {
					profile = "'bounded-profile'"
				}
				_, err := store.pool.Exec(t.Context(), `INSERT INTO auth_sso_sessions(token_hash,profile_id,user_id,expires_at,payload) SELECT lpad(i::text,64,'0'),`+profile+`, 'user-'||i::text, now()+interval '1 hour',convert_to('fixture','UTF8') FROM generate_series(1,$1)i`, test.count)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := store.CreateSSOSession(t.Context(), strings.Repeat("a", 64), strings.Repeat("b", 64), test.profile, test.user, expiry, []byte("fixture")); !errors.Is(err, ErrSSOUnavailable) {
				t.Fatal("bound exceeded", err)
			}
		})
	}
}
