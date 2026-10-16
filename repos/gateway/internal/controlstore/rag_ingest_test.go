package controlstore

import (
	"context"
	"errors"
	"os"
	"testing"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/ragstate"
	"ai-gateway-gateway/internal/vectorstate"
)

func TestPostgresRAGIngestIsAtomic(t *testing.T) {
	dsn := os.Getenv("CONTROL_PLANE_POSTGRES_TEST_DSN")
	if dsn == "" {
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("CONTROL_PLANE_POSTGRES_TEST_DSN is required")
		}
		t.Skip("CONTROL_PLANE_POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	pool := prepareVectorStoreTable(t, ctx, dsn)
	defer pool.Close()
	store, err := NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := "rag-ingest-atomic"
	_, _ = pool.Exec(ctx, `DELETE FROM gateway_vector_stores WHERE owner_key=$1`, owner)
	_, _ = pool.Exec(ctx, `DELETE FROM gateway_files WHERE owner_key=$1`, owner)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM gateway_vector_stores WHERE owner_key=$1`, owner)
		_, _ = pool.Exec(context.Background(), `DELETE FROM gateway_files WHERE owner_key=$1`, owner)
	})

	file := filestate.File{ID: "file_rag_atomic", OwnerKey: owner, Filename: "guide.txt", Purpose: "assistants", ContentType: "text/plain", Bytes: 5, Content: []byte("guide")}
	vectorStore := vectorstate.VectorStore{ID: "vs_rag_atomic", OwnerKey: owner, Name: "guides", Metadata: map[string]string{"region": "eu"}}
	result, err := store.IngestRAG(ctx, ragstate.IngestRequest{
		OwnerKey: owner, File: &file, VectorStore: &vectorStore, Attributes: map[string]any{"kind": "guide"},
		ChunkingStrategy: vectorstate.ChunkingStrategy{Type: "static", MaxChunkSizeTokens: 800, ChunkOverlapTokens: 200},
		FileOwnerQuota:   100, VectorStoreQuota: 2, VectorStoreFiles: 1, VectorStoreBytes: 100,
	})
	if err != nil || result.File.ID != file.ID || result.VectorStore.ID != vectorStore.ID || result.VectorStore.FileCount != 1 || result.Attachment.FileID != file.ID || result.Attachment.ChunkingStrategy.Type != "static" || result.Attachment.ChunkingStrategy.MaxChunkSizeTokens != 800 || result.Attachment.ChunkingStrategy.ChunkOverlapTokens != 200 {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	second := filestate.File{ID: "file_rag_rollback", OwnerKey: owner, Filename: "second.txt", Purpose: "assistants", ContentType: "text/plain", Bytes: 6, Content: []byte("second")}
	_, err = store.IngestRAG(ctx, ragstate.IngestRequest{
		OwnerKey: owner, File: &second, VectorStoreID: vectorStore.ID,
		ChunkingStrategy: vectorstate.AutoChunkingStrategy(),
		FileOwnerQuota:   100, VectorStoreQuota: 2, VectorStoreFiles: 1, VectorStoreBytes: 100,
	})
	if !errors.Is(err, vectorstate.ErrFileQuotaExceeded) {
		t.Fatalf("quota error=%v", err)
	}
	if _, err = store.Get(ctx, owner, second.ID, false); !errors.Is(err, filestate.ErrNotFound) {
		t.Fatalf("failed atomic ingest left file behind: %v", err)
	}
	third := filestate.File{ID: "file_rag_store_rollback", OwnerKey: owner, Filename: "third.txt", Purpose: "assistants", ContentType: "text/plain", Bytes: 5, Content: []byte("third")}
	secondStore := vectorstate.VectorStore{ID: "vs_rag_rollback", OwnerKey: owner, Name: "second", Metadata: map[string]string{}}
	_, err = store.IngestRAG(ctx, ragstate.IngestRequest{
		OwnerKey: owner, File: &third, VectorStore: &secondStore,
		ChunkingStrategy: vectorstate.AutoChunkingStrategy(),
		FileOwnerQuota:   100, VectorStoreQuota: 1, VectorStoreFiles: 2, VectorStoreBytes: 100,
	})
	if !errors.Is(err, vectorstate.ErrQuotaExceeded) {
		t.Fatalf("vector store quota error=%v", err)
	}
	if _, err = store.Get(ctx, owner, third.ID, false); !errors.Is(err, filestate.ErrNotFound) {
		t.Fatalf("failed store creation left file behind: %v", err)
	}
	if _, err = store.GetVectorStore(ctx, owner, secondStore.ID); !errors.Is(err, vectorstate.ErrNotFound) {
		t.Fatalf("failed store creation left vector store behind: %v", err)
	}
}
