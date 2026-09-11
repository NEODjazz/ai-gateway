package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/assistantstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/vectorstate"
)

type memoryAssistantThreadStore struct {
	*memoryAssistantStore
	mu      sync.Mutex
	threads map[string]assistantstate.ThreadRecord
	clock   int64
}

func (s *memoryAssistantThreadStore) CreateThread(_ context.Context, record assistantstate.ThreadRecord, quota int) (assistantstate.ThreadRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, existing := range s.threads {
		if existing.OwnerKey == record.OwnerKey {
			count++
		}
	}
	if count >= quota {
		return assistantstate.ThreadRecord{}, assistantstate.ErrQuotaExceeded
	}
	key := record.OwnerKey + "/" + record.ID
	if _, found := s.threads[key]; found {
		return assistantstate.ThreadRecord{}, assistantstate.ErrConflict
	}
	s.clock++
	record.Revision = 1
	record.CreatedAt = time.Unix(s.clock, 0).UTC()
	record.UpdatedAt = record.CreatedAt
	record.Snapshot = append([]byte(nil), record.Snapshot...)
	s.threads[key] = record
	return record, nil
}

func (s *memoryAssistantThreadStore) ListThreads(_ context.Context, owner string, limit int, after string) ([]assistantstate.ThreadRecord, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]assistantstate.ThreadRecord, 0)
	for _, record := range s.threads {
		if record.OwnerKey == owner {
			values = append(values, record)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].CreatedAt.After(values[j].CreatedAt) })
	if after != "" {
		return nil, "", assistantstate.ErrNotFound
	}
	if len(values) > limit {
		return values[:limit], values[limit-1].ID, nil
	}
	return values, "", nil
}

func (s *memoryAssistantThreadStore) GetThread(_ context.Context, owner, id string) (assistantstate.ThreadRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.threads[owner+"/"+id]
	if !found {
		return assistantstate.ThreadRecord{}, assistantstate.ErrNotFound
	}
	return record, nil
}

func (s *memoryAssistantThreadStore) UpdateThread(_ context.Context, owner, id string, snapshot []byte, revision int64) (assistantstate.ThreadRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := owner + "/" + id
	record, found := s.threads[key]
	if !found {
		return assistantstate.ThreadRecord{}, assistantstate.ErrNotFound
	}
	if record.Revision != revision {
		return assistantstate.ThreadRecord{}, assistantstate.ErrConflict
	}
	record.Revision++
	record.UpdatedAt = record.UpdatedAt.Add(time.Second)
	record.Snapshot = append([]byte(nil), snapshot...)
	s.threads[key] = record
	return record, nil
}

func (s *memoryAssistantThreadStore) DeleteThread(_ context.Context, owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := owner + "/" + id
	if _, found := s.threads[key]; !found {
		return assistantstate.ErrNotFound
	}
	delete(s.threads, key)
	return nil
}

func (*memoryAssistantThreadStore) CreateThreadMessage(context.Context, assistantstate.MessageRecord, int) (assistantstate.MessageRecord, error) {
	return assistantstate.MessageRecord{}, assistantstate.ErrUnavailable
}
func (*memoryAssistantThreadStore) ListThreadMessages(context.Context, string, string, int, string) ([]assistantstate.MessageRecord, string, error) {
	return nil, "", assistantstate.ErrUnavailable
}
func (*memoryAssistantThreadStore) GetThreadMessage(context.Context, string, string, string) (assistantstate.MessageRecord, error) {
	return assistantstate.MessageRecord{}, assistantstate.ErrUnavailable
}
func (*memoryAssistantThreadStore) UpdateThreadMessage(context.Context, string, string, string, []byte, int64) (assistantstate.MessageRecord, error) {
	return assistantstate.MessageRecord{}, assistantstate.ErrUnavailable
}
func (*memoryAssistantThreadStore) DeleteThreadMessage(context.Context, string, string, string) error {
	return assistantstate.ErrUnavailable
}

func TestAssistantThreadCRUDOwnershipQuotaAndValidation(t *testing.T) {
	store := &memoryAssistantThreadStore{
		memoryAssistantStore: &memoryAssistantStore{records: map[string]assistantstate.Record{}},
		threads:              map[string]assistantstate.ThreadRecord{},
	}
	handlerFor := func(user string) http.Handler {
		return Routes(NewHandler(modules.NewPipeline([]modules.Module{&assistantAuthModule{user: user}}), nil).
			WithAssistantStore(store, AssistantRuntimeConfig{OwnerQuota: 10, ThreadOwnerQuota: 1}))
	}
	handler := handlerFor("user-a")
	created := assistantRequest(t, handler, http.MethodPost, "/v1/threads", `{"metadata":{"case":"one"}}`)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"object":"thread"`) || !strings.Contains(created.Body.String(), `"tool_resources":{}`) {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var value map[string]any
	if json.Unmarshal(created.Body.Bytes(), &value) != nil {
		t.Fatal("invalid create response")
	}
	id, _ := value["id"].(string)
	if !strings.HasPrefix(id, "thread_") {
		t.Fatalf("id=%q", id)
	}
	if response := assistantRequest(t, handlerFor("user-b"), http.MethodGet, "/v1/threads/"+id, ""); response.Code != http.StatusNotFound {
		t.Fatalf("cross-owner status=%d body=%s", response.Code, response.Body.String())
	}
	updated := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+id, `{"metadata":{"case":"updated"}}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"case":"updated"`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	if response := assistantRequest(t, handler, http.MethodPost, "/v1/threads", `{}`); response.Code != http.StatusTooManyRequests {
		t.Fatalf("quota status=%d body=%s", response.Code, response.Body.String())
	}
	if response := assistantRequest(t, handler, http.MethodPost, "/v1/threads/"+id, `{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("empty update status=%d body=%s", response.Code, response.Body.String())
	}
	if response := assistantRequest(t, handler, http.MethodDelete, "/v1/threads/"+id, ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"deleted":true`) {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAssistantThreadResourcesMustBeOwned(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "credential", UserID: "user"}
	owner := fileOwnerKey(identity)
	files := &memoryFileStore{files: map[string]filestate.File{"file_owned": {ID: "file_owned", OwnerKey: owner, Purpose: "assistants"}}}
	vectors := &memoryVectorStore{stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner, Status: "completed"}}}
	store := &memoryAssistantThreadStore{memoryAssistantStore: &memoryAssistantStore{records: map[string]assistantstate.Record{}}, threads: map[string]assistantstate.ThreadRecord{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&assistantAuthModule{user: "user"}}), nil).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}).
		WithVectorStore(vectors, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 10}).
		WithAssistantStore(store, AssistantRuntimeConfig{OwnerQuota: 10, ThreadOwnerQuota: 10}))
	valid := `{"tool_resources":{"code_interpreter":{"file_ids":["file_owned"]},"file_search":{"vector_store_ids":["vs_owned"]}}}`
	if response := assistantRequest(t, handler, http.MethodPost, "/v1/threads", valid); response.Code != http.StatusOK {
		t.Fatalf("owned resources status=%d body=%s", response.Code, response.Body.String())
	}
	unowned := `{"tool_resources":{"code_interpreter":{"file_ids":["file_other"]}}}`
	if response := assistantRequest(t, handler, http.MethodPost, "/v1/threads", unowned); response.Code != http.StatusBadRequest {
		t.Fatalf("unowned resources status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAssistantThreadRejectsUnknownFieldsAndUnavailableStorage(t *testing.T) {
	store := &memoryAssistantStore{records: map[string]assistantstate.Record{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&assistantAuthModule{user: "user"}}), nil).
		WithAssistantStore(store, AssistantRuntimeConfig{OwnerQuota: 10, ThreadOwnerQuota: 10}))
	if response := assistantRequest(t, handler, http.MethodPost, "/v1/threads", `{"unknown":true}`); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", response.Code, response.Body.String())
	}
	threadStore := &memoryAssistantThreadStore{memoryAssistantStore: store, threads: map[string]assistantstate.ThreadRecord{}}
	handler = Routes(NewHandler(modules.NewPipeline([]modules.Module{&assistantAuthModule{user: "user"}}), nil).
		WithAssistantStore(threadStore, AssistantRuntimeConfig{OwnerQuota: 10, ThreadOwnerQuota: 10}))
	if response := assistantRequest(t, handler, http.MethodPost, "/v1/threads", `{"unknown":true}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", response.Code, response.Body.String())
	}
	if response := assistantRequestWithKey(t, handler, http.MethodPost, "/v1/threads", `{}`, "bad"); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", response.Code)
	}
}

var _ assistantstate.ThreadStore = (*memoryAssistantThreadStore)(nil)
