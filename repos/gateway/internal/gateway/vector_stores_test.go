package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/vectorstate"
)

type memoryVectorStore struct {
	mu             sync.Mutex
	stores         map[string]vectorstate.VectorStore
	files          map[string]vectorstate.File
	availableFiles map[string]int64
	batches        map[string]vectorstate.FileBatch
	batchFiles     map[string][]string
}

func TestVectorStoreFileBatchHTTPLifecycleAtomicityAndIsolation(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memoryVectorStore{
		stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner, Name: "docs", Status: "completed"}},
		files:  map[string]vectorstate.File{}, availableFiles: map[string]int64{"file_a": 2, "file_b": 3, "file_c": 5},
	}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}}), modelsProvider{}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 10, ByteQuota: 100}))
	created := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/vs_owned/file_batches", `{"file_ids":["file_a","file_b"],"attributes":{"region":"eu"},"chunking_strategy":{"type":"auto"}}`)
	var payload struct {
		ID, Status string
		FileCounts map[string]int `json:"file_counts"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil || created.Code != http.StatusOK || payload.ID == "" || payload.Status != "completed" || payload.FileCounts["completed"] != 2 || payload.FileCounts["total"] != 2 {
		t.Fatalf("created status=%d payload=%+v err=%v body=%s", created.Code, payload, err, created.Body.String())
	}
	for _, path := range []string{
		"/v1/vector_stores/vs_owned/file_batches/" + payload.ID,
		"/v1/vector_stores/vs_owned/file_batches/" + payload.ID + "/cancel",
	} {
		method := http.MethodGet
		if strings.HasSuffix(path, "/cancel") {
			method = http.MethodPost
		}
		response := callVectorStore(handler, method, path, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"completed"`) {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	listed := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned/file_batches/"+payload.ID+"/files?limit=1&order=asc", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"file_a"`) || !strings.Contains(listed.Body.String(), `"region":"eu"`) || !strings.Contains(listed.Body.String(), `"has_more":true`) {
		t.Fatalf("listed status=%d body=%s", listed.Code, listed.Body.String())
	}
	other := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "other"}}), modelsProvider{}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 10, ByteQuota: 100}))
	if response := callVectorStore(other, http.MethodGet, "/v1/vector_stores/vs_owned/file_batches/"+payload.ID, ""); response.Code != http.StatusNotFound {
		t.Fatalf("cross-owner status=%d body=%s", response.Code, response.Body.String())
	}
	missing := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/vs_owned/file_batches", `{"file_ids":["file_c","missing"]}`)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
	if _, found := store.files["vs_owned/file_c"]; found {
		t.Fatal("partial attachment survived failed batch")
	}
	for _, body := range []string{
		`{"file_ids":["file_c","file_c"]}`,
		`{"file_ids":["file_c"],"files":[{"file_id":"file_c"}]}`,
		`{"files":[{"file_id":"file_c"}],"attributes":{"global":true}}`,
	} {
		if response := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/vs_owned/file_batches", body); response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	static := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/vs_owned/file_batches", `{"file_ids":["file_c"],"chunking_strategy":{"type":"static","static":{"max_chunk_size_tokens":800,"chunk_overlap_tokens":400}}}`)
	if static.Code != http.StatusUnprocessableEntity || !strings.Contains(static.Body.String(), `"vector_store_chunking_unsupported"`) {
		t.Fatalf("static status=%d body=%s", static.Code, static.Body.String())
	}
	perFile := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/vs_owned/file_batches", `{"files":[{"file_id":"file_c","attributes":{"region":"us"},"chunking_strategy":{"type":"auto"}}]}`)
	if perFile.Code != http.StatusOK || !strings.Contains(perFile.Body.String(), `"completed":1`) {
		t.Fatalf("per-file status=%d body=%s", perFile.Code, perFile.Body.String())
	}
}

func (s *memoryVectorStore) CreateVectorStoreFileBatch(_ context.Context, batch vectorstate.FileBatch, entries []vectorstate.FileBatchEntry, quota int, byteQuota int64) (vectorstate.FileBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch.ID == "" || batch.VectorStoreID == "" || batch.OwnerKey == "" || len(entries) < 1 || len(entries) > 2000 || quota < 1 || byteQuota < 1 {
		return vectorstate.FileBatch{}, vectorstate.ErrInvalid
	}
	store, ok := s.stores[batch.VectorStoreID]
	if !ok || store.OwnerKey != batch.OwnerKey {
		return vectorstate.FileBatch{}, vectorstate.ErrNotFound
	}
	if s.batches == nil {
		s.batches = map[string]vectorstate.FileBatch{}
	}
	if s.batchFiles == nil {
		s.batchFiles = map[string][]string{}
	}
	if _, found := s.batches[batch.ID]; found {
		return vectorstate.FileBatch{}, vectorstate.ErrConflict
	}
	count, used := 0, int64(0)
	for _, file := range s.files {
		if file.OwnerKey == batch.OwnerKey && file.VectorStoreID == batch.VectorStoreID {
			count++
			used += file.Bytes
		}
	}
	if count > quota || len(entries) > quota-count {
		return vectorstate.FileBatch{}, vectorstate.ErrFileQuotaExceeded
	}
	seen := map[string]struct{}{}
	added := int64(0)
	for _, entry := range entries {
		if entry.FileID == "" || vectorstate.ValidateAttributes(entry.Attributes) != "" {
			return vectorstate.FileBatch{}, vectorstate.ErrInvalid
		}
		size, found := s.availableFiles[entry.FileID]
		if !found {
			return vectorstate.FileBatch{}, vectorstate.ErrFileNotFound
		}
		if _, duplicate := seen[entry.FileID]; duplicate {
			return vectorstate.FileBatch{}, vectorstate.ErrConflict
		}
		seen[entry.FileID] = struct{}{}
		if _, attached := s.files[batch.VectorStoreID+"/"+entry.FileID]; attached {
			return vectorstate.FileBatch{}, vectorstate.ErrConflict
		}
		if size < 0 || added > byteQuota || size > byteQuota-added {
			return vectorstate.FileBatch{}, vectorstate.ErrByteQuotaExceeded
		}
		added += size
	}
	if used < 0 || used > byteQuota || added > byteQuota-used {
		return vectorstate.FileBatch{}, vectorstate.ErrByteQuotaExceeded
	}
	batch.Status, batch.Total, batch.Completed, batch.CreatedAt = "completed", len(entries), len(entries), time.Unix(300, 0).UTC()
	ids := make([]string, len(entries))
	for index, entry := range entries {
		ids[index] = entry.FileID
		s.files[batch.VectorStoreID+"/"+entry.FileID] = vectorstate.File{VectorStoreID: batch.VectorStoreID, FileID: entry.FileID, OwnerKey: batch.OwnerKey, Status: "completed", Bytes: s.availableFiles[entry.FileID], Attributes: normalizedVectorStoreAttributes(entry.Attributes), CreatedAt: time.Unix(300+int64(index), 0).UTC()}
	}
	s.batches[batch.ID], s.batchFiles[batch.ID] = batch, ids
	return batch, nil
}

func (s *memoryVectorStore) GetVectorStoreFileBatch(_ context.Context, owner, storeID, batchID string) (vectorstate.FileBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, found := s.batches[batchID]
	if !found || batch.OwnerKey != owner || batch.VectorStoreID != storeID {
		return vectorstate.FileBatch{}, vectorstate.ErrFileBatchNotFound
	}
	return batch, nil
}

func (s *memoryVectorStore) ListVectorStoreFileBatchFiles(ctx context.Context, owner, storeID, batchID string, options vectorstate.FileListOptions) ([]vectorstate.File, string, error) {
	s.mu.Lock()
	batch, found := s.batches[batchID]
	ids := append([]string(nil), s.batchFiles[batchID]...)
	selected := make(map[string]vectorstate.File, len(ids))
	for _, id := range ids {
		selected[storeID+"/"+id] = s.files[storeID+"/"+id]
	}
	store := s.stores[storeID]
	s.mu.Unlock()
	if !found || batch.OwnerKey != owner || batch.VectorStoreID != storeID {
		return nil, "", vectorstate.ErrFileBatchNotFound
	}
	temporary := &memoryVectorStore{stores: map[string]vectorstate.VectorStore{storeID: store}, files: selected}
	return temporary.ListVectorStoreFiles(ctx, owner, storeID, options)
}

func (s *memoryVectorStore) AttachVectorStoreFile(_ context.Context, owner, storeID, fileID string, attributes map[string]any, quota int, byteQuota int64) (vectorstate.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, ok := s.stores[storeID]
	if !ok || store.OwnerKey != owner {
		return vectorstate.File{}, vectorstate.ErrNotFound
	}
	bytes, ok := s.availableFiles[fileID]
	if !ok {
		return vectorstate.File{}, vectorstate.ErrFileNotFound
	}
	key := storeID + "/" + fileID
	if _, ok := s.files[key]; ok {
		return vectorstate.File{}, vectorstate.ErrConflict
	}
	count, usedBytes := 0, int64(0)
	for _, file := range s.files {
		if file.OwnerKey == owner && file.VectorStoreID == storeID {
			count++
			usedBytes += file.Bytes
		}
	}
	if count >= quota {
		return vectorstate.File{}, vectorstate.ErrFileQuotaExceeded
	}
	if usedBytes < 0 || bytes < 0 || usedBytes > byteQuota || bytes > byteQuota-usedBytes {
		return vectorstate.File{}, vectorstate.ErrByteQuotaExceeded
	}
	file := vectorstate.File{VectorStoreID: storeID, FileID: fileID, OwnerKey: owner, Status: "completed", Bytes: bytes, Attributes: normalizedVectorStoreAttributes(attributes), CreatedAt: time.Unix(200+int64(count), 0).UTC()}
	s.files[key] = file
	return file, nil
}

func (s *memoryVectorStore) ListVectorStoreFiles(_ context.Context, owner, storeID string, options vectorstate.FileListOptions) ([]vectorstate.File, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !options.Valid() {
		return nil, "", vectorstate.ErrInvalid
	}
	store, ok := s.stores[storeID]
	if !ok || store.OwnerKey != owner {
		return nil, "", vectorstate.ErrNotFound
	}
	files := make([]vectorstate.File, 0)
	for _, file := range s.files {
		if file.OwnerKey == owner && file.VectorStoreID == storeID && (options.Status == "" || file.Status == options.Status) {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].CreatedAt.Equal(files[j].CreatedAt) {
			if options.Order == "asc" {
				return files[i].FileID < files[j].FileID
			}
			return files[i].FileID > files[j].FileID
		}
		if options.Order == "asc" {
			return files[i].CreatedAt.Before(files[j].CreatedAt)
		}
		return files[i].CreatedAt.After(files[j].CreatedAt)
	})
	cursor := options.After
	if options.Before != "" {
		cursor = options.Before
	}
	if cursor != "" {
		position := -1
		for index := range files {
			if files[index].FileID == cursor {
				position = index
				break
			}
		}
		if position < 0 {
			return nil, "", vectorstate.ErrFileNotFound
		}
		if options.Before != "" {
			files = files[:position]
			if len(files) > options.Limit+1 {
				files = files[len(files)-(options.Limit+1):]
			}
		} else {
			files = files[position+1:]
		}
	}
	next := ""
	if len(files) > options.Limit {
		if options.Before != "" {
			next = files[1].FileID
			files = files[1:]
		} else {
			next = files[options.Limit-1].FileID
			files = files[:options.Limit]
		}
	}
	return files, next, nil
}

func (s *memoryVectorStore) GetVectorStoreFile(_ context.Context, owner, storeID, fileID string) (vectorstate.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.files[storeID+"/"+fileID]
	if !ok || file.OwnerKey != owner {
		return vectorstate.File{}, vectorstate.ErrFileNotFound
	}
	return file, nil
}

func (s *memoryVectorStore) UpdateVectorStoreFile(_ context.Context, owner, storeID, fileID string, attributes map[string]any) (vectorstate.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := storeID + "/" + fileID
	file, ok := s.files[key]
	if !ok || file.OwnerKey != owner {
		return vectorstate.File{}, vectorstate.ErrFileNotFound
	}
	file.Attributes = normalizedVectorStoreAttributes(attributes)
	s.files[key] = file
	return file, nil
}

func (s *memoryVectorStore) DeleteVectorStoreFile(_ context.Context, owner, storeID, fileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := storeID + "/" + fileID
	file, ok := s.files[key]
	if !ok || file.OwnerKey != owner {
		return vectorstate.ErrFileNotFound
	}
	delete(s.files, key)
	return nil
}

func (s *memoryVectorStore) CreateVectorStore(_ context.Context, store vectorstate.VectorStore, quota int) (vectorstate.VectorStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, existing := range s.stores {
		if existing.OwnerKey == store.OwnerKey {
			count++
		}
	}
	if count >= quota {
		return vectorstate.VectorStore{}, vectorstate.ErrQuotaExceeded
	}
	if _, exists := s.stores[store.ID]; exists {
		return vectorstate.VectorStore{}, vectorstate.ErrConflict
	}
	store.Status = "completed"
	store.CreatedAt = time.Unix(100, 0).UTC()
	store.LastActiveAt = store.CreatedAt
	if store.ExpiresAfter > 0 {
		expires := store.LastActiveAt.Add(time.Duration(store.ExpiresAfter) * 24 * time.Hour)
		store.ExpiresAt = &expires
	}
	s.stores[store.ID] = store
	return store, nil
}

func (s *memoryVectorStore) ListVectorStores(_ context.Context, owner string, limit int, after string) ([]vectorstate.VectorStore, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]vectorstate.VectorStore, 0, len(s.stores))
	for _, store := range s.stores {
		if store.OwnerKey == owner {
			for _, file := range s.files {
				if file.OwnerKey == owner && file.VectorStoreID == store.ID {
					store.FileCount++
					store.UsageBytes += file.Bytes
				}
			}
			values = append(values, store)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID > values[j].ID })
	start := 0
	if after != "" {
		start = -1
		for index := range values {
			if values[index].ID == after {
				start = index + 1
			}
		}
		if start < 0 {
			return nil, "", vectorstate.ErrNotFound
		}
	}
	values = values[start:]
	next := ""
	if len(values) > limit {
		next = values[limit-1].ID
		values = values[:limit]
	}
	return values, next, nil
}

func (s *memoryVectorStore) GetVectorStore(_ context.Context, owner, id string) (vectorstate.VectorStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, exists := s.stores[id]
	if !exists || store.OwnerKey != owner {
		return vectorstate.VectorStore{}, vectorstate.ErrNotFound
	}
	for _, file := range s.files {
		if file.OwnerKey == owner && file.VectorStoreID == id {
			store.FileCount++
			store.UsageBytes += file.Bytes
		}
	}
	return store, nil
}

func (s *memoryVectorStore) UpdateVectorStore(_ context.Context, owner, id string, update vectorstate.Update) (vectorstate.VectorStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, exists := s.stores[id]
	if !exists || store.OwnerKey != owner {
		return vectorstate.VectorStore{}, vectorstate.ErrNotFound
	}
	if update.Name != nil {
		store.Name = *update.Name
	}
	if update.Metadata != nil {
		store.Metadata = *update.Metadata
	}
	if update.ExpiresAfter != nil {
		store.ExpiresAfter = *update.ExpiresAfter
		expires := store.LastActiveAt.Add(time.Duration(*update.ExpiresAfter) * 24 * time.Hour)
		store.ExpiresAt = &expires
	}
	s.stores[id] = store
	return store, nil
}

func (s *memoryVectorStore) DeleteVectorStore(_ context.Context, owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, exists := s.stores[id]
	if !exists || store.OwnerKey != owner {
		return vectorstate.ErrNotFound
	}
	delete(s.stores, id)
	return nil
}

func callVectorStore(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer key")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestVectorStoreHTTPLifecycleAndIsolation(t *testing.T) {
	store := &memoryVectorStore{stores: map[string]vectorstate.VectorStore{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}}), modelsProvider{}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10}))
	created := callVectorStore(handler, http.MethodPost, "/v1/vector_stores", `{"name":"Support docs","expires_after":{"anchor":"last_active_at","days":30},"metadata":{"team":"help"}}`)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"object":"vector_store"`) || !strings.Contains(created.Body.String(), `"days":30`) {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var id string
	for storedID := range store.stores {
		id = storedID
	}
	if !strings.HasPrefix(id, "vs_") {
		t.Fatalf("id=%q", id)
	}
	other := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "other"}}), modelsProvider{}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10}))
	if response := callVectorStore(other, http.MethodGet, "/v1/vector_stores/"+id, ""); response.Code != http.StatusNotFound {
		t.Fatalf("cross-owner get status=%d body=%s", response.Code, response.Body.String())
	}
	updated := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/"+id, `{"name":"Current docs","metadata":{}}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"name":"Current docs"`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	listed := callVectorStore(handler, http.MethodGet, "/v1/vector_stores?limit=1", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), id) || !strings.Contains(listed.Body.String(), `"has_more":false`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	deleted := callVectorStore(handler, http.MethodDelete, "/v1/vector_stores/"+id, "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestVectorStoreFileHTTPLifecyclePaginationAndIsolation(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memoryVectorStore{
		stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner, Name: "docs", Status: "completed"}},
		files:  map[string]vectorstate.File{}, availableFiles: map[string]int64{"file_one": 11, "file_two": 22},
	}
	contents := &memoryFileStore{files: map[string]filestate.File{
		"file_one": {ID: "file_one", OwnerKey: owner, Filename: "one.md", Purpose: "assistants", ContentType: "text/markdown", Bytes: 11, Content: []byte("first chunk")},
	}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}}), modelsProvider{}).
		WithFileStore(contents, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 2, ByteQuota: 100}))
	for _, fileID := range []string{"file_one", "file_two"} {
		body := `{"file_id":"` + fileID + `"}`
		if fileID == "file_one" {
			body = `{"file_id":"file_one","attributes":{"region":"eu","priority":2,"active":true},"chunking_strategy":{"type":"auto"}}`
		}
		response := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/vs_owned/files", body)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"object":"vector_store.file"`) || !strings.Contains(response.Body.String(), fileID) {
			t.Fatalf("attach %s status=%d body=%s", fileID, response.Code, response.Body.String())
		}
		if fileID == "file_one" && (!strings.Contains(response.Body.String(), `"priority":2`) || !strings.Contains(response.Body.String(), `"active":true`) || !strings.Contains(response.Body.String(), `"chunking_strategy":{"type":"auto"}`)) {
			t.Fatalf("attach attributes body=%s", response.Body.String())
		}
	}
	listed := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned/files?limit=1", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"has_more":true`) || !strings.Contains(listed.Body.String(), `"file_two"`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	ascending := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned/files?limit=1&order=asc", "")
	if ascending.Code != http.StatusOK || !strings.Contains(ascending.Body.String(), `"file_one"`) || !strings.Contains(ascending.Body.String(), `"has_more":true`) {
		t.Fatalf("ascending status=%d body=%s", ascending.Code, ascending.Body.String())
	}
	after := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned/files?limit=1&after=file_two&order=desc", "")
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"file_one"`) || !strings.Contains(after.Body.String(), `"has_more":false`) {
		t.Fatalf("after status=%d body=%s", after.Code, after.Body.String())
	}
	before := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned/files?limit=1&before=file_one&order=desc", "")
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), `"file_two"`) || !strings.Contains(before.Body.String(), `"has_more":false`) {
		t.Fatalf("before status=%d body=%s", before.Code, before.Body.String())
	}
	failed := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned/files?filter=failed", "")
	if failed.Code != http.StatusOK || !strings.Contains(failed.Body.String(), `"data":[]`) {
		t.Fatalf("failed filter status=%d body=%s", failed.Code, failed.Body.String())
	}
	got := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned/files/file_one", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"usage_bytes":11`) || !strings.Contains(got.Body.String(), `"priority":2`) || !strings.Contains(got.Body.String(), `"active":true`) {
		t.Fatalf("get status=%d body=%s", got.Code, got.Body.String())
	}
	updated := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/vs_owned/files/file_one", `{"attributes":{"region":"us","priority":3.5,"active":false}}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"region":"us"`) || !strings.Contains(updated.Body.String(), `"priority":3.5`) || !strings.Contains(updated.Body.String(), `"active":false`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	got = callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned/files/file_one", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"region":"us"`) || !strings.Contains(got.Body.String(), `"priority":3.5`) || !strings.Contains(got.Body.String(), `"active":false`) {
		t.Fatalf("updated get status=%d body=%s", got.Code, got.Body.String())
	}
	content := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned/files/file_one/content", "")
	if content.Code != http.StatusOK || !strings.Contains(content.Body.String(), `"filename":"one.md"`) || !strings.Contains(content.Body.String(), `"text":"first chunk"`) || !strings.Contains(content.Body.String(), `"priority":3.5`) {
		t.Fatalf("content status=%d body=%s", content.Code, content.Body.String())
	}
	parent := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned", "")
	if parent.Code != http.StatusOK || !strings.Contains(parent.Body.String(), `"usage_bytes":33`) || !strings.Contains(parent.Body.String(), `"completed":2`) || !strings.Contains(parent.Body.String(), `"total":2`) {
		t.Fatalf("parent totals status=%d body=%s", parent.Code, parent.Body.String())
	}
	other := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "other"}}), modelsProvider{}).
		WithFileStore(contents, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 2, ByteQuota: 100}))
	if response := callVectorStore(other, http.MethodGet, "/v1/vector_stores/vs_owned/files/file_one", ""); response.Code != http.StatusNotFound {
		t.Fatalf("cross-owner get status=%d body=%s", response.Code, response.Body.String())
	}
	if response := callVectorStore(other, http.MethodGet, "/v1/vector_stores/vs_owned/files/file_one/content", ""); response.Code != http.StatusNotFound {
		t.Fatalf("cross-owner content status=%d body=%s", response.Code, response.Body.String())
	}
	if response := callVectorStore(other, http.MethodPost, "/v1/vector_stores/vs_owned/files/file_one", `{"attributes":{}}`); response.Code != http.StatusNotFound {
		t.Fatalf("cross-owner update status=%d body=%s", response.Code, response.Body.String())
	}
	deleted := callVectorStore(handler, http.MethodDelete, "/v1/vector_stores/vs_owned/files/file_one", "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if response := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned/files/file_one", ""); response.Code != http.StatusNotFound {
		t.Fatalf("deleted get status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestVectorStoreFileContentRejectsUnavailableAndUnsearchableFiles(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memoryVectorStore{
		stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner, Name: "docs", Status: "completed"}},
		files: map[string]vectorstate.File{
			"vs_owned/binary":  {VectorStoreID: "vs_owned", FileID: "binary", OwnerKey: owner, Status: "completed"},
			"vs_owned/empty":   {VectorStoreID: "vs_owned", FileID: "empty", OwnerKey: owner, Status: "completed"},
			"vs_owned/missing": {VectorStoreID: "vs_owned", FileID: "missing", OwnerKey: owner, Status: "completed"},
			"vs_owned/large":   {VectorStoreID: "vs_owned", FileID: "large", OwnerKey: owner, Status: "completed"},
		},
	}
	files := &memoryFileStore{files: map[string]filestate.File{
		"binary": {ID: "binary", OwnerKey: owner, Filename: "binary.bin", Purpose: "assistants", ContentType: "application/octet-stream", Content: []byte{1, 2, 3}},
		"empty":  {ID: "empty", OwnerKey: owner, Filename: "empty.txt", Purpose: "assistants", ContentType: "text/plain", Content: []byte(" \n\t")},
		"large":  {ID: "large", OwnerKey: owner, Filename: "large.txt", Purpose: "assistants", ContentType: "text/plain", Bytes: maxVectorSearchBytes + 1, Content: []byte("must not be loaded")},
	}}
	pipeline := modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}})
	configured := Routes(NewHandler(pipeline, modelsProvider{}).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: maxVectorSearchBytes, OwnerQuotaBytes: maxVectorSearchBytes}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 10, ByteQuota: maxVectorSearchBytes}))
	for _, test := range []struct {
		fileID string
		code   int
	}{
		{"binary", http.StatusUnprocessableEntity},
		{"empty", http.StatusUnprocessableEntity},
		{"missing", http.StatusUnprocessableEntity},
		{"large", http.StatusUnprocessableEntity},
	} {
		response := callVectorStore(configured, http.MethodGet, "/v1/vector_stores/vs_owned/files/"+test.fileID+"/content", "")
		if response.Code != test.code {
			t.Fatalf("file=%s status=%d body=%s", test.fileID, response.Code, response.Body.String())
		}
	}

	unavailable := Routes(NewHandler(pipeline, modelsProvider{}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 10, ByteQuota: maxVectorSearchBytes}))
	response := callVectorStore(unavailable, http.MethodGet, "/v1/vector_stores/vs_owned/files/binary/content", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestVectorStoreFilesRejectInvalidMissingDuplicateAndQuota(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memoryVectorStore{
		stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner, Name: "docs", Status: "completed"}},
		files:  map[string]vectorstate.File{}, availableFiles: map[string]int64{"file_one": 1, "file_two": 2},
	}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}}), modelsProvider{}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 1, ByteQuota: 100}))
	for _, path := range []string{
		"/v1/vector_stores/vs_owned/files?after=file_one&before=file_two",
		"/v1/vector_stores/vs_owned/files?before=bad%2Fid",
		"/v1/vector_stores/vs_owned/files?order=newest",
		"/v1/vector_stores/vs_owned/files?filter=unknown",
		"/v1/vector_stores/vs_owned/files?order=asc&order=desc",
	} {
		response := callVectorStore(handler, http.MethodGet, path, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	for _, test := range []struct {
		path, body string
		code       int
	}{
		{"/v1/vector_stores/vs_owned/files", `{}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files", `{"file_id":"missing"}`, http.StatusNotFound},
		{"/v1/vector_stores/vs_owned/files", `{"file_id":"file_one","attributes":{"too_long":"` + strings.Repeat("x", 513) + `"}}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files", `{"file_id":"file_one","attributes":{"nested":{"bad":true}}}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files", `{"file_id":"file_one","chunking_strategy":{"type":"auto","static":{"max_chunk_size_tokens":800,"chunk_overlap_tokens":400}}}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files", `{"file_id":"file_one","chunking_strategy":{"type":"static","static":{"max_chunk_size_tokens":99,"chunk_overlap_tokens":0}}}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files", `{"file_id":"file_one","chunking_strategy":{"type":"static","static":{"max_chunk_size_tokens":800,"chunk_overlap_tokens":401}}}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files", `{"file_id":"file_one","chunking_strategy":{"type":"static","static":{"max_chunk_size_tokens":800,"chunk_overlap_tokens":400}}}`, http.StatusUnprocessableEntity},
		{"/v1/vector_stores/vs_owned/files", `{"file_id":"file_one","chunking_strategy":{"type":"unknown"}}`, http.StatusBadRequest},
		{"/v1/vector_stores/missing/files", `{"file_id":"file_one"}`, http.StatusNotFound},
		{"/v1/vector_stores/vs_owned/files?extra=1", `{"file_id":"file_one"}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files/file_one", `{}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files/file_one", `{"attributes":null}`, http.StatusBadRequest},
	} {
		response := callVectorStore(handler, http.MethodPost, test.path, test.body)
		if response.Code != test.code {
			t.Fatalf("path=%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
		if test.code == http.StatusUnprocessableEntity && !strings.Contains(response.Body.String(), `"code":"vector_store_chunking_unsupported"`) {
			t.Fatalf("path=%s missing capability error body=%s", test.path, response.Body.String())
		}
	}
	if response := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/vs_owned/files", `{"file_id":"file_one"}`); response.Code != http.StatusOK {
		t.Fatalf("first attach status=%d body=%s", response.Code, response.Body.String())
	}
	if response := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/vs_owned/files", `{"file_id":"file_one"}`); response.Code != http.StatusConflict {
		t.Fatalf("duplicate status=%d body=%s", response.Code, response.Body.String())
	}
	if response := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/vs_owned/files", `{"file_id":"file_two"}`); response.Code != http.StatusTooManyRequests {
		t.Fatalf("quota status=%d body=%s", response.Code, response.Body.String())
	}
	byteStore := &memoryVectorStore{
		stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner, Name: "docs", Status: "completed"}},
		files:  map[string]vectorstate.File{}, availableFiles: map[string]int64{"file_two": 2},
	}
	byteHandler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}}), modelsProvider{}).
		WithVectorStore(byteStore, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 2, ByteQuota: 1}))
	response := callVectorStore(byteHandler, http.MethodPost, "/v1/vector_stores/vs_owned/files", `{"file_id":"file_two"}`)
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), `"code":"vector_store_byte_quota_exceeded"`) {
		t.Fatalf("byte quota status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestVectorStoresRejectInvalidRequestsAndEnforceQuota(t *testing.T) {
	store := &memoryVectorStore{stores: map[string]vectorstate.VectorStore{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}}), modelsProvider{}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 1}))
	for _, body := range []string{
		`{}`, `{"name":" padded "}`, `{"name":"x","unknown":true}`,
		`{"name":"x","expires_after":{"anchor":"created_at","days":1}}`,
		`{"name":"x","expires_after":{"anchor":"last_active_at","days":366}}`,
	} {
		response := callVectorStore(handler, http.MethodPost, "/v1/vector_stores", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	if response := callVectorStore(handler, http.MethodPost, "/v1/vector_stores", `{"name":"one"}`); response.Code != http.StatusOK {
		t.Fatalf("first create status=%d body=%s", response.Code, response.Body.String())
	}
	if response := callVectorStore(handler, http.MethodPost, "/v1/vector_stores", `{"name":"two"}`); response.Code != http.StatusTooManyRequests {
		t.Fatalf("quota status=%d body=%s", response.Code, response.Body.String())
	}
	if response := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/missing", `{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("empty update status=%d body=%s", response.Code, response.Body.String())
	}
	missing := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}}), modelsProvider{}))
	if response := callVectorStore(missing, http.MethodGet, "/v1/vector_stores", ""); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing store status=%d body=%s", response.Code, response.Body.String())
	}
	unauthorized := httptest.NewRecorder()
	Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{})).ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/vector_stores", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
}

func TestVectorStoreMemoryStoreContract(t *testing.T) {
	store := &memoryVectorStore{stores: map[string]vectorstate.VectorStore{}}
	if _, err := store.GetVectorStore(t.Context(), "owner", "missing"); !errors.Is(err, vectorstate.ErrNotFound) {
		t.Fatalf("error=%v", err)
	}
}
