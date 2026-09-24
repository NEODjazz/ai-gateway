package controlstore

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestPostgresResponseSessionsIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.pool.Exec(t.Context(), `CREATE TABLE gateway_response_sessions (
		key TEXT PRIMARY KEY CHECK (length(key) BETWEEN 1 AND 256),
		value BYTEA NOT NULL CHECK (octet_length(value) BETWEEN 1 AND 4096),
		expires_at TIMESTAMPTZ NOT NULL);
		CREATE INDEX gateway_response_sessions_expires_at_idx ON gateway_response_sessions (expires_at)`); err != nil {
		t.Fatal(err)
	}
	sessions := store.ResponseSessions()
	if _, found, err := sessions.Get(t.Context(), "responses-affinity:missing"); err != nil || found {
		t.Fatalf("missing record found=%v err=%v", found, err)
	}
	if err := sessions.Set(t.Context(), "responses-affinity:one", []byte("first"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Set(t.Context(), "responses-affinity:one", []byte("second"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if value, found, err := sessions.Get(t.Context(), "responses-affinity:one"); err != nil || !found || string(value) != "second" {
		t.Fatalf("affinity value=%q found=%v err=%v", value, found, err)
	}

	const ownershipKey = "response-owner:v1:responses-affinity:one"
	if accepted, err := sessions.SetIfAbsentOrEqual(t.Context(), ownershipKey, []byte("owner-a"), time.Hour); err != nil || !accepted {
		t.Fatalf("first immutable write accepted=%v err=%v", accepted, err)
	}
	var expiry time.Time
	if err := store.pool.QueryRow(t.Context(), `SELECT expires_at FROM gateway_response_sessions WHERE key=$1`, ownershipKey).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	if accepted, err := sessions.SetIfAbsentOrEqual(t.Context(), ownershipKey, []byte("owner-a"), 2*time.Hour); err != nil || !accepted {
		t.Fatalf("identical retry accepted=%v err=%v", accepted, err)
	}
	var retryExpiry time.Time
	if err := store.pool.QueryRow(t.Context(), `SELECT expires_at FROM gateway_response_sessions WHERE key=$1`, ownershipKey).Scan(&retryExpiry); err != nil || !retryExpiry.Equal(expiry) {
		t.Fatalf("identical retry extended expiry: before=%v after=%v err=%v", expiry, retryExpiry, err)
	}
	if accepted, err := sessions.SetIfAbsentOrEqual(t.Context(), ownershipKey, []byte("owner-b"), time.Hour); err != nil || accepted {
		t.Fatalf("conflicting write accepted=%v err=%v", accepted, err)
	}
	if removed, err := sessions.DeleteIfEqual(t.Context(), ownershipKey, []byte("owner-b")); err != nil || removed {
		t.Fatalf("conflicting delete removed=%v err=%v", removed, err)
	}
	if removed, err := sessions.DeleteIfEqual(t.Context(), ownershipKey, []byte("owner-a")); err != nil || !removed {
		t.Fatalf("matching delete removed=%v err=%v", removed, err)
	}
	if removed, err := sessions.DeleteIfEqual(t.Context(), ownershipKey, []byte("owner-a")); err != nil || !removed {
		t.Fatalf("idempotent delete removed=%v err=%v", removed, err)
	}

	if _, err := store.pool.Exec(t.Context(), `UPDATE gateway_response_sessions SET expires_at=now()-interval '1 second' WHERE key=$1`, "responses-affinity:one"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := sessions.Get(t.Context(), "responses-affinity:one"); err != nil || found {
		t.Fatalf("expired affinity found=%v err=%v", found, err)
	}
	if accepted, err := sessions.SetIfAbsentOrEqual(t.Context(), "responses-affinity:one", []byte("new"), time.Hour); err != nil || !accepted {
		t.Fatalf("expired record replacement accepted=%v err=%v", accepted, err)
	}
	if value, found, err := sessions.Get(t.Context(), "responses-affinity:one"); err != nil || !found || string(value) != "new" {
		t.Fatalf("replacement value=%q found=%v err=%v", value, found, err)
	}
}

func TestPostgresResponseSessionsImmutableWriteIsAtomic(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.pool.Exec(t.Context(), `CREATE TABLE gateway_response_sessions (
		key TEXT PRIMARY KEY, value BYTEA NOT NULL, expires_at TIMESTAMPTZ NOT NULL);
		CREATE INDEX gateway_response_sessions_expires_at_idx ON gateway_response_sessions (expires_at)`); err != nil {
		t.Fatal(err)
	}
	sessions := store.ResponseSessions()
	const writers = 8
	var wg sync.WaitGroup
	accepted := make(chan string, writers)
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			value := string(rune('a' + i))
			ok, err := sessions.SetIfAbsentOrEqual(context.Background(), "response-owner:v1:race", []byte(value), time.Hour)
			if err != nil {
				errs <- err
			} else if ok {
				accepted <- value
			}
		}(i)
	}
	wg.Wait()
	close(accepted)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	winner := ""
	for value := range accepted {
		if winner != "" {
			t.Fatalf("multiple immutable winners: %q and %q", winner, value)
		}
		winner = value
	}
	value, found, err := sessions.Get(t.Context(), "response-owner:v1:race")
	if err != nil || !found || winner == "" || string(value) != winner {
		t.Fatalf("winner=%q value=%q found=%v err=%v", winner, value, found, err)
	}
}
