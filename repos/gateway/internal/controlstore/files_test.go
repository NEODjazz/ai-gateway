package controlstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/filestate"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresFileLifecycleAndIsolationIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	ctx := context.Background()
	pool := prepareFileTable(t, ctx, dsn)
	owner := "file-integration/" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM gateway_files WHERE owner_key=$1`, owner)
		pool.Close()
	})
	store, err := NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.Create(ctx, filestate.File{
		ID: "file_lifecycle", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch",
		ContentType: "application/jsonl", Bytes: 7, Content: []byte("payload"),
	}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if created.CreatedAt.IsZero() || created.Content != nil {
		t.Fatalf("created file must include timestamp and omit content: %+v", created)
	}

	metadata, err := store.Get(ctx, owner, created.ID, false)
	if err != nil || metadata.Content != nil || metadata.Bytes != 7 {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	withContent, err := store.Get(ctx, owner, created.ID, true)
	if err != nil || string(withContent.Content) != "payload" {
		t.Fatalf("content=%q err=%v", withContent.Content, err)
	}
	if _, err := store.Get(ctx, owner+"/other", created.ID, true); !errors.Is(err, filestate.ErrNotFound) {
		t.Fatalf("cross-owner get error=%v", err)
	}
	if err := store.Delete(ctx, owner+"/other", created.ID); !errors.Is(err, filestate.ErrNotFound) {
		t.Fatalf("cross-owner delete error=%v", err)
	}
	if _, err := store.Create(ctx, filestate.File{
		ID: created.ID, OwnerKey: owner + "/other", Filename: "collision", Purpose: "batch",
		ContentType: "application/octet-stream", Bytes: 1, Content: []byte("x"),
	}, 1024); !errors.Is(err, filestate.ErrConflict) {
		t.Fatalf("global id collision error=%v", err)
	}
	if err := store.Delete(ctx, owner, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, owner, created.ID, false); !errors.Is(err, filestate.ErrNotFound) {
		t.Fatalf("deleted file error=%v", err)
	}
}

func TestPostgresFileQuotaIsAtomicIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	ctx := context.Background()
	pool := prepareFileTable(t, ctx, dsn)
	owner := "file-quota/" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM gateway_files WHERE owner_key=$1`, owner)
		pool.Close()
	})
	store, err := NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, id := range []string{"file_quota_a", "file_quota_b"} {
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			<-start
			_, err := store.Create(ctx, filestate.File{
				ID: id, OwnerKey: owner, Filename: id, Purpose: "batch",
				ContentType: "application/octet-stream", Bytes: 6, Content: []byte("123456"),
			}, 10)
			results <- err
		}(id)
	}
	close(start)
	workers.Wait()
	close(results)

	var succeeded, rejected int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, filestate.ErrQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
	var used int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(bytes),0) FROM gateway_files WHERE owner_key=$1`, owner).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 6 {
		t.Fatalf("used bytes=%d, want 6", used)
	}
}

func TestPostgresFileListPaginationIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	ctx := context.Background()
	pool := prepareFileTable(t, ctx, dsn)
	owner := "file-list/" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM gateway_files WHERE owner_key=$1`, owner)
		pool.Close()
	})
	store, err := NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, file := range []filestate.File{
		{ID: "file_list_a", Filename: "a", Purpose: "batch"},
		{ID: "file_list_b", Filename: "b", Purpose: "batch"},
		{ID: "file_list_c", Filename: "c", Purpose: "fine-tune"},
	} {
		file.OwnerKey = owner
		file.ContentType = "text/plain"
		file.Bytes = 1
		file.Content = []byte("x")
		if _, err := store.Create(ctx, file, 1024); err != nil {
			t.Fatal(err)
		}
	}

	first, after, err := store.List(ctx, owner, "", 2, "")
	if err != nil || len(first) != 2 || after == "" {
		t.Fatalf("first page=%+v after=%q err=%v", first, after, err)
	}
	second, next, err := store.List(ctx, owner, "", 2, after)
	if err != nil || len(second) != 1 || next != "" {
		t.Fatalf("second page=%+v next=%q err=%v", second, next, err)
	}
	ids := []string{first[0].ID, first[1].ID, second[0].ID}
	sort.Strings(ids)
	if want := []string{"file_list_a", "file_list_b", "file_list_c"}; ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("paginated ids=%v", ids)
	}
	batch, _, err := store.List(ctx, owner, "batch", 100, "")
	if err != nil || len(batch) != 2 {
		t.Fatalf("batch files=%+v err=%v", batch, err)
	}
	if _, _, err := store.List(ctx, owner, "", 2, "missing"); !errors.Is(err, filestate.ErrNotFound) {
		t.Fatalf("missing cursor error=%v", err)
	}
	if _, _, err := store.List(ctx, owner, "", 0, ""); !errors.Is(err, filestate.ErrInvalid) {
		t.Fatalf("invalid limit error=%v", err)
	}
}

func requiredPostgresTestDSN(t *testing.T) string {
	t.Helper()
	baseDSN := os.Getenv("CONTROL_PLANE_POSTGRES_TEST_DSN")
	if baseDSN == "" {
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("CONTROL_PLANE_POSTGRES_TEST_DSN is required")
		}
		t.Skip("CONTROL_PLANE_POSTGRES_TEST_DSN is not set")
	}

	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("generate isolated PostgreSQL schema name: %v", err)
	}
	schema := "gateway_test_" + hex.EncodeToString(random)
	identifier := `"` + schema + `"`
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		t.Fatalf("connect to PostgreSQL for isolated schema: %v", err)
	}
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		adminPool.Close()
		t.Fatalf("create isolated PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("drop isolated PostgreSQL schema: %v", err)
		}
		adminPool.Close()
	})

	dsn, err := postgresDSNWithSearchPath(baseDSN, schema)
	if err != nil {
		t.Fatalf("configure isolated PostgreSQL schema: %v", err)
	}
	verificationPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to isolated PostgreSQL schema: %v", err)
	}
	var currentSchema string
	if err := verificationPool.QueryRow(ctx, `SELECT current_schema()`).Scan(&currentSchema); err != nil {
		verificationPool.Close()
		t.Fatalf("verify isolated PostgreSQL schema: %v", err)
	}
	verificationPool.Close()
	if currentSchema != schema {
		t.Fatalf("PostgreSQL integration test schema=%q, want %q", currentSchema, schema)
	}
	return dsn
}

func postgresDSNWithSearchPath(dsn, schema string) (string, error) {
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}
	for _, character := range schema {
		if character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return "", fmt.Errorf("invalid schema name %q", schema)
		}
	}
	return dsn + " search_path=" + schema, nil
}

func prepareFileTable(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS gateway_files
		(id TEXT PRIMARY KEY, owner_key TEXT NOT NULL, filename TEXT NOT NULL, purpose TEXT NOT NULL,
		content_type TEXT NOT NULL, bytes BIGINT NOT NULL CHECK (bytes >= 0), content BYTEA NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(), CHECK (octet_length(content) = bytes))`)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return pool
}
