package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/vectorstate"
)

type memoryVectorStore struct {
	mu             sync.Mutex
	stores         map[string]vectorstate.VectorStore
	files          map[string]vectorstate.File
	availableFiles map[string]int64
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

func (s *memoryVectorStore) ListVectorStoreFiles(_ context.Context, owner, storeID string, limit int, after string) ([]vectorstate.File, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, ok := s.stores[storeID]
	if !ok || store.OwnerKey != owner {
		return nil, "", vectorstate.ErrNotFound
	}
	files := make([]vectorstate.File, 0)
	for _, file := range s.files {
		if file.OwnerKey == owner && file.VectorStoreID == storeID {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].FileID > files[j].FileID })
	start := 0
	if after != "" {
		start = -1
		for index := range files {
			if files[index].FileID == after {
				start = index + 1
			}
		}
		if start < 0 {
			return nil, "", vectorstate.ErrFileNotFound
		}
	}
	files = files[start:]
	next := ""
	if len(files) > limit {
		next = files[limit-1].FileID
		files = files[:limit]
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
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}}), modelsProvider{}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 2, ByteQuota: 100}))
	for _, fileID := range []string{"file_one", "file_two"} {
		body := `{"file_id":"` + fileID + `"}`
		if fileID == "file_one" {
			body = `{"file_id":"file_one","attributes":{"region":"eu","priority":2,"active":true}}`
		}
		response := callVectorStore(handler, http.MethodPost, "/v1/vector_stores/vs_owned/files", body)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"object":"vector_store.file"`) || !strings.Contains(response.Body.String(), fileID) {
			t.Fatalf("attach %s status=%d body=%s", fileID, response.Code, response.Body.String())
		}
		if fileID == "file_one" && (!strings.Contains(response.Body.String(), `"priority":2`) || !strings.Contains(response.Body.String(), `"active":true`)) {
			t.Fatalf("attach attributes body=%s", response.Body.String())
		}
	}
	listed := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned/files?limit=1", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"has_more":true`) || !strings.Contains(listed.Body.String(), `"file_two"`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
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
	parent := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/vs_owned", "")
	if parent.Code != http.StatusOK || !strings.Contains(parent.Body.String(), `"usage_bytes":33`) || !strings.Contains(parent.Body.String(), `"completed":2`) || !strings.Contains(parent.Body.String(), `"total":2`) {
		t.Fatalf("parent totals status=%d body=%s", parent.Code, parent.Body.String())
	}
	other := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "other"}}), modelsProvider{}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 2, ByteQuota: 100}))
	if response := callVectorStore(other, http.MethodGet, "/v1/vector_stores/vs_owned/files/file_one", ""); response.Code != http.StatusNotFound {
		t.Fatalf("cross-owner get status=%d body=%s", response.Code, response.Body.String())
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

func TestVectorStoreFilesRejectInvalidMissingDuplicateAndQuota(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memoryVectorStore{
		stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner, Name: "docs", Status: "completed"}},
		files:  map[string]vectorstate.File{}, availableFiles: map[string]int64{"file_one": 1, "file_two": 2},
	}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "user"}}), modelsProvider{}).
		WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 1, ByteQuota: 100}))
	for _, test := range []struct {
		path, body string
		code       int
	}{
		{"/v1/vector_stores/vs_owned/files", `{}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files", `{"file_id":"missing"}`, http.StatusNotFound},
		{"/v1/vector_stores/vs_owned/files", `{"file_id":"file_one","attributes":{"too_long":"` + strings.Repeat("x", 513) + `"}}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files", `{"file_id":"file_one","attributes":{"nested":{"bad":true}}}`, http.StatusBadRequest},
		{"/v1/vector_stores/missing/files", `{"file_id":"file_one"}`, http.StatusNotFound},
		{"/v1/vector_stores/vs_owned/files?extra=1", `{"file_id":"file_one"}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files/file_one", `{}`, http.StatusBadRequest},
		{"/v1/vector_stores/vs_owned/files/file_one", `{"attributes":null}`, http.StatusBadRequest},
	} {
		response := callVectorStore(handler, http.MethodPost, test.path, test.body)
		if response.Code != test.code {
			t.Fatalf("path=%s status=%d body=%s", test.path, response.Code, response.Body.String())
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
