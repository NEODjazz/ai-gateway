package controlstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/vectorstate"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresVectorStoreLifecycleIsolationAndPaginationIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	ctx := context.Background()
	pool := prepareVectorStoreTable(t, ctx, dsn)
	owner := "vector-integration/" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM gateway_vector_stores WHERE owner_key=$1`, owner)
		pool.Close()
	})
	store, err := NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, value := range []vectorstate.VectorStore{
		{ID: "vs_integration_a", Name: "A", Metadata: map[string]string{"team": "one"}, ExpiresAfter: 30},
		{ID: "vs_integration_b", Name: "B", Metadata: map[string]string{}},
	} {
		value.OwnerKey = owner
		created, createErr := store.CreateVectorStore(ctx, value, 10)
		if createErr != nil || created.Status != "completed" || created.CreatedAt.IsZero() || created.LastActiveAt.IsZero() {
			t.Fatalf("created=%+v err=%v", created, createErr)
		}
	}
	if _, err := store.GetVectorStore(ctx, owner+"/other", "vs_integration_a"); !errors.Is(err, vectorstate.ErrNotFound) {
		t.Fatalf("cross-owner error=%v", err)
	}
	name := "Updated"
	metadata := map[string]string{"stage": "ready"}
	days := 7
	updated, err := store.UpdateVectorStore(ctx, owner, "vs_integration_a", vectorstate.Update{Name: &name, Metadata: &metadata, ExpiresAfter: &days})
	if err != nil || updated.Name != name || updated.ExpiresAfter != days || updated.Metadata["stage"] != "ready" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	first, after, err := store.ListVectorStores(ctx, owner, 1, "")
	if err != nil || len(first) != 1 || after == "" {
		t.Fatalf("first=%+v after=%q err=%v", first, after, err)
	}
	second, next, err := store.ListVectorStores(ctx, owner, 1, after)
	if err != nil || len(second) != 1 || next != "" || second[0].ID == first[0].ID {
		t.Fatalf("second=%+v next=%q err=%v", second, next, err)
	}
	if err := store.DeleteVectorStore(ctx, owner+"/other", "vs_integration_a"); !errors.Is(err, vectorstate.ErrNotFound) {
		t.Fatalf("cross-owner delete error=%v", err)
	}
	if err := store.DeleteVectorStore(ctx, owner, "vs_integration_a"); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresVectorStoreFileLifecycleIsolationAndQuotaIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	ctx := context.Background()
	pool := prepareVectorStoreTable(t, ctx, dsn)
	owner := "vector-files/" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM gateway_vector_store_files WHERE owner_key=$1`, owner)
		_, _ = pool.Exec(context.Background(), `DELETE FROM gateway_files WHERE owner_key=$1`, owner)
		_, _ = pool.Exec(context.Background(), `DELETE FROM gateway_vector_stores WHERE owner_key=$1`, owner)
		pool.Close()
	})
	store, err := NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.CreateVectorStore(ctx, vectorstate.VectorStore{ID: "vs_files", OwnerKey: owner, Name: "files", Metadata: map[string]string{}}, 1); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"file_vector_a", "file_vector_b", "file_vector_c"} {
		content := []byte{byte('a' + index)}
		_, err = store.Create(ctx, filestate.File{ID: id, OwnerKey: owner, Filename: id + ".txt", Purpose: "assistants", ContentType: "text/plain", Bytes: int64(len(content)), Content: content}, 100)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.Create(ctx, filestate.File{ID: "file_vector_expired", OwnerKey: owner, Filename: "expired.txt", Purpose: "assistants", ContentType: "text/plain", Bytes: 1, Content: []byte("x"), ExpiresAfterSeconds: 3600}, 100); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE gateway_files SET expires_at=now()-interval '1 second' WHERE owner_key=$1 AND id='file_vector_expired'`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AttachVectorStoreFile(ctx, owner, "vs_files", "file_vector_expired", 2, 100); !errors.Is(err, vectorstate.ErrFileNotFound) {
		t.Fatalf("expired file error=%v", err)
	}
	if _, err = store.AttachVectorStoreFile(ctx, owner, "vs_files", "missing", 2, 100); !errors.Is(err, vectorstate.ErrFileNotFound) {
		t.Fatalf("missing file error=%v", err)
	}
	for _, id := range []string{"file_vector_a", "file_vector_b"} {
		attached, attachErr := store.AttachVectorStoreFile(ctx, owner, "vs_files", id, 2, 100)
		if attachErr != nil || attached.Status != "completed" || attached.Bytes != 1 {
			t.Fatalf("attached=%+v err=%v", attached, attachErr)
		}
	}
	if _, err = store.AttachVectorStoreFile(ctx, owner, "vs_files", "file_vector_a", 2, 100); !errors.Is(err, vectorstate.ErrConflict) {
		t.Fatalf("duplicate error=%v", err)
	}
	if _, err = store.AttachVectorStoreFile(ctx, owner, "vs_files", "file_vector_c", 2, 100); !errors.Is(err, vectorstate.ErrFileQuotaExceeded) {
		t.Fatalf("quota error=%v", err)
	}
	if _, err = store.GetVectorStoreFile(ctx, owner+"/other", "vs_files", "file_vector_a"); !errors.Is(err, vectorstate.ErrFileNotFound) {
		t.Fatalf("cross-owner error=%v", err)
	}
	first, after, err := store.ListVectorStoreFiles(ctx, owner, "vs_files", 1, "")
	if err != nil || len(first) != 1 || after == "" {
		t.Fatalf("first=%+v after=%q err=%v", first, after, err)
	}
	second, next, err := store.ListVectorStoreFiles(ctx, owner, "vs_files", 1, after)
	if err != nil || len(second) != 1 || next != "" || second[0].FileID == first[0].FileID {
		t.Fatalf("second=%+v next=%q err=%v", second, next, err)
	}
	vectorStore, err := store.GetVectorStore(ctx, owner, "vs_files")
	if err != nil || vectorStore.FileCount != 2 || vectorStore.UsageBytes != 2 {
		t.Fatalf("vector store totals=%+v err=%v", vectorStore, err)
	}
	if err = store.DeleteVectorStoreFile(ctx, owner, "vs_files", "file_vector_a"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetVectorStoreFile(ctx, owner, "vs_files", "file_vector_a"); !errors.Is(err, vectorstate.ErrFileNotFound) {
		t.Fatalf("deleted file error=%v", err)
	}
	if _, err = store.CreateVectorStore(ctx, vectorstate.VectorStore{ID: "vs_files_concurrent", OwnerKey: owner, Name: "concurrent", Metadata: map[string]string{}}, 2); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, fileID := range []string{"file_vector_a", "file_vector_c"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, attachErr := store.AttachVectorStoreFile(ctx, owner, "vs_files_concurrent", fileID, 1, 100)
			results <- attachErr
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	var succeeded, rejected int
	for result := range results {
		switch {
		case result == nil:
			succeeded++
		case errors.Is(result, vectorstate.ErrFileQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected concurrent attach result: %v", result)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent attach succeeded=%d rejected=%d", succeeded, rejected)
	}
	if _, err = store.CreateVectorStore(ctx, vectorstate.VectorStore{ID: "vs_bytes_concurrent", OwnerKey: owner, Name: "bytes", Metadata: map[string]string{}}, 3); err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	results = make(chan error, 2)
	workers = sync.WaitGroup{}
	for _, fileID := range []string{"file_vector_a", "file_vector_c"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, attachErr := store.AttachVectorStoreFile(ctx, owner, "vs_bytes_concurrent", fileID, 2, 1)
			results <- attachErr
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded, rejected = 0, 0
	for result := range results {
		switch {
		case result == nil:
			succeeded++
		case errors.Is(result, vectorstate.ErrByteQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected concurrent byte attach result: %v", result)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent byte attach succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestPostgresVectorStoreQuotaIsAtomicIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	ctx := context.Background()
	pool := prepareVectorStoreTable(t, ctx, dsn)
	owner := "vector-quota/" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM gateway_vector_stores WHERE owner_key=$1`, owner)
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
	for _, id := range []string{"vs_quota_a", "vs_quota_b"} {
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			<-start
			_, createErr := store.CreateVectorStore(ctx, vectorstate.VectorStore{ID: id, OwnerKey: owner, Name: id, Metadata: map[string]string{}}, 1)
			results <- createErr
		}(id)
	}
	close(start)
	workers.Wait()
	close(results)
	var succeeded, rejected int
	for result := range results {
		switch {
		case result == nil:
			succeeded++
		case errors.Is(result, vectorstate.ErrQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected result: %v", result)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
	if _, err := pool.Exec(ctx, `UPDATE gateway_vector_stores SET expires_after_days=1,expires_at=now()-interval '1 second' WHERE owner_key=$1`, owner); err != nil {
		t.Fatal(err)
	}
	var expiredID string
	if err := pool.QueryRow(ctx, `SELECT id FROM gateway_vector_stores WHERE owner_key=$1`, owner).Scan(&expiredID); err != nil {
		t.Fatal(err)
	}
	expired, err := store.GetVectorStore(ctx, owner, expiredID)
	if err != nil || expired.Status != "expired" {
		t.Fatalf("expired=%+v err=%v", expired, err)
	}
	if _, err := store.CreateVectorStore(ctx, vectorstate.VectorStore{ID: "vs_quota_after_expiry", OwnerKey: owner, Name: "replacement", Metadata: map[string]string{}}, 1); err != nil {
		t.Fatalf("expired store still consumed quota: %v", err)
	}
}

func prepareVectorStoreTable(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS gateway_vector_stores (
		id TEXT PRIMARY KEY, owner_key TEXT NOT NULL, name TEXT NOT NULL, metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
		expires_after_days INTEGER NOT NULL DEFAULT 0 CHECK (expires_after_days BETWEEN 0 AND 365),
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_active_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		expires_at TIMESTAMPTZ, updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err == nil {
		_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS gateway_files (
			id TEXT PRIMARY KEY, owner_key TEXT NOT NULL, filename TEXT NOT NULL, purpose TEXT NOT NULL,
			content_type TEXT NOT NULL, bytes BIGINT NOT NULL CHECK (bytes >= 0), content BYTEA NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(), expires_at TIMESTAMPTZ, CHECK (octet_length(content) = bytes))`)
	}
	if err == nil {
		_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS gateway_vector_store_files (
			vector_store_id TEXT NOT NULL REFERENCES gateway_vector_stores(id) ON DELETE CASCADE,
			file_id TEXT NOT NULL REFERENCES gateway_files(id) ON DELETE CASCADE,
			owner_key TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'completed', created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (vector_store_id,file_id))`)
	}
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return pool
}
