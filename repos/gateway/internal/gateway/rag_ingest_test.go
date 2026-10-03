package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/ragstate"
	"ai-gateway-gateway/internal/vectorstate"
)

type ragIngestRecorder struct {
	*memoryVectorStore
	request ragstate.IngestRequest
	calls   int
	err     error
}

func (s *ragIngestRecorder) IngestRAG(_ context.Context, request ragstate.IngestRequest) (ragstate.IngestResult, error) {
	s.calls++
	s.request = request
	if s.err != nil {
		return ragstate.IngestResult{}, s.err
	}
	now := time.Unix(10, 0)
	file := filestate.File{ID: request.FileID, OwnerKey: request.OwnerKey, Filename: "existing.txt", Purpose: "assistants", ContentType: "text/plain", Bytes: 8, CreatedAt: now}
	if request.File != nil {
		file = *request.File
		file.CreatedAt = now
		file.Content = nil
	}
	store := vectorstate.VectorStore{ID: request.VectorStoreID, OwnerKey: request.OwnerKey, Name: "existing", Metadata: map[string]string{}, Status: "completed", CreatedAt: now, LastActiveAt: now, FileCount: 1, UsageBytes: file.Bytes}
	if request.VectorStore != nil {
		store = *request.VectorStore
		store.Status = "completed"
		store.CreatedAt = now
		store.LastActiveAt = now
		store.FileCount = 1
		store.UsageBytes = file.Bytes
	}
	attachment := vectorstate.File{VectorStoreID: store.ID, FileID: file.ID, OwnerKey: request.OwnerKey, Status: "completed", Bytes: file.Bytes, Attributes: request.Attributes, ChunkingStrategy: request.ChunkingStrategy, CreatedAt: now}
	return ragstate.IngestResult{File: file, VectorStore: store, Attachment: attachment}, nil
}

func newRAGIngestHandler(user string, files *memoryFileStore, store *ragIngestRecorder) http.Handler {
	auth := &ragQueryAuthModule{user: user}
	return Routes(NewHandler(modules.NewPipeline([]modules.Module{auth}), modelsProvider{}).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: maxVectorSearchBytes, OwnerQuotaBytes: 4 * maxVectorSearchBytes}).
		WithRAGIngestStore(store).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 4, FileQuota: 10, ByteQuota: 4 * maxVectorSearchBytes}))
}

func TestRAGIngestAtomicallyCreatesInlineFileStoreAndAttachment(t *testing.T) {
	files := &memoryFileStore{files: map[string]filestate.File{}}
	store := &ragIngestRecorder{memoryVectorStore: &memoryVectorStore{stores: map[string]vectorstate.VectorStore{}, files: map[string]vectorstate.File{}}}
	handler := newRAGIngestHandler("user", files, store)
	body := `{"file":{"filename":"guide.md","content":"IyBHdWlkZQ==","content_type":"text/markdown"},"vector_store":{"name":"Guides","metadata":{"region":"eu"}},"attributes":{"category":"manual"},"chunking_strategy":{"type":"static","static":{"max_chunk_size_tokens":800,"chunk_overlap_tokens":200}}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/rag/ingest", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || store.calls != 1 || !strings.Contains(response.Body.String(), `"object":"rag.ingest"`) || !strings.Contains(response.Body.String(), `"filename":"guide.md"`) {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, store.calls, response.Body.String())
	}
	if store.request.File == nil || store.request.File.ID == "" || string(store.request.File.Content) != "# Guide" || store.request.File.Purpose != "assistants" || store.request.VectorStore == nil || store.request.VectorStore.ID == "" || store.request.VectorStore.Metadata["region"] != "eu" || store.request.Attributes["category"] != "manual" {
		t.Fatalf("request=%+v", store.request)
	}
	if strategy := store.request.ChunkingStrategy; strategy.Type != "static" || strategy.MaxChunkSizeTokens != 800 || strategy.ChunkOverlapTokens != 200 || !strings.Contains(response.Body.String(), `"chunking_strategy":{"static":{"chunk_overlap_tokens":200,"max_chunk_size_tokens":800},"type":"static"}`) {
		t.Fatalf("chunking strategy=%+v body=%s", strategy, response.Body.String())
	}
}

func TestRAGIngestUsesOwnedExistingResources(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	files := &memoryFileStore{files: map[string]filestate.File{
		"file_owned": {ID: "file_owned", OwnerKey: owner, Filename: "owned.txt", Purpose: "assistants", ContentType: "text/plain", Bytes: 5, Content: []byte("owned")},
	}}
	store := &ragIngestRecorder{memoryVectorStore: &memoryVectorStore{stores: map[string]vectorstate.VectorStore{}, files: map[string]vectorstate.File{}}}
	request := httptest.NewRequest(http.MethodPost, "/v1/rag/ingest", strings.NewReader(`{"file_id":"file_owned","vector_store_id":"vs_owned"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	newRAGIngestHandler("user", files, store).ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.calls != 1 || store.request.FileID != "file_owned" || store.request.VectorStoreID != "vs_owned" || store.request.OwnerKey != owner {
		t.Fatalf("status=%d calls=%d request=%+v body=%s", response.Code, store.calls, store.request, response.Body.String())
	}

	otherRequest := httptest.NewRequest(http.MethodPost, "/v1/rag/ingest", strings.NewReader(`{"file_id":"file_owned","vector_store_id":"vs_owned"}`))
	otherRequest.Header.Set("Authorization", "Bearer key")
	otherResponse := httptest.NewRecorder()
	newRAGIngestHandler("other", files, store).ServeHTTP(otherResponse, otherRequest)
	if otherResponse.Code != http.StatusNotFound || store.calls != 1 {
		t.Fatalf("cross-owner status=%d calls=%d body=%s", otherResponse.Code, store.calls, otherResponse.Body.String())
	}
}

func TestRAGIngestRejectsUnsafeOrAmbiguousInputBeforeStorage(t *testing.T) {
	for _, body := range []string{
		`{"file_id":"file_a","file":{"filename":"a.txt","content":"YQ=="},"vector_store_id":"vs_a"}`,
		`{"file":{"filename":"bad.json","content":"bm90LWpzb24=","content_type":"application/json"},"vector_store_id":"vs_a"}`,
		`{"file":{"filename":"bad.bin","content":"AAE=","content_type":"application/octet-stream"},"vector_store_id":"vs_a"}`,
		`{"file":{"filename":"a.txt","content":"YQ=="},"vector_store":{"name":" store "}}`,
		`{"file":{"filename":"a.txt","content":"%%%"},"vector_store_id":"vs_a"}`,
	} {
		files := &memoryFileStore{files: map[string]filestate.File{}}
		store := &ragIngestRecorder{memoryVectorStore: &memoryVectorStore{stores: map[string]vectorstate.VectorStore{}, files: map[string]vectorstate.File{}}}
		request := httptest.NewRequest(http.MethodPost, "/v1/rag/ingest", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer key")
		response := httptest.NewRecorder()
		newRAGIngestHandler("user", files, store).ServeHTTP(response, request)
		if response.Code < 400 || response.Code >= 500 || store.calls != 0 {
			t.Fatalf("body=%s status=%d calls=%d response=%s", body, response.Code, store.calls, response.Body.String())
		}
	}
}
