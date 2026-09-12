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

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/batchstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	providerpkg "ai-gateway-gateway/internal/provider"
)

type memoryBatchStore struct {
	mu      sync.Mutex
	batches map[string]batchstate.Batch
	items   map[string]map[int]batchstate.Item
	jobs    map[string]asyncstate.Job
}

func newMemoryBatchStore() *memoryBatchStore {
	return &memoryBatchStore{batches: map[string]batchstate.Batch{}, items: map[string]map[int]batchstate.Item{}, jobs: map[string]asyncstate.Job{}}
}

func (s *memoryBatchStore) CreateBatch(_ context.Context, batch batchstate.Batch, items []batchstate.Item, jobs []asyncstate.Job, _ int) (batchstate.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch.CreatedAt = time.Unix(100, 0).UTC()
	s.batches[batch.ID] = batch
	s.items[batch.ID] = map[int]batchstate.Item{}
	for index, item := range items {
		s.items[batch.ID][item.Ordinal] = item
		s.jobs[jobs[index].ResourceID] = jobs[index]
	}
	return batch, nil
}
func (s *memoryBatchStore) GetBatch(_ context.Context, owner, id string) (batchstate.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[id]
	if !ok || b.OwnerKey != owner {
		return batchstate.Batch{}, batchstate.ErrNotFound
	}
	return b, nil
}
func (s *memoryBatchStore) ListBatches(_ context.Context, owner string, limit int, _ string) ([]batchstate.Batch, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []batchstate.Batch
	for _, b := range s.batches {
		if b.OwnerKey == owner {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		next := out[limit-1].ID
		return out[:limit], next, nil
	}
	return out, "", nil
}
func (s *memoryBatchStore) GetBatchItem(_ context.Context, owner, id string, ordinal int) (batchstate.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id][ordinal]
	if !ok || item.OwnerKey != owner {
		return batchstate.Item{}, batchstate.ErrNotFound
	}
	return item, nil
}
func (s *memoryBatchStore) StartBatch(_ context.Context, owner, id string) (batchstate.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[id]
	if !ok || b.OwnerKey != owner {
		return batchstate.Batch{}, batchstate.ErrNotFound
	}
	if b.Status != "queued" && b.Status != "in_progress" {
		return batchstate.Batch{}, batchstate.ErrConflict
	}
	if b.Status == "queued" {
		now := time.Unix(100, 0).UTC()
		b.Status = "in_progress"
		b.InProgressAt = &now
		s.batches[id] = b
	}
	return b, nil
}
func (s *memoryBatchStore) FinishBatchItem(_ context.Context, item batchstate.Item, failed bool) (batchstate.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := s.items[item.BatchID][item.Ordinal]
	if stored.State != "pending" {
		return s.batches[item.BatchID], nil
	}
	stored.Result = item.Result
	if failed {
		stored.State = "failed"
	} else {
		stored.State = "completed"
	}
	s.items[item.BatchID][item.Ordinal] = stored
	b := s.batches[item.BatchID]
	if failed {
		b.Failed++
	} else {
		b.Completed++
	}
	now := time.Unix(101, 0).UTC()
	b.InProgressAt = &now
	b.Status = "in_progress"
	if b.Completed+b.Failed == b.Total {
		b.Status = "finalizing"
		b.FinalizingAt = &now
	}
	s.batches[b.ID] = b
	return b, nil
}
func (s *memoryBatchStore) ListBatchResults(_ context.Context, owner, id string, failed bool) ([]batchstate.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []batchstate.Item
	state := "completed"
	if failed {
		state = "failed"
	}
	for _, item := range s.items[id] {
		if item.OwnerKey == owner && item.State == state {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ordinal < out[j].Ordinal })
	return out, nil
}
func (s *memoryBatchStore) CancelBatch(_ context.Context, owner, id string) (batchstate.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[id]
	if !ok || b.OwnerKey != owner {
		return batchstate.Batch{}, batchstate.ErrNotFound
	}
	now := time.Unix(102, 0).UTC()
	b.Status = "cancelled"
	b.CancelledAt = &now
	s.batches[id] = b
	return b, nil
}
func (s *memoryBatchStore) ExpireBatch(_ context.Context, owner, id string) (batchstate.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[id]
	if !ok || b.OwnerKey != owner {
		return batchstate.Batch{}, batchstate.ErrNotFound
	}
	b.Status = "expired"
	s.batches[id] = b
	return b, nil
}
func (s *memoryBatchStore) FinalizeBatch(_ context.Context, owner, id, output, errorID string) (batchstate.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.batches[id]
	if b.OwnerKey != owner {
		return batchstate.Batch{}, batchstate.ErrNotFound
	}
	now := time.Unix(103, 0).UTC()
	b.Status = "completed"
	b.OutputFileID = output
	b.ErrorFileID = errorID
	b.CompletedAt = &now
	s.batches[id] = b
	return b, nil
}
func (s *memoryBatchStore) EnqueueAsyncJob(context.Context, asyncstate.Job) (bool, error) {
	return false, errors.New("unused")
}
func (s *memoryBatchStore) HasAsyncJob(_ context.Context, kind, id, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return ok && j.Kind == kind && j.OwnerKey == owner, nil
}
func (s *memoryBatchStore) ClaimAsyncJobs(_ context.Context, kind string, limit int, lease time.Duration) ([]asyncstate.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for key, j := range s.jobs {
		if j.Kind == kind && j.LeaseGeneration == 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]asyncstate.Job, 0, len(keys))
	for _, key := range keys {
		j := s.jobs[key]
		j.LeaseGeneration = 1
		j.Attempts++
		j.LeaseUntil = time.Now().Add(lease)
		s.jobs[key] = j
		out = append(out, j)
	}
	return out, nil
}
func (s *memoryBatchStore) RetryAsyncJob(_ context.Context, kind, id string, generation int64, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j.Kind != kind || j.LeaseGeneration != generation {
		return asyncstate.ErrLeaseLost
	}
	j.LeaseGeneration = 0
	s.jobs[id] = j
	return nil
}
func (s *memoryBatchStore) CompleteAsyncJob(_ context.Context, kind, id string, generation int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok || j.Kind != kind || j.LeaseGeneration != generation {
		return asyncstate.ErrLeaseLost
	}
	delete(s.jobs, id)
	return nil
}

type batchProvider struct {
	mu         sync.Mutex
	models     []string
	executions []string
	speech     modules.RequestContext
	speechData []byte
	compact    modules.RequestContext
	ocr        modules.RequestContext
}

func (p *batchProvider) ChatCompletions(_ context.Context, req modules.RequestContext) (openai.ChatCompletionResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.mu.Unlock()
	return openai.ChatCompletionResponse{ID: "chat_" + req.RequestID, Object: "chat.completion", Model: req.Request.Model, Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "ok"}}}}, nil
}
func (p *batchProvider) StreamChatCompletions(context.Context, modules.RequestContext, providerpkg.ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	return openai.ChatCompletionResponse{}, false, nil
}
func (p *batchProvider) Responses(context.Context, modules.RequestContext) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, errors.New("unused")
}
func (p *batchProvider) StreamResponses(context.Context, modules.RequestContext, providerpkg.ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	return openai.ResponseResponse{}, false, nil
}
func (p *batchProvider) Models() []openai.Model {
	out := make([]openai.Model, len(p.models))
	for i, id := range p.models {
		out[i] = openai.Model{ID: id}
	}
	return out
}

func (p *batchProvider) Rerank(_ context.Context, req modules.RequestContext) (openai.RerankResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.mu.Unlock()
	return openai.RerankResponse{ID: "rerank_" + req.RequestID, Results: []openai.RerankResult{{Index: 1, RelevanceScore: 0.9}}}, nil
}

func (p *batchProvider) Search(_ context.Context, req modules.RequestContext) (openai.SearchResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.mu.Unlock()
	model, _ := req.SearchRequest.RoutingModel()
	return openai.SearchResponse{Object: "search", Model: model, Results: []openai.SearchResult{{Title: "Result", URL: "https://example.test/result"}}, Usage: openai.Usage{SearchRequests: req.SearchRequest.SearchUnits()}}, nil
}

func (p *batchProvider) GenerateImage(_ context.Context, req modules.RequestContext) (openai.ImageGenerationResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.mu.Unlock()
	return openai.ImageGenerationResponse{Created: 7, Data: []openai.ImageData{{URL: "https://images.example/result.png"}}, Usage: &openai.ImageUsage{InputTokens: 2, OutputTokens: 5, TotalTokens: 7}}, nil
}

func (p *batchProvider) GenerateSpeech(_ context.Context, req modules.RequestContext) (openai.AudioSpeechResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.speech = req
	data := append([]byte(nil), p.speechData...)
	p.mu.Unlock()
	usage := openai.AudioSpeechUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}
	return openai.AudioSpeechResponse{Data: data, ContentType: "audio/mpeg", Model: req.AudioSpeechRequest.Model, Usage: &usage}, nil
}

func (p *batchProvider) CompactResponse(_ context.Context, req modules.RequestContext) (openai.CompactedResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.compact = req
	p.mu.Unlock()
	return openai.CompactedResponse{
		ID: "cmp_" + req.RequestID, Object: "response.compaction", Output: []json.RawMessage{json.RawMessage(`{"type":"compaction","encrypted_content":"opaque"}`)},
		Usage: openai.ResponseUsage{InputTokens: 8, OutputTokens: 2, TotalTokens: 10},
	}, nil
}

func (p *batchProvider) OCR(_ context.Context, req modules.RequestContext) (openai.OCRResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.ocr = req
	p.mu.Unlock()
	return openai.OCRResponse{Pages: []json.RawMessage{json.RawMessage(`{"index":0,"markdown":"text","images":[]}`)}, Model: req.OCRRequest.Model, UsageInfo: openai.OCRUsageInfo{PagesProcessed: 1}}, nil
}

func TestBatchLifecycleExecutesMixedModelsWithDistinctBillingIDs(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	provider := &batchProvider{models: []string{"model-a", "model-b"}}
	identity := modules.RequestContext{CredentialID: "credential", UserID: "user"}
	owner := fileOwnerKey(identity)
	payload := []byte("{\"custom_id\":\"a\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"model-a\",\"messages\":[{\"role\":\"user\",\"content\":\"one\"}]}}\n{\"custom_id\":\"b\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"model-b\",\"messages\":[{\"role\":\"user\",\"content\":\"two\"}]}}\n")
	files.files["file_input"] = filestate.File{ID: "file_input", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
	h := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), provider).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store)
	routes := Routes(h)
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_input","endpoint":"/v1/chat/completions","completion_window":"24h","output_expires_after":{"anchor":"created_at","seconds":3600}}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created openai.Batch
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != "queued" || created.RequestCounts.Total != 2 {
		t.Fatalf("created=%+v", created)
	}
	processed, err := h.ProcessBatchItems(t.Context())
	if err != nil || processed != 2 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	done := authorizedFileRequest(t, routes, http.MethodGet, "/v1/batches/"+created.ID)
	if done.Code != http.StatusOK || !strings.Contains(done.Body.String(), `"status":"completed"`) {
		t.Fatalf("retrieve status=%d body=%s", done.Code, done.Body.String())
	}
	var batch openai.Batch
	_ = json.Unmarshal(done.Body.Bytes(), &batch)
	if batch.OutputFileID == "" || batch.RequestCounts.Completed != 2 {
		t.Fatalf("batch=%+v", batch)
	}
	output, err := files.Get(t.Context(), owner, batch.OutputFileID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output.Content), `"custom_id":"a"`) || !strings.Contains(string(output.Content), `"custom_id":"b"`) {
		t.Fatalf("output=%s", output.Content)
	}
	if output.ExpiresAt == nil || output.ExpiresAt.Sub(output.CreatedAt) != time.Hour {
		t.Fatalf("output expiry=%v created=%v", output.ExpiresAt, output.CreatedAt)
	}
	provider.mu.Lock()
	executions := append([]string(nil), provider.executions...)
	provider.mu.Unlock()
	if len(executions) != 2 || executions[0] == executions[1] {
		t.Fatalf("execution IDs=%v", executions)
	}
}

func TestBatchLifecycleExecutesRerankWithSharedValidation(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	runtime := &batchProvider{models: []string{"rerank-model"}}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	payload := []byte("{\"custom_id\":\"rank\",\"method\":\"POST\",\"url\":\"/v1/rerank\",\"body\":{\"model\":\"rerank-model\",\"query\":\"refund\",\"documents\":[\"shipping\",\"refund policy\"],\"top_n\":1}}\n")
	files.files["file_rerank"] = filestate.File{ID: "file_rerank", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
	h := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), runtime).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store)
	routes := Routes(h)
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_rerank","endpoint":"/v1/rerank","completion_window":"24h"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created openai.Batch
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if processed, err := h.ProcessBatchItems(t.Context()); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	done := authorizedFileRequest(t, routes, http.MethodGet, "/v1/batches/"+created.ID)
	var batch openai.Batch
	if err := json.Unmarshal(done.Body.Bytes(), &batch); err != nil || done.Code != http.StatusOK || batch.RequestCounts.Completed != 1 {
		t.Fatalf("status=%d batch=%+v err=%v", done.Code, batch, err)
	}
	output, err := files.Get(t.Context(), owner, batch.OutputFileID, true)
	if err != nil || !strings.Contains(string(output.Content), `"id":"rerank_`) || !strings.Contains(string(output.Content), `"index":1`) {
		t.Fatalf("output=%s err=%v", output.Content, err)
	}
}

func TestBatchLifecycleExecutesResponseCompactionWithSharedAccounting(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	runtime := &batchProvider{models: []string{"compact-model"}}
	rates := &embeddingTokenRateStore{}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	payload := []byte("{\"custom_id\":\"compact\",\"method\":\"POST\",\"url\":\"/v1/responses/compact\",\"body\":{\"model\":\"compact-model\",\"input\":[{\"role\":\"user\",\"content\":\"hello\"}],\"instructions\":\"shorten\"}}\n")
	files.files["file_compact"] = filestate.File{ID: "file_compact", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
	h := NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), runtime, rates).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store)
	routes := Routes(h)
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_compact","endpoint":"/v1/responses/compact","completion_window":"24h"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	var created openai.Batch
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
	if processed, err := h.ProcessBatchItems(t.Context()); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	reservedTokens := rates.tokens
	done := authorizedFileRequest(t, routes, http.MethodGet, "/v1/batches/"+created.ID)
	var batch openai.Batch
	if err := json.Unmarshal(done.Body.Bytes(), &batch); err != nil || batch.RequestCounts.Completed != 1 || batch.OutputFileID == "" {
		t.Fatalf("status=%d batch=%+v err=%v", done.Code, batch, err)
	}
	output, err := files.Get(t.Context(), owner, batch.OutputFileID, true)
	if err != nil || !strings.Contains(string(output.Content), `"object":"response.compaction"`) || !strings.Contains(string(output.Content), `"total_tokens":10`) {
		t.Fatalf("output=%s err=%v", output.Content, err)
	}
	runtime.mu.Lock()
	compactRequest := runtime.compact
	runtime.mu.Unlock()
	if compactRequest.ResponseRequest == nil || compactRequest.RequestID == "" || compactRequest.Metadata["gateway.api_type"] != "batch" || reservedTokens != estimateResponseCompactTokens(openai.ResponseCompactRequest{Model: "compact-model", Input: []any{map[string]any{"role": "user", "content": "hello"}}, Instructions: "shorten"}) {
		t.Fatalf("request=%+v metadata=%v TPM=%d", compactRequest.ResponseRequest, compactRequest.Metadata, reservedTokens)
	}
}

func TestBatchRejectsInvalidResponseCompaction(t *testing.T) {
	for _, body := range []string{
		`{"model":"compact-model","input":[],"instructions":"shorten"}`,
		`{"model":"compact-model","input":"hello","stream":true}`,
	} {
		if _, _, _, err := validateBatchBody("/v1/responses/compact", []byte(body)); err == nil {
			t.Fatalf("invalid compact request accepted: %s", body)
		}
	}
}

func TestBatchLifecycleExecutesSearchWithPerQueryAccounting(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	runtime := &batchProvider{models: []string{"search-model"}}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	payload := []byte("{\"custom_id\":\"search\",\"method\":\"POST\",\"url\":\"/v1/search\",\"body\":{\"search_tool_name\":\"search-model\",\"query\":[\"first\",\"second\"],\"max_results\":2,\"country\":\"US\"}}\n")
	files.files["file_search"] = filestate.File{ID: "file_search", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
	h := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), runtime).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store)
	routes := Routes(h)
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_search","endpoint":"/v1/search","completion_window":"24h"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created openai.Batch
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if processed, err := h.ProcessBatchItems(t.Context()); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	done := authorizedFileRequest(t, routes, http.MethodGet, "/v1/batches/"+created.ID)
	var batch openai.Batch
	if err := json.Unmarshal(done.Body.Bytes(), &batch); err != nil || done.Code != http.StatusOK || batch.RequestCounts.Completed != 1 {
		t.Fatalf("status=%d batch=%+v err=%v", done.Code, batch, err)
	}
	output, err := files.Get(t.Context(), owner, batch.OutputFileID, true)
	if err != nil || !strings.Contains(string(output.Content), `"object":"search"`) || !strings.Contains(string(output.Content), `https://example.test/result`) {
		t.Fatalf("output=%s err=%v", output.Content, err)
	}
}

func TestBatchLifecycleExecutesImageGenerationWithTokenSettlement(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	runtime := &batchProvider{models: []string{"image-model"}}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	payload := []byte("{\"custom_id\":\"image\",\"method\":\"POST\",\"url\":\"/v1/images/generations\",\"body\":{\"model\":\"image-model\",\"prompt\":\"draw a circle\",\"n\":1,\"response_format\":\"url\"}}\n")
	files.files["file_image"] = filestate.File{ID: "file_image", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
	h := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), runtime).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store)
	routes := Routes(h)
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_image","endpoint":"/v1/images/generations","completion_window":"24h"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created openai.Batch
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if processed, err := h.ProcessBatchItems(t.Context()); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	done := authorizedFileRequest(t, routes, http.MethodGet, "/v1/batches/"+created.ID)
	var batch openai.Batch
	if err := json.Unmarshal(done.Body.Bytes(), &batch); err != nil || done.Code != http.StatusOK || batch.RequestCounts.Completed != 1 {
		t.Fatalf("status=%d batch=%+v err=%v", done.Code, batch, err)
	}
	output, err := files.Get(t.Context(), owner, batch.OutputFileID, true)
	if err != nil || !strings.Contains(string(output.Content), `https://images.example/result.png`) || !strings.Contains(string(output.Content), `"total_tokens":7`) {
		t.Fatalf("output=%s err=%v", output.Content, err)
	}
}

func TestBatchLifecycleExecutesAudioSpeechWithBoundedJSONOutput(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	runtime := &batchProvider{models: []string{"tts-model"}, speechData: []byte("ID3audio")}
	rates := &embeddingTokenRateStore{}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	payload := []byte("{\"custom_id\":\"speech\",\"method\":\"POST\",\"url\":\"/v1/audio/speech\",\"body\":{\"model\":\"tts-model\",\"input\":\"Привет 👋\",\"voice\":\"alloy\",\"response_format\":\"mp3\"}}\n")
	files.files["file_speech"] = filestate.File{ID: "file_speech", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
	h := NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), runtime, rates).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store)
	routes := Routes(h)
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_speech","endpoint":"/v1/audio/speech","completion_window":"24h"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created openai.Batch
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if processed, err := h.ProcessBatchItems(t.Context()); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	reservedTokens := rates.tokens
	done := authorizedFileRequest(t, routes, http.MethodGet, "/v1/batches/"+created.ID)
	var batch openai.Batch
	if err := json.Unmarshal(done.Body.Bytes(), &batch); err != nil || done.Code != http.StatusOK || batch.RequestCounts.Completed != 1 {
		t.Fatalf("status=%d batch=%+v err=%v", done.Code, batch, err)
	}
	output, err := files.Get(t.Context(), owner, batch.OutputFileID, true)
	if err != nil || !strings.Contains(string(output.Content), `"data":"SUQzYXVkaW8="`) || !strings.Contains(string(output.Content), `"content_type":"audio/mpeg"`) || !strings.Contains(string(output.Content), `"total_tokens":5`) {
		t.Fatalf("output=%s err=%v", output.Content, err)
	}
	runtime.mu.Lock()
	speechRequest := runtime.speech
	runtime.mu.Unlock()
	if speechRequest.AudioSpeechRequest == nil || speechRequest.InputCharacters != 8 || speechRequest.RequestID == "" || reservedTokens != openai.AudioSpeechReserveTokens(*speechRequest.AudioSpeechRequest) {
		t.Fatalf("request=%+v characters=%d TPM=%d", speechRequest.AudioSpeechRequest, speechRequest.InputCharacters, reservedTokens)
	}
}

func TestBatchAudioSpeechRejectsSSEAndContainsOversizedResults(t *testing.T) {
	if _, _, _, err := validateBatchBody("/v1/audio/speech", []byte(`{"model":"tts","input":"hello","voice":"alloy","stream_format":"sse"}`)); err == nil || !strings.Contains(err.Error(), "not supported in batches") {
		t.Fatalf("SSE err=%v", err)
	}

	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	runtime := &batchProvider{models: []string{"tts"}, speechData: []byte(strings.Repeat("a", 400))}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	payload := []byte("{\"custom_id\":\"large\",\"method\":\"POST\",\"url\":\"/v1/audio/speech\",\"body\":{\"model\":\"tts\",\"input\":\"hello\",\"voice\":\"alloy\"}}\n")
	files.files["file_large"] = filestate.File{ID: "file_large", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
	h := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), runtime).WithFileStore(files, FileRuntimeConfig{MaxBytes: batchResultMinLine, OwnerQuotaBytes: 4096}).WithBatchStore(store, store)
	routes := Routes(h)
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_large","endpoint":"/v1/audio/speech","completion_window":"24h"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	var created openai.Batch
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
	if processed, err := h.ProcessBatchItems(t.Context()); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	done := authorizedFileRequest(t, routes, http.MethodGet, "/v1/batches/"+created.ID)
	var batch openai.Batch
	if err := json.Unmarshal(done.Body.Bytes(), &batch); err != nil || batch.RequestCounts.Failed != 1 || batch.ErrorFileID == "" {
		t.Fatalf("status=%d batch=%+v err=%v", done.Code, batch, err)
	}
	errorsFile, err := files.Get(t.Context(), owner, batch.ErrorFileID, true)
	if err != nil || !strings.Contains(string(errorsFile.Content), `"code":"batch_result_too_large"`) {
		t.Fatalf("errors=%s err=%v", errorsFile.Content, err)
	}
}

func TestBatchLifecycleExecutesOCRWithOwnerScopedFileResolution(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	runtime := &batchProvider{models: []string{"ocr-model"}}
	rates := &embeddingTokenRateStore{}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	document := []byte("%PDF-1.7\ntext")
	files.files["file_document"] = filestate.File{ID: "file_document", OwnerKey: owner, Filename: "document.pdf", Purpose: "assistants", ContentType: "application/pdf", Bytes: int64(len(document)), Content: document}
	payload := []byte("{\"custom_id\":\"ocr\",\"method\":\"POST\",\"url\":\"/v1/ocr\",\"body\":{\"model\":\"ocr-model\",\"document\":{\"type\":\"file\",\"file_id\":\"file_document\"},\"pages\":[0]}}\n")
	files.files["file_ocr"] = filestate.File{ID: "file_ocr", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
	h := NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), runtime, rates).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store)
	routes := Routes(h)
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_ocr","endpoint":"/v1/ocr","completion_window":"24h"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	var created openai.Batch
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
	if processed, err := h.ProcessBatchItems(t.Context()); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	reservedTokens := rates.tokens
	done := authorizedFileRequest(t, routes, http.MethodGet, "/v1/batches/"+created.ID)
	var batch openai.Batch
	if err := json.Unmarshal(done.Body.Bytes(), &batch); err != nil || batch.RequestCounts.Completed != 1 || batch.OutputFileID == "" {
		t.Fatalf("status=%d batch=%+v err=%v", done.Code, batch, err)
	}
	output, err := files.Get(t.Context(), owner, batch.OutputFileID, true)
	if err != nil || !strings.Contains(string(output.Content), `"pages_processed":1`) || !strings.Contains(string(output.Content), `"markdown":"text"`) {
		t.Fatalf("output=%s err=%v", output.Content, err)
	}
	runtime.mu.Lock()
	ocrRequest := runtime.ocr
	runtime.mu.Unlock()
	if ocrRequest.OCRRequest == nil || ocrRequest.RequestID == "" || ocrRequest.InputPages != 1 || ocrRequest.OCRRequest.Document.Type != "document_url" || !strings.HasPrefix(ocrRequest.OCRRequest.Document.DocumentURL, "data:application/pdf;base64,") || ocrRequest.OCRRequest.Document.FileID != "" || reservedTokens != ocrRequest.OCRRequest.InputTokens() {
		t.Fatalf("request=%+v pages=%d TPM=%d", ocrRequest.OCRRequest, ocrRequest.InputPages, reservedTokens)
	}
}

func TestBatchOCRDoesNotResolveAnotherOwnersFile(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	foreign := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "other"})
	document := []byte("%PDF-1.7\ntext")
	files.files["file_foreign_document"] = filestate.File{ID: "file_foreign_document", OwnerKey: foreign, Filename: "document.pdf", Purpose: "assistants", ContentType: "application/pdf", Bytes: int64(len(document)), Content: document}
	payload := []byte("{\"custom_id\":\"ocr\",\"method\":\"POST\",\"url\":\"/v1/ocr\",\"body\":{\"model\":\"ocr-model\",\"document\":{\"type\":\"file\",\"file_id\":\"file_foreign_document\"}}}\n")
	files.files["file_ocr_foreign"] = filestate.File{ID: "file_ocr_foreign", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
	h := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), &batchProvider{models: []string{"ocr-model"}}).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_ocr_foreign","endpoint":"/v1/ocr","completion_window":"24h"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	Routes(h).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"file_not_found"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBatchRejectsRequestCountThatCannotFitBoundedResults(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	payload := []byte("{\"custom_id\":\"one\",\"method\":\"POST\",\"url\":\"/v1/audio/speech\",\"body\":{\"model\":\"tts\",\"input\":\"one\",\"voice\":\"alloy\"}}\n{\"custom_id\":\"two\",\"method\":\"POST\",\"url\":\"/v1/audio/speech\",\"body\":{\"model\":\"tts\",\"input\":\"two\",\"voice\":\"alloy\"}}\n")
	files.files["file_crowded"] = filestate.File{ID: "file_crowded", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
	h := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), &batchProvider{models: []string{"tts"}}).WithFileStore(files, FileRuntimeConfig{MaxBytes: batchResultMinLine, OwnerQuotaBytes: 4096}).WithBatchStore(store, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_crowded","endpoint":"/v1/audio/speech","completion_window":"24h"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	Routes(h).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_batch_file"`) || !strings.Contains(response.Body.String(), "configured output file limit") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBatchRejectsInvalidJSONLAndCrossOwnerInput(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	foreign := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "other"})
	files.files["file_duplicate"] = filestate.File{ID: "file_duplicate", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: 300, Content: []byte("{\"custom_id\":\"same\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"model-a\",\"messages\":[]}}\n{\"custom_id\":\"same\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"model-a\",\"messages\":[]}}\n")}
	files.files["file_foreign"] = filestate.File{ID: "file_foreign", OwnerKey: foreign, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: 2, Content: []byte("{}")}
	routes := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), &batchProvider{models: []string{"model-a"}}).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store))
	for _, test := range []struct {
		id     string
		status int
	}{{"file_duplicate", http.StatusBadRequest}, {"file_foreign", http.StatusNotFound}} {
		request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"`+test.id+`","endpoint":"/v1/chat/completions","completion_window":"24h"}`))
		request.Header.Set("Authorization", "Bearer key")
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("file=%s status=%d body=%s", test.id, response.Code, response.Body.String())
		}
	}
}

func TestBatchRejectsStreamingImageGeneration(t *testing.T) {
	_, _, _, err := validateBatchBody("/v1/images/generations", []byte(`{"model":"image-model","prompt":"draw","stream":true}`))
	if err == nil || !strings.Contains(err.Error(), "not supported for batch") {
		t.Fatalf("err=%v", err)
	}
}

func TestBatchRejectsInvalidOutputExpiration(t *testing.T) {
	files := &memoryFileStore{files: map[string]filestate.File{}}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	files.files["file_input"] = filestate.File{ID: "file_input", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: 3, Content: []byte("{}\n")}
	routes := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), &batchProvider{models: []string{"model-a"}}).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(newMemoryBatchStore(), newMemoryBatchStore()))
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_input","endpoint":"/v1/chat/completions","completion_window":"24h","output_expires_after":{"anchor":"created_at","seconds":3599}}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "output_expires_after") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBatchWorkerExpiresJobsBeforeProviderExecution(t *testing.T) {
	store := newMemoryBatchStore()
	provider := &batchProvider{models: []string{"model-a"}}
	owner := "owner"
	batch := batchstate.Batch{ID: "batch_expired", OwnerKey: owner, Status: "queued", ExpiresAt: time.Now().Add(-time.Second)}
	store.batches[batch.ID] = batch
	store.items[batch.ID] = map[int]batchstate.Item{0: {BatchID: batch.ID, OwnerKey: owner, Ordinal: 0, State: "pending", ExecutionID: "exec", Identity: []byte(`{}`)}}
	payload, _ := json.Marshal(batchJob{Ordinal: 0})
	store.jobs[batch.ID+":0"] = asyncstate.Job{Kind: batchJobKind, ResourceID: batch.ID + ":0", OwnerKey: owner, EndpointID: "gateway", ExecutionID: "exec", Payload: payload}
	h := NewHandler(modules.NewPipeline(nil), provider).WithBatchStore(store, store)
	processed, err := h.ProcessBatchItems(t.Context())
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	expired, _ := store.GetBatch(t.Context(), owner, batch.ID)
	if expired.Status != "expired" || len(store.jobs) != 0 || len(provider.executions) != 0 {
		t.Fatalf("batch=%+v jobs=%d executions=%v", expired, len(store.jobs), provider.executions)
	}
}

func TestBatchWorkerReportsCauseAfterSchedulingRetry(t *testing.T) {
	store := newMemoryBatchStore()
	store.jobs["invalid"] = asyncstate.Job{
		Kind: batchJobKind, ResourceID: "invalid", OwnerKey: "owner", EndpointID: "gateway", ExecutionID: "exec", Payload: []byte(`{`),
	}
	h := NewHandler(modules.NewPipeline(nil), &batchProvider{}).WithBatchStore(store, store)

	processed, err := h.ProcessBatchItems(t.Context())
	if processed != 1 || err == nil || !strings.Contains(err.Error(), "invalid batch job payload") {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	store.mu.Lock()
	retried, retained := store.jobs["invalid"]
	store.mu.Unlock()
	if !retained || retried.Attempts != 1 || retried.LeaseGeneration != 0 {
		t.Fatalf("retry was not retained: %+v retained=%t", retried, retained)
	}
}
