package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/batchstate"
	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/containerstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	providerpkg "ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/vectorstate"
)

type memoryBatchStore struct {
	mu      sync.Mutex
	batches map[string]batchstate.Batch
	items   map[string]map[int]batchstate.Item
	jobs    map[string]asyncstate.Job
}

func TestBatchIdentityPreservesAnonymizationPolicy(t *testing.T) {
	runtime := providerpkg.New(providerpkg.Config{GuardrailPolicies: map[string]config.GuardrailPolicyConfig{
		"batch-pii": {Anonymization: "custom", AnonymizationRules: []string{"email"}},
	}})
	registry := NewAccessRegistry()
	if _, err := registry.PutPolicyAttachment("batch-pii", PolicyAttachment{PolicyName: "batch-pii", Scope: "specific", Tags: []string{"batch"}, Models: []string{"gpt-*"}, Providers: []string{"azure"}}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(modules.NewPipeline(nil), runtime).WithAccessRegistry(registry)
	payload := []byte("{\"custom_id\":\"item-1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"gpt-test\",\"messages\":[{\"role\":\"user\",\"content\":\"user@example.com\"}]}}\n")
	items, err := handler.decodeBatchItems(httptest.NewRecorder(), context.Background(), modules.RequestContext{CredentialID: "key", AllowedModels: []string{"*"}, Tags: []string{"batch"}}, "/v1/chat/completions", payload)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	var identity modules.RequestContext
	if err := json.Unmarshal(items[0].Identity, &identity); err != nil {
		t.Fatal(err)
	}
	var deferred []providerpkg.EndpointPolicyAttachment
	if err := json.Unmarshal([]byte(identity.Metadata[providerpkg.EndpointPolicyAttachmentsMetadataKey]), &deferred); err != nil || len(deferred) != 1 || deferred[0].Anonymization != "custom" || strings.Join(deferred[0].Models, ",") != "gpt-*" {
		t.Fatalf("deferred=%+v err=%v metadata=%+v", deferred, err, identity.Metadata)
	}
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
func (s *memoryBatchStore) ListBatches(_ context.Context, owner string, limit int, after string) ([]batchstate.Batch, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []batchstate.Batch
	for _, b := range s.batches {
		if b.OwnerKey == owner {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if after != "" {
		index := sort.Search(len(out), func(index int) bool { return out[index].ID >= after })
		if index == len(out) || out[index].ID != after {
			return nil, "", batchstate.ErrNotFound
		}
		out = out[index+1:]
	}
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
func (s *memoryBatchStore) ListBatchItems(_ context.Context, owner, id string) ([]batchstate.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch, ok := s.batches[id]; !ok || batch.OwnerKey != owner {
		return nil, batchstate.ErrNotFound
	}
	items := make([]batchstate.Item, 0, len(s.items[id]))
	for _, item := range s.items[id] {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Ordinal < items[j].Ordinal })
	return items, nil
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
func (s *memoryBatchStore) DeleteBatch(_ context.Context, owner, id string) (batchstate.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[id]
	if !ok || b.OwnerKey != owner {
		return batchstate.Batch{}, batchstate.ErrNotFound
	}
	switch b.Status {
	case "completed", "failed", "expired", "cancelled":
	default:
		return batchstate.Batch{}, batchstate.ErrConflict
	}
	delete(s.batches, id)
	delete(s.items, id)
	for key := range s.jobs {
		if strings.HasPrefix(key, id+":") {
			delete(s.jobs, key)
		}
	}
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
	chat       modules.RequestContext
	speech     modules.RequestContext
	speechData []byte
	audio      modules.RequestContext
	audioOp    string
	imageEdit  modules.RequestContext
	variation  modules.RequestContext
	compact    modules.RequestContext
	ocr        modules.RequestContext
}

func (p *batchProvider) ChatCompletions(_ context.Context, req modules.RequestContext) (openai.ChatCompletionResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.chat = req
	p.mu.Unlock()
	return openai.ChatCompletionResponse{ID: "chat_" + req.RequestID, Object: "chat.completion", Model: req.Request.Model, Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}, Usage: openai.Usage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4}}, nil
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

func (p *batchProvider) EditImage(_ context.Context, req modules.RequestContext) (openai.ImageGenerationResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.imageEdit = req
	p.mu.Unlock()
	return openai.ImageGenerationResponse{Created: 8, Data: []openai.ImageData{{URL: "https://images.example/edited.png"}}, Usage: &openai.ImageUsage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8}}, nil
}

func (p *batchProvider) CreateImageVariation(_ context.Context, req modules.RequestContext) (openai.ImageGenerationResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.variation = req
	p.mu.Unlock()
	return openai.ImageGenerationResponse{Created: 9, Data: []openai.ImageData{{URL: "https://images.example/variation.png"}}, Usage: &openai.ImageUsage{InputTokens: 2, OutputTokens: 5, TotalTokens: 7}}, nil
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

func (p *batchProvider) TranscribeAudio(_ context.Context, req modules.RequestContext) (openai.AudioTranscriptionResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.audio = req
	p.audioOp = "transcription"
	p.mu.Unlock()
	usage := openai.AudioTranscriptionUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6}
	return openai.AudioTranscriptionResponse{Text: "transcribed", Usage: &usage}, nil
}

func (p *batchProvider) TranslateAudio(_ context.Context, req modules.RequestContext) (openai.AudioTranscriptionResponse, error) {
	p.mu.Lock()
	p.executions = append(p.executions, req.RequestID)
	p.audio = req
	p.audioOp = "translation"
	p.mu.Unlock()
	usage := openai.AudioTranscriptionUsage{InputTokens: 5, OutputTokens: 3, TotalTokens: 8}
	return openai.AudioTranscriptionResponse{Text: "translated", Usage: &usage}, nil
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

func TestBatchResponsesRequireOwnedBuiltInToolResources(t *testing.T) {
	for _, test := range []struct {
		name       string
		storeOwner string
		wantStatus int
	}{
		{name: "owned", storeOwner: fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"}), wantStatus: http.StatusOK},
		{name: "foreign", storeOwner: "foreign", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryBatchStore()
			files := &memoryFileStore{files: map[string]filestate.File{}}
			owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
			payload := []byte(`{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"model-a","input":"search","tools":[{"type":"file_search","vector_store_ids":["vs_owned"]}]}}` + "\n")
			files.files["file_input"] = filestate.File{ID: "file_input", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
			vectors := &memoryVectorStore{stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: test.storeOwner}}, files: map[string]vectorstate.File{}}
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"model-a"}, allowedTools: []string{"file_search"}}}), &batchProvider{models: []string{"model-a"}}).
				WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).
				WithVectorStore(vectors, VectorStoreRuntimeConfig{OwnerQuota: 10}).
				WithBatchStore(store, store))
			request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_input","endpoint":"/v1/responses","completion_window":"24h"}`))
			request.Header.Set("Authorization", "Bearer key")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBatchResponsesPersistReusableContainerBinding(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	payload := []byte(`{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"model-a","input":"continue","tools":[{"type":"code_interpreter","container":"cntr_owned"}]}}` + "\n")
	files.files["file_input"] = filestate.File{ID: "file_input", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(payload)), Content: payload}
	binding := providerpkg.ContainerBinding{Endpoint: "bound", Model: "model-a", Deployment: "deployment-v1"}
	record := containerstate.Record{OwnerKey: owner, Binding: binding, Container: openai.Container{ID: "cntr_owned"}}
	containers := &memoryContainerStore{records: map[string]containerstate.Record{containerKey(owner, "cntr_owned"): record}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"model-a"}, allowedTools: []string{"code_interpreter"}}}), &batchProvider{models: []string{"model-a"}}).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).
		WithContainerStore(containers).
		WithBatchStore(store, store))
	request := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_input","endpoint":"/v1/responses","completion_window":"24h"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var created openai.Batch
	if json.Unmarshal(response.Body.Bytes(), &created) != nil {
		t.Fatal("invalid batch response")
	}
	store.mu.Lock()
	item := store.items[created.ID][0]
	store.mu.Unlock()
	var itemIdentity modules.RequestContext
	if json.Unmarshal(item.Identity, &itemIdentity) != nil || itemIdentity.Metadata[modules.MetadataResponseContainerEndpoint] != binding.Endpoint || itemIdentity.Metadata[modules.MetadataResponseContainerDeployment] != binding.Deployment {
		t.Fatalf("container binding was not persisted: %+v", itemIdentity.Metadata)
	}
}

func TestBatchLifecycleExecutesMessagesWithNativeResponse(t *testing.T) {
	store := newMemoryBatchStore()
	files := &memoryFileStore{files: map[string]filestate.File{}}
	runtime := &batchProvider{models: []string{"message-model"}}
	rates := &embeddingTokenRateStore{}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	request := messagesRequest{
		Model: "message-model", MaxTokens: 32,
		System:   json.RawMessage(`"Be concise"`),
		Messages: []messagesInput{{Role: "user", Content: json.RawMessage(`[{"type":"image","source":{"type":"url","url":"https://images.example/chart.png"}},{"type":"image","source":{"type":"file","file_id":"file_image"}},{"type":"text","text":"hello"},{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjcKY29udGVudA=="},"title":"PDF report","context":"Audited","citations":{"enabled":true}},{"type":"document","source":{"type":"file","file_id":"file_document"},"title":"Text report","context":"Internal","citations":{"enabled":true}},{"type":"document","source":{"type":"url","url":"https://documents.example/remote.pdf"},"title":"Remote report","citations":{"enabled":true}}]`)}},
	}
	requestBody, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(openai.BatchRequestLine{CustomID: "message", Method: http.MethodPost, URL: "/v1/messages", Body: requestBody})
	if err != nil {
		t.Fatal(err)
	}
	line = append(line, '\n')
	files.files["file_messages"] = filestate.File{ID: "file_messages", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(line)), Content: line}
	document := []byte("Quarterly revenue is 42.")
	files.files["file_document"] = filestate.File{ID: "file_document", OwnerKey: owner, Filename: "report.txt", Purpose: "user_data", ContentType: "text/plain", Bytes: int64(len(document)), Content: document}
	image := []byte("\x89PNG\r\n\x1a\nimage")
	files.files["file_image"] = filestate.File{ID: "file_image", OwnerKey: owner, Filename: "chart.png", Purpose: "user_data", ContentType: "image/png", Bytes: int64(len(image)), Content: image}
	h := NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), runtime, rates).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store)
	fetches := 0
	h.a2aHTTPClient = a2aHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		fetches++
		if strings.HasSuffix(request.URL.Path, ".png") {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\nimage"))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/pdf"}}, Body: io.NopCloser(strings.NewReader("%PDF-1.7\nremote"))}, nil
	})
	routes := Routes(h)
	createBody, _ := json.Marshal(openai.BatchCreateRequest{InputFileID: "file_messages", Endpoint: "/v1/messages", CompletionWindow: openai.BatchCompletionWindow})
	create := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(string(createBody)))
	create.Header.Set("Authorization", "Bearer key")
	createdResponse := httptest.NewRecorder()
	routes.ServeHTTP(createdResponse, create)
	var created openai.Batch
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil || createdResponse.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s err=%v", createdResponse.Code, createdResponse.Body.String(), err)
	}
	files.mu.Lock()
	delete(files.files, "file_document")
	delete(files.files, "file_image")
	files.mu.Unlock()
	h.a2aHTTPClient = a2aHTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("remote document must have been snapshotted")
	})
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
	if err != nil || !strings.Contains(string(output.Content), `"type":"message"`) || !strings.Contains(string(output.Content), `"stop_reason":"end_turn"`) || !strings.Contains(string(output.Content), `"input_tokens":3`) {
		t.Fatalf("output=%s err=%v", output.Content, err)
	}
	runtime.mu.Lock()
	providerRequest := runtime.chat
	runtime.mu.Unlock()
	attachments, attachmentErr := openai.ChatFileAttachments(providerRequest.Request.Messages)
	images, imageErr := openai.ChatImageAttachments(providerRequest.Request.Messages)
	var citations []bool
	var metadata []openai.DocumentMetadata
	for _, message := range providerRequest.Request.Messages {
		if len(message.AnthropicDocumentCitations) > 0 {
			citations = message.AnthropicDocumentCitations
			metadata = message.AnthropicDocumentMetadata
		}
	}
	if providerRequest.RequestID == "" || providerRequest.Metadata["gateway.api_type"] != "messages" || reservedTokens != estimateChatTokens(providerRequest.Request) || attachmentErr != nil || len(attachments) != 2 || imageErr != nil || len(images) != 2 || !openai.HasChatTextDocuments(providerRequest.Request) || len(citations) != 3 || !citations[0] || !citations[1] || !citations[2] || len(metadata) != 3 || metadata[0].Title != "PDF report" || metadata[1].Title != "Text report" || metadata[2].Title != "Remote report" || fetches != 2 {
		t.Fatalf("request=%+v metadata=%v TPM=%d want=%d", providerRequest.Request, providerRequest.Metadata, reservedTokens, estimateChatTokens(providerRequest.Request))
	}
}

func TestBatchMessagesRejectsStreaming(t *testing.T) {
	body := []byte(`{"model":"message-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	if _, _, _, err := validateBatchBody("/v1/messages", body); err == nil || !strings.Contains(err.Error(), "not supported in batches") {
		t.Fatalf("err=%v", err)
	}
}

func TestBatchMessagesIncludesSkillExecutionInToolPolicy(t *testing.T) {
	body := []byte(`{"model":"message-model","max_tokens":32,"container":{"skills":[{"type":"custom","skill_id":"skill_owned","version":"v1"}]},"tools":[{"type":"code_execution_20250825","name":"code_execution"}],"messages":[{"role":"user","content":"hello"}]}`)
	_, model, tools, err := validateBatchBody("/v1/messages", body)
	if err != nil || model != "message-model" || len(tools) != 2 || tools[0] != "skill:skill_owned" || tools[1] != "code_execution" {
		t.Fatalf("model=%q tools=%v err=%v", model, tools, err)
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

func TestBatchRejectsInvalidResponseEnvelope(t *testing.T) {
	for _, body := range []string{
		`{"input":"hello"}`,
		`{"model":"model"}`,
		`{"model":"model","input":[]}`,
		`{"model":"model","input":42}`,
	} {
		if _, _, _, err := validateBatchBody("/v1/responses", []byte(body)); err == nil {
			t.Fatalf("invalid response request accepted: %s", body)
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

func TestBatchLifecycleExecutesImageEditAndVariation(t *testing.T) {
	image := openai.ImageAttachment{MediaType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))}
	edit := openai.ImageEditRequest{Model: "image-model", Prompt: "remove background", Images: []openai.ImageAttachment{image}, ResponseFormat: "url"}
	variation := openai.ImageVariationRequest{Model: "image-model", Image: image, ResponseFormat: "url"}
	for _, test := range []struct {
		name     string
		endpoint string
		body     any
		wantURL  string
		reserve  int
		stored   func(*batchProvider) modules.RequestContext
	}{
		{name: "edit", endpoint: "/v1/images/edits", body: edit, wantURL: "edited.png", reserve: openai.ImageEditReserveTokens(edit), stored: func(p *batchProvider) modules.RequestContext { return p.imageEdit }},
		{name: "variation", endpoint: "/v1/images/variations", body: variation, wantURL: "variation.png", reserve: openai.ImageVariationReserveTokens(variation), stored: func(p *batchProvider) modules.RequestContext { return p.variation }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryBatchStore()
			files := &memoryFileStore{files: map[string]filestate.File{}}
			runtime := &batchProvider{models: []string{"image-model"}}
			rates := &embeddingTokenRateStore{}
			owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
			requestBody, err := json.Marshal(test.body)
			if err != nil {
				t.Fatal(err)
			}
			line, err := json.Marshal(openai.BatchRequestLine{CustomID: test.name, Method: http.MethodPost, URL: test.endpoint, Body: requestBody})
			if err != nil {
				t.Fatal(err)
			}
			line = append(line, '\n')
			files.files["file_image"] = filestate.File{ID: "file_image", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(line)), Content: line}
			h := NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), runtime, rates).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store)
			routes := Routes(h)
			createBody, _ := json.Marshal(openai.BatchCreateRequest{InputFileID: "file_image", Endpoint: test.endpoint, CompletionWindow: openai.BatchCompletionWindow})
			create := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(string(createBody)))
			create.Header.Set("Authorization", "Bearer key")
			createdResponse := httptest.NewRecorder()
			routes.ServeHTTP(createdResponse, create)
			var created openai.Batch
			if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil || createdResponse.Code != http.StatusOK {
				t.Fatalf("create status=%d body=%s err=%v", createdResponse.Code, createdResponse.Body.String(), err)
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
			if err != nil || !strings.Contains(string(output.Content), test.wantURL) || !strings.Contains(string(output.Content), `"total_tokens":`) {
				t.Fatalf("output=%s err=%v", output.Content, err)
			}
			runtime.mu.Lock()
			stored := test.stored(runtime)
			runtime.mu.Unlock()
			if stored.RequestID == "" || reservedTokens != test.reserve {
				t.Fatalf("request=%+v TPM=%d want=%d", stored, reservedTokens, test.reserve)
			}
			if test.name == "edit" && stored.ImageEditRequest == nil || test.name == "variation" && stored.ImageVariationRequest == nil {
				t.Fatalf("typed request missing: %+v", stored)
			}
		})
	}
}

func TestBatchImageEditRejectsStreaming(t *testing.T) {
	image := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))
	body := []byte(`{"model":"image","prompt":"edit","images":[{"media_type":"image/png","data_base64":"` + image + `"}],"stream":true}`)
	if _, _, _, err := validateBatchBody("/v1/images/edits", body); err == nil || !strings.Contains(err.Error(), "not supported in batches") {
		t.Fatalf("err=%v", err)
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

func TestBatchLifecycleExecutesAudioTranscriptionAndTranslation(t *testing.T) {
	audio := openai.AudioAttachment{
		Filename:  "sample.wav",
		MediaType: "audio/wav",
		Data:      base64.StdEncoding.EncodeToString([]byte("RIFF\x04\x00\x00\x00WAVE")),
	}
	for _, test := range []struct {
		name     string
		endpoint string
		wantText string
		wantOp   string
		wantType string
	}{
		{name: "transcription", endpoint: "/v1/audio/transcriptions", wantText: "transcribed", wantOp: "transcription", wantType: "batch"},
		{name: "translation", endpoint: "/v1/audio/translations", wantText: "translated", wantOp: "translation", wantType: "audio_translation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryBatchStore()
			files := &memoryFileStore{files: map[string]filestate.File{}}
			runtime := &batchProvider{models: []string{"audio-model"}}
			rates := &embeddingTokenRateStore{}
			owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
			requestBody, err := json.Marshal(openai.AudioTranscriptionRequest{Model: "audio-model", File: audio, ResponseFormat: "json"})
			if err != nil {
				t.Fatal(err)
			}
			line, err := json.Marshal(openai.BatchRequestLine{CustomID: test.name, Method: http.MethodPost, URL: test.endpoint, Body: requestBody})
			if err != nil {
				t.Fatal(err)
			}
			line = append(line, '\n')
			files.files["file_audio"] = filestate.File{ID: "file_audio", OwnerKey: owner, Filename: "input.jsonl", Purpose: "batch", ContentType: "application/jsonl", Bytes: int64(len(line)), Content: line}
			h := NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), runtime, rates).WithFileStore(files, FileRuntimeConfig{MaxBytes: 4 << 20, OwnerQuotaBytes: 64 << 20}).WithBatchStore(store, store)
			routes := Routes(h)
			createBody, _ := json.Marshal(openai.BatchCreateRequest{InputFileID: "file_audio", Endpoint: test.endpoint, CompletionWindow: openai.BatchCompletionWindow})
			create := httptest.NewRequest(http.MethodPost, "/v1/batches", strings.NewReader(string(createBody)))
			create.Header.Set("Authorization", "Bearer key")
			createdResponse := httptest.NewRecorder()
			routes.ServeHTTP(createdResponse, create)
			var created openai.Batch
			if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil || createdResponse.Code != http.StatusOK {
				t.Fatalf("create status=%d body=%s err=%v", createdResponse.Code, createdResponse.Body.String(), err)
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
			if err != nil || !strings.Contains(string(output.Content), `"text":"`+test.wantText+`"`) || !strings.Contains(string(output.Content), `"total_tokens":`) {
				t.Fatalf("output=%s err=%v", output.Content, err)
			}
			runtime.mu.Lock()
			audioRequest, operation := runtime.audio, runtime.audioOp
			runtime.mu.Unlock()
			wantTokens := openai.AudioTranscriptionReserveTokens(openai.AudioTranscriptionRequest{Model: "audio-model", File: audio, ResponseFormat: "json"})
			if audioRequest.AudioTranscriptionRequest == nil || audioRequest.RequestID == "" || audioRequest.Metadata["gateway.api_type"] != test.wantType || operation != test.wantOp || reservedTokens != wantTokens {
				t.Fatalf("request=%+v metadata=%v operation=%q TPM=%d want=%d", audioRequest.AudioTranscriptionRequest, audioRequest.Metadata, operation, reservedTokens, wantTokens)
			}
		})
	}
}

func TestBatchAudioTranscriptionRejectsStreaming(t *testing.T) {
	audio := base64.StdEncoding.EncodeToString([]byte("RIFF\x04\x00\x00\x00WAVE"))
	for _, endpoint := range []string{"/v1/audio/transcriptions", "/v1/audio/translations"} {
		body := []byte(`{"model":"audio","file":{"filename":"sample.wav","media_type":"audio/wav","data_base64":"` + audio + `"},"stream":true}`)
		if _, _, _, err := validateBatchBody(endpoint, body); err == nil || !strings.Contains(err.Error(), "not supported in batches") {
			t.Fatalf("endpoint=%s err=%v", endpoint, err)
		}
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
