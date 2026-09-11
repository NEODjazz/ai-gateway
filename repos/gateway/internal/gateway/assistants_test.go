package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

type memoryAssistantStore struct {
	mu      sync.Mutex
	records map[string]assistantstate.Record
	clock   int64
}

func (s *memoryAssistantStore) CreateAssistant(_ context.Context, record assistantstate.Record, quota int) (assistantstate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, existing := range s.records {
		if existing.OwnerKey == record.OwnerKey {
			count++
		}
	}
	if count >= quota {
		return assistantstate.Record{}, assistantstate.ErrQuotaExceeded
	}
	key := record.OwnerKey + "/" + record.ID
	if _, found := s.records[key]; found {
		return assistantstate.Record{}, assistantstate.ErrConflict
	}
	s.clock++
	record.Revision = 1
	record.CreatedAt = time.Unix(s.clock, 0).UTC()
	record.UpdatedAt = record.CreatedAt
	record.Snapshot = append([]byte(nil), record.Snapshot...)
	s.records[key] = record
	return record, nil
}

func (s *memoryAssistantStore) ListAssistants(_ context.Context, owner string, limit int, after string) ([]assistantstate.Record, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]assistantstate.Record, 0)
	for _, record := range s.records {
		if record.OwnerKey == owner {
			values = append(values, record)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].CreatedAt.After(values[j].CreatedAt) || values[i].CreatedAt.Equal(values[j].CreatedAt) && values[i].ID > values[j].ID
	})
	start := 0
	if after != "" {
		start = -1
		for index := range values {
			if values[index].ID == after {
				start = index + 1
			}
		}
		if start < 0 {
			return nil, "", assistantstate.ErrNotFound
		}
	}
	values = values[start:]
	next := ""
	if len(values) > limit {
		next = values[limit-1].ID
		values = values[:limit]
	}
	return append([]assistantstate.Record(nil), values...), next, nil
}

func (s *memoryAssistantStore) GetAssistant(_ context.Context, owner, id string) (assistantstate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.records[owner+"/"+id]
	if !found {
		return assistantstate.Record{}, assistantstate.ErrNotFound
	}
	return record, nil
}

func (s *memoryAssistantStore) UpdateAssistant(_ context.Context, owner, id string, snapshot []byte, revision int64) (assistantstate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := owner + "/" + id
	record, found := s.records[key]
	if !found {
		return assistantstate.Record{}, assistantstate.ErrNotFound
	}
	if record.Revision != revision {
		return assistantstate.Record{}, assistantstate.ErrConflict
	}
	record.Revision++
	record.UpdatedAt = record.UpdatedAt.Add(time.Second)
	record.Snapshot = append([]byte(nil), snapshot...)
	s.records[key] = record
	return record, nil
}

func (s *memoryAssistantStore) DeleteAssistant(_ context.Context, owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := owner + "/" + id
	if _, found := s.records[key]; !found {
		return assistantstate.ErrNotFound
	}
	delete(s.records, key)
	return nil
}

type assistantAuthModule struct {
	user string
}

func (*assistantAuthModule) Name() string   { return "auth" }
func (*assistantAuthModule) Required() bool { return true }
func (m *assistantAuthModule) Handle(_ context.Context, request *modules.RequestContext) error {
	if request.APIKey != "gateway-key" {
		return modules.ErrUnauthorized
	}
	request.CredentialID = "credential"
	request.UserID = m.user
	request.AllowedModels = []string{"model-a", "model-b"}
	request.AllowedTools = []string{"lookup", "file_search", "code_interpreter"}
	return nil
}

func TestAssistantCRUDOwnershipPaginationAndPolicy(t *testing.T) {
	store := &memoryAssistantStore{records: map[string]assistantstate.Record{}}
	handlerFor := func(user string) http.Handler {
		return Routes(NewHandler(modules.NewPipeline([]modules.Module{&assistantAuthModule{user: user}}), nil).WithAssistantStore(store, AssistantRuntimeConfig{OwnerQuota: 2}))
	}
	handler := handlerFor("user-a")
	create := assistantRequest(t, handler, http.MethodPost, "/v1/assistants", `{"model":"model-a","name":"Support","instructions":"Use policy","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}},{"type":"file_search"}],"metadata":{"team":"ops"}}`)
	if create.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var created map[string]any
	if json.Unmarshal(create.Body.Bytes(), &created) != nil {
		t.Fatal("invalid create response")
	}
	id, _ := created["id"].(string)
	if !strings.HasPrefix(id, "asst_") || created["object"] != "assistant" || created["model"] != "model-a" {
		t.Fatalf("created=%v", created)
	}

	second := assistantRequest(t, handler, http.MethodPost, "/v1/assistants", `{"model":"model-b"}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second=%d %s", second.Code, second.Body.String())
	}
	page := assistantRequest(t, handler, http.MethodGet, "/v1/assistants?limit=1", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"has_more":true`) {
		t.Fatalf("page=%d %s", page.Code, page.Body.String())
	}

	otherGet := assistantRequest(t, handlerFor("user-b"), http.MethodGet, "/v1/assistants/"+id, "")
	if otherGet.Code != http.StatusNotFound {
		t.Fatalf("cross-owner status=%d body=%s", otherGet.Code, otherGet.Body.String())
	}
	update := assistantRequest(t, handler, http.MethodPost, "/v1/assistants/"+id, `{"model":"model-b","instructions":"Updated"}`)
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"instructions":"Updated"`) {
		t.Fatalf("update=%d %s", update.Code, update.Body.String())
	}
	cleared := assistantRequest(t, handler, http.MethodPost, "/v1/assistants/"+id, `{"instructions":null,"temperature":null}`)
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"instructions":null`) || strings.Contains(cleared.Body.String(), `"temperature"`) {
		t.Fatalf("clear nullable fields=%d %s", cleared.Code, cleared.Body.String())
	}
	deleted := assistantRequest(t, handler, http.MethodDelete, "/v1/assistants/"+id, "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete=%d %s", deleted.Code, deleted.Body.String())
	}
}

func TestAssistantRejectsInvalidUnauthorizedAndOverQuotaRequests(t *testing.T) {
	store := &memoryAssistantStore{records: map[string]assistantstate.Record{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&assistantAuthModule{user: "user"}}), nil).WithAssistantStore(store, AssistantRuntimeConfig{OwnerQuota: 1}))
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{name: "unknown field", body: `{"model":"model-a","unknown":true}`, status: http.StatusBadRequest},
		{name: "disallowed model", body: `{"model":"model-c"}`, status: http.StatusForbidden},
		{name: "disallowed tool", body: `{"model":"model-a","tools":[{"type":"function","function":{"name":"other"}}]}`, status: http.StatusForbidden},
		{name: "invalid function", body: `{"model":"model-a","tools":[{"type":"function","function":{"name":"bad name"}}]}`, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := assistantRequest(t, handler, http.MethodPost, "/v1/assistants", test.body)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if response := assistantRequestWithKey(t, handler, http.MethodPost, "/v1/assistants", `{"model":"model-a"}`, "bad"); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d", response.Code)
	}
	if response := assistantRequest(t, handler, http.MethodPost, "/v1/assistants", `{"model":"model-a"}`); response.Code != http.StatusOK {
		t.Fatalf("valid=%d %s", response.Code, response.Body.String())
	}
	if response := assistantRequest(t, handler, http.MethodPost, "/v1/assistants", `{"model":"model-a"}`); response.Code != http.StatusTooManyRequests {
		t.Fatalf("quota=%d %s", response.Code, response.Body.String())
	}
}

func TestAssistantToolResourcesRequireOwnedFilesAndVectorStores(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "credential", UserID: "user"}
	owner := fileOwnerKey(identity)
	files := &memoryFileStore{files: map[string]filestate.File{
		"file_owned": {ID: "file_owned", OwnerKey: owner, Purpose: "assistants"},
		"file_other": {ID: "file_other", OwnerKey: "other", Purpose: "assistants"},
	}}
	vectors := &memoryVectorStore{stores: map[string]vectorstate.VectorStore{
		"vs_owned": {ID: "vs_owned", OwnerKey: owner, Name: "docs", Status: "completed"},
	}}
	store := &memoryAssistantStore{records: map[string]assistantstate.Record{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&assistantAuthModule{user: "user"}}), nil).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}).
		WithVectorStore(vectors, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 10, ByteQuota: 1024}).
		WithAssistantStore(store, AssistantRuntimeConfig{OwnerQuota: 10}))

	valid := `{"model":"model-a","tools":[{"type":"code_interpreter"},{"type":"file_search"}],"tool_resources":{"code_interpreter":{"file_ids":["file_owned"]},"file_search":{"vector_store_ids":["vs_owned"]}}}`
	if response := assistantRequest(t, handler, http.MethodPost, "/v1/assistants", valid); response.Code != http.StatusOK {
		t.Fatalf("owned resources status=%d body=%s", response.Code, response.Body.String())
	}
	unowned := `{"model":"model-a","tools":[{"type":"code_interpreter"}],"tool_resources":{"code_interpreter":{"file_ids":["file_other"]}}}`
	if response := assistantRequest(t, handler, http.MethodPost, "/v1/assistants", unowned); response.Code != http.StatusBadRequest {
		t.Fatalf("unowned resource status=%d body=%s", response.Code, response.Body.String())
	}
}

func assistantRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return assistantRequestWithKey(t, handler, method, path, body, "gateway-key")
}

func assistantRequestWithKey(t *testing.T, handler http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

var _ assistantstate.Store = (*memoryAssistantStore)(nil)
