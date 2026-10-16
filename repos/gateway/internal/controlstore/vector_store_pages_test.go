package controlstore

import (
	"testing"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/vectorstate"
)

func TestPostgresVectorStoreDirectoryPagingSearchAndIsolationIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	pool := prepareVectorStoreTable(t, t.Context(), dsn)
	t.Cleanup(pool.Close)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	const owner = "directory-owner"
	if _, err = pool.Exec(t.Context(), `INSERT INTO gateway_vector_stores(id,owner_key,name,metadata)
	 SELECT 'vs_page_'||lpad(i::text,3,'0'),$1,'Store '||lpad(i::text,3,'0'),'{}'::jsonb FROM generate_series(1,130)i`, owner); err != nil {
		t.Fatal(err)
	}
	q := vectorstate.PageQuery{Offset: 100, Limit: 25, SortBy: "created_at", SortOrder: "asc"}
	page, total, err := store.ListVectorStorePage(t.Context(), owner, q)
	if err != nil || total != 130 || len(page) != 25 || page[0].ID != "vs_page_101" || page[24].ID != "vs_page_125" {
		t.Fatalf("late stable page: total=%d rows=%d err=%v", total, len(page), err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE gateway_vector_stores SET name='Needle %_' WHERE id='vs_page_127';
	 UPDATE gateway_vector_stores SET expires_at=now()-interval '1 second' WHERE id='vs_page_130';
	 INSERT INTO gateway_vector_stores(id,owner_key,name,metadata) VALUES('vs_foreign','another-owner','Needle %_','{}');`); err != nil {
		t.Fatal(err)
	}
	q.Offset, q.Search = 0, "%_"
	page, total, err = store.ListVectorStorePage(t.Context(), owner, q)
	if err != nil || total != 1 || len(page) != 1 || page[0].ID != "vs_page_127" {
		t.Fatal("literal search or owner isolation failed", total, err)
	}
	q.Search, q.Status = "", "expired"
	page, total, err = store.ListVectorStorePage(t.Context(), owner, q)
	if err != nil || total != 1 || len(page) != 1 || page[0].Status != "expired" {
		t.Fatal("expiry filter failed", total, err)
	}
	q.Status, q.Offset = "completed", 200
	page, total, err = store.ListVectorStorePage(t.Context(), owner, q)
	if err != nil || total != 129 || len(page) != 0 {
		t.Fatal("empty page lost exact active count", total, err)
	}
	q.Offset, q.Search = 0, "NEEDLE"
	if _, err = store.Create(t.Context(), filestate.File{ID: "file_page", OwnerKey: owner, Filename: "page.txt", Purpose: "assistants", Bytes: 4, Content: []byte("text"), ContentType: "text/plain"}, 100); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AttachVectorStoreFile(t.Context(), owner, "vs_page_127", "file_page", nil, vectorstate.AutoChunkingStrategy(), 100, 100); err != nil {
		t.Fatal(err)
	}
	page, total, err = store.ListVectorStorePage(t.Context(), owner, q)
	if err != nil || total != 1 || len(page) != 1 || page[0].FileCount != 1 || page[0].UsageBytes != 4 {
		t.Fatal("file inventory summary incorrect", total, err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE gateway_files SET expires_at=now()-interval '1 second' WHERE id='file_page'`); err != nil {
		t.Fatal(err)
	}
	page, _, err = store.ListVectorStorePage(t.Context(), owner, q)
	if err != nil || len(page) != 1 || page[0].FileCount != 0 || page[0].UsageBytes != 0 {
		t.Fatal("expired file inflated directory summary", err)
	}
	page, total, err = store.ListVectorStorePage(t.Context(), "unknown-owner", q)
	if err != nil || total != 0 || len(page) != 0 {
		t.Fatal("unknown owner received another directory", total, err)
	}
}
