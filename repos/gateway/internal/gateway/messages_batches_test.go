package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
)

type messagesBatchAuthModule struct{}

func (*messagesBatchAuthModule) Name() string   { return "auth" }
func (*messagesBatchAuthModule) Required() bool { return true }
func (*messagesBatchAuthModule) Handle(_ context.Context, request *modules.RequestContext) error {
	request.CredentialID = request.APIKey
	request.UserID = "user"
	request.APIKey = ""
	request.AllowedModels = []string{"message-model"}
	return nil
}

func TestMessagesBatchLifecycleProducesNativeResults(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	provider := &batchProvider{models: []string{"message-model"}}
	handler := NewHandler(modules.NewPipeline([]modules.Module{&messagesBatchAuthModule{}}), provider).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).
		WithBatchStore(store, store)
	routes := Routes(handler)

	createdResponse := messagesBatchRequest(t, routes, http.MethodPost, "/v1/messages/batches", "owner-a", `{
		"requests":[
			{"custom_id":"first","params":{"model":"message-model","max_tokens":16,"messages":[{"role":"user","content":"one"}]}},
			{"custom_id":"second","params":{"model":"message-model","max_tokens":16,"messages":[{"role":"user","content":"two"}]}}
		]}`)
	if createdResponse.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created messagesBatch
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ID, "msgbatch_") || created.ProcessingStatus != "in_progress" || created.RequestCounts.Processing != 2 || created.ResultsURL != nil {
		t.Fatalf("created=%+v", created)
	}
	if len(store.jobs) != 2 {
		t.Fatalf("jobs=%d", len(store.jobs))
	}
	if processed, err := handler.ProcessBatchItems(t.Context()); err != nil || processed != 2 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}

	retrieved := messagesBatchRequest(t, routes, http.MethodGet, "/v1/messages/batches/"+created.ID, "owner-a", "")
	var ended messagesBatch
	if err := json.Unmarshal(retrieved.Body.Bytes(), &ended); err != nil || retrieved.Code != http.StatusOK {
		t.Fatalf("retrieve status=%d body=%s err=%v", retrieved.Code, retrieved.Body.String(), err)
	}
	if ended.ProcessingStatus != "ended" || ended.RequestCounts.Succeeded != 2 || ended.EndedAt == nil || ended.ResultsURL == nil {
		t.Fatalf("ended=%+v", ended)
	}
	results := messagesBatchRequest(t, routes, http.MethodGet, "/v1/messages/batches/"+created.ID+"/results", "owner-a", "")
	if results.Code != http.StatusOK || results.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("results status=%d type=%q body=%s", results.Code, results.Header().Get("Content-Type"), results.Body.String())
	}
	lines := strings.Split(strings.TrimSpace(results.Body.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"custom_id":"first"`) || !strings.Contains(lines[0], `"type":"succeeded"`) || !strings.Contains(lines[0], `"type":"message"`) || !strings.Contains(lines[1], `"custom_id":"second"`) {
		t.Fatalf("results=%s", results.Body.String())
	}

	deleted := messagesBatchRequest(t, routes, http.MethodDelete, "/v1/messages/batches/"+created.ID, "owner-a", "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"type":"message_batch_deleted"`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := messagesBatchRequest(t, routes, http.MethodGet, "/v1/messages/batches/"+created.ID, "owner-a", "")
	if missing.Code != http.StatusNotFound || len(store.jobs) != 0 {
		t.Fatalf("missing status=%d jobs=%d body=%s", missing.Code, len(store.jobs), missing.Body.String())
	}
}

func TestMessagesBatchCancelOwnerIsolationAndListPagination(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	handler := NewHandler(modules.NewPipeline([]modules.Module{&messagesBatchAuthModule{}}), &batchProvider{models: []string{"message-model"}}).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).
		WithBatchStore(store, store)
	routes := Routes(handler)
	createBody := `{"requests":[{"custom_id":"one","params":{"model":"message-model","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}}]}`
	firstResponse := messagesBatchRequest(t, routes, http.MethodPost, "/v1/messages/batches", "owner-a", createBody)
	secondResponse := messagesBatchRequest(t, routes, http.MethodPost, "/v1/messages/batches", "owner-a", createBody)
	var first, second messagesBatch
	if firstResponse.Code != http.StatusOK || secondResponse.Code != http.StatusOK || json.Unmarshal(firstResponse.Body.Bytes(), &first) != nil || json.Unmarshal(secondResponse.Body.Bytes(), &second) != nil {
		t.Fatalf("create statuses=%d,%d bodies=%s %s", firstResponse.Code, secondResponse.Code, firstResponse.Body.String(), secondResponse.Body.String())
	}
	foreign := messagesBatchRequest(t, routes, http.MethodGet, "/v1/messages/batches/"+first.ID, "owner-b", "")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign status=%d body=%s", foreign.Code, foreign.Body.String())
	}

	listed := messagesBatchRequest(t, routes, http.MethodGet, "/v1/messages/batches?limit=1", "owner-a", "")
	var page struct {
		Data    []messagesBatch `json:"data"`
		HasMore bool            `json:"has_more"`
		LastID  string          `json:"last_id"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil || listed.Code != http.StatusOK || len(page.Data) != 1 || !page.HasMore || page.LastID == "" {
		t.Fatalf("list status=%d page=%+v body=%s err=%v", listed.Code, page, listed.Body.String(), err)
	}
	next := messagesBatchRequest(t, routes, http.MethodGet, "/v1/messages/batches?limit=1&after_id="+page.LastID, "owner-a", "")
	var nextPage struct {
		Data    []messagesBatch `json:"data"`
		HasMore bool            `json:"has_more"`
	}
	if err := json.Unmarshal(next.Body.Bytes(), &nextPage); err != nil || next.Code != http.StatusOK || len(nextPage.Data) != 1 || nextPage.HasMore || nextPage.Data[0].ID == page.Data[0].ID {
		t.Fatalf("next status=%d page=%+v body=%s err=%v", next.Code, nextPage, next.Body.String(), err)
	}

	cancelled := messagesBatchRequest(t, routes, http.MethodPost, "/v1/messages/batches/"+first.ID+"/cancel", "owner-a", "")
	var ended messagesBatch
	if err := json.Unmarshal(cancelled.Body.Bytes(), &ended); err != nil || cancelled.Code != http.StatusOK || ended.ProcessingStatus != "ended" || ended.RequestCounts.Canceled != 1 {
		t.Fatalf("cancel status=%d batch=%+v body=%s err=%v", cancelled.Code, ended, cancelled.Body.String(), err)
	}
	results := messagesBatchRequest(t, routes, http.MethodGet, "/v1/messages/batches/"+first.ID+"/results", "owner-a", "")
	if results.Code != http.StatusOK || !strings.Contains(results.Body.String(), `"type":"canceled"`) {
		t.Fatalf("results status=%d body=%s", results.Code, results.Body.String())
	}
}

func TestMessagesBatchRejectsInvalidEnvelopeBeforePersistence(t *testing.T) {
	for _, test := range []struct {
		name    string
		path    string
		version string
		body    string
	}{
		{name: "missing version", path: "/v1/messages/batches", body: `{"requests":[]}`},
		{name: "duplicate custom id", path: "/v1/messages/batches", version: "2023-06-01", body: `{"requests":[{"custom_id":"same","params":{"model":"message-model","max_tokens":8,"messages":[{"role":"user","content":"a"}]}},{"custom_id":"same","params":{"model":"message-model","max_tokens":8,"messages":[{"role":"user","content":"b"}]}}]}`},
		{name: "streaming", path: "/v1/messages/batches", version: "2023-06-01", body: `{"requests":[{"custom_id":"one","params":{"model":"message-model","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"a"}]}}]}`},
		{name: "foreign cursor namespace", path: "/v1/messages/batches?after_id=batch_other", version: "2023-06-01"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryBatchStore()
			files := &memoryFileStore{files: map[string]filestate.File{}}
			routes := Routes(NewHandler(modules.NewPipeline([]modules.Module{&messagesBatchAuthModule{}}), &batchProvider{models: []string{"message-model"}}).
				WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).
				WithBatchStore(store, store))
			method := http.MethodPost
			if strings.Contains(test.path, "?") {
				method = http.MethodGet
			}
			request := httptest.NewRequest(method, test.path, strings.NewReader(test.body))
			request.Header.Set("x-api-key", "owner-a")
			if test.version != "" {
				request.Header.Set("anthropic-version", test.version)
			}
			response := httptest.NewRecorder()
			routes.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || len(store.batches) != 0 {
				t.Fatalf("status=%d batches=%d body=%s", response.Code, len(store.batches), response.Body.String())
			}
		})
	}
}

func messagesBatchRequest(t *testing.T, handler http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("x-api-key", key)
	request.Header.Set("anthropic-version", "2023-06-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
