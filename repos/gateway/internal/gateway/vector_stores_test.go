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
	mu     sync.Mutex
	stores map[string]vectorstate.VectorStore
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
