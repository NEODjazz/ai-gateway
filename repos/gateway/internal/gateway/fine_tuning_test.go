package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/finetunestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type memoryFineTuningStore struct {
	mu        sync.Mutex
	records   map[string]finetunestate.Record
	createErr error
}

type fineTuningBillingModule struct {
	phases         []string
	trainingTokens []int
	apiTypes       []string
	reserveErr     error
	commitErr      error
}

func (*fineTuningBillingModule) Name() string   { return "billing" }
func (*fineTuningBillingModule) Required() bool { return true }
func (m *fineTuningBillingModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.record("reserve", req)
	return m.reserveErr
}
func (*fineTuningBillingModule) PostResponseEnabled() bool { return true }
func (m *fineTuningBillingModule) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.record("commit", req)
	return m.commitErr
}
func (m *fineTuningBillingModule) HandleFailure(_ context.Context, req *modules.RequestContext, _ error) error {
	m.record("cancel", req)
	return nil
}
func (m *fineTuningBillingModule) record(phase string, req *modules.RequestContext) {
	m.phases = append(m.phases, phase)
	m.trainingTokens = append(m.trainingTokens, req.TrainingTokens)
	m.apiTypes = append(m.apiTypes, req.Metadata["gateway.api_type"])
}

func (s *memoryFineTuningStore) CreateFineTuningRecord(_ context.Context, record finetunestate.Record, _ int) (finetunestate.Record, error) {
	if s.createErr != nil {
		return finetunestate.Record{}, s.createErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.records[record.Job.ID]; found {
		return finetunestate.Record{}, finetunestate.ErrConflict
	}
	s.records[record.Job.ID] = record
	return record, nil
}

func (s *memoryFineTuningStore) GetFineTuningRecord(_ context.Context, owner, id string) (finetunestate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.records[id]
	if !found || record.OwnerKey != owner {
		return finetunestate.Record{}, finetunestate.ErrNotFound
	}
	return record, nil
}

func (s *memoryFineTuningStore) ListFineTuningRecords(_ context.Context, owner string, limit int, after string) ([]finetunestate.Record, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]finetunestate.Record, 0, limit)
	for id, record := range s.records {
		if record.OwnerKey == owner && id != after && len(result) < limit {
			result = append(result, record)
		}
	}
	return result, "", nil
}

func (s *memoryFineTuningStore) UpdateFineTuningRecord(_ context.Context, owner string, job openai.FineTuningJob) (finetunestate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.records[job.ID]
	if !found || record.OwnerKey != owner {
		return finetunestate.Record{}, finetunestate.ErrNotFound
	}
	record.Job = job
	s.records[job.ID] = record
	return record, nil
}

func (s *memoryFineTuningStore) FindFineTuningRecordByModel(_ context.Context, owner, model string) (finetunestate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.OwnerKey == owner && record.Job.FineTunedModel != nil && *record.Job.FineTunedModel == model {
			return record, nil
		}
	}
	return finetunestate.Record{}, finetunestate.ErrNotFound
}

type gatewayFineTuningProvider struct {
	*batchProvider
	mu          sync.Mutex
	actions     []string
	lastBinding provider.FineTuningBinding
}

func (p *gatewayFineTuningProvider) job(status string) openai.FineTuningJob {
	return openai.FineTuningJob{ID: "ftjob_1", Object: "fine_tuning.job", Model: "model-a", TrainingFile: "file_train", Status: status, ResultFiles: []string{}}
}

func (p *gatewayFineTuningProvider) record(action string, binding provider.FineTuningBinding) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.actions = append(p.actions, action)
	p.lastBinding = binding
}

func (p *gatewayFineTuningProvider) CreateFineTuningJob(ctx context.Context, identity modules.RequestContext, input openai.FineTuningCreateRequest, admit func(context.Context, *modules.RequestContext) error) (openai.FineTuningJob, provider.FineTuningBinding, error) {
	binding := provider.FineTuningBinding{Endpoint: "primary", Model: input.Model, Deployment: "deployment-hash"}
	identity.Request.Model = input.Model
	identity.Metadata["gateway.api_type"] = "fine_tuning"
	if admit != nil {
		if err := admit(ctx, &identity); err != nil {
			return openai.FineTuningJob{}, provider.FineTuningBinding{}, err
		}
	}
	p.record("create", binding)
	job := p.job("validating_files")
	job.Model, job.TrainingFile = input.Model, input.TrainingFile
	return job, binding, nil
}

func (p *gatewayFineTuningProvider) RetrieveFineTuningJob(_ context.Context, binding provider.FineTuningBinding, _ string) (openai.FineTuningJob, error) {
	p.record("retrieve", binding)
	return p.job("running"), nil
}

func (p *gatewayFineTuningProvider) CancelFineTuningJob(_ context.Context, binding provider.FineTuningBinding, _ string) (openai.FineTuningJob, error) {
	p.record("cancel", binding)
	return p.job("cancelled"), nil
}

func (p *gatewayFineTuningProvider) PauseFineTuningJob(_ context.Context, binding provider.FineTuningBinding, _ string) (openai.FineTuningJob, error) {
	p.record("pause", binding)
	return p.job("paused"), nil
}

func (p *gatewayFineTuningProvider) ResumeFineTuningJob(_ context.Context, binding provider.FineTuningBinding, _ string) (openai.FineTuningJob, error) {
	p.record("resume", binding)
	return p.job("running"), nil
}

func (p *gatewayFineTuningProvider) ListFineTuningEvents(_ context.Context, binding provider.FineTuningBinding, _ string, options provider.FineTuningListOptions) (openai.FineTuningEventList, error) {
	p.record("events", binding)
	return openai.FineTuningEventList{Object: "list", Data: []openai.FineTuningEvent{{ID: options.After, Object: "fine_tuning.job.event", Level: "info", Message: "running"}}}, nil
}

func (p *gatewayFineTuningProvider) ListFineTuningCheckpoints(_ context.Context, binding provider.FineTuningBinding, id string, _ provider.FineTuningListOptions) (openai.FineTuningCheckpointList, error) {
	p.record("checkpoints", binding)
	return openai.FineTuningCheckpointList{Object: "list", Data: []openai.FineTuningCheckpoint{{ID: "ftckpt_1", Object: "fine_tuning.job.checkpoint", FineTuningJobID: id, FineTunedModel: "model-a:checkpoint"}}}, nil
}

func (p *gatewayFineTuningProvider) DeleteFineTunedModel(_ context.Context, binding provider.FineTuningBinding, model string) (openai.ModelDeletion, error) {
	p.record("delete_model", binding)
	return openai.ModelDeletion{ID: model, Object: "model", Deleted: true}, nil
}

func fineTuningTestHandler(store *memoryFineTuningStore, files *memoryFileStore, runtime *gatewayFineTuningProvider, allowed ...string) http.Handler {
	return Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: allowed}}), runtime).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 1 << 20, OwnerQuotaBytes: 4 << 20}).
		WithFineTuningStore(store))
}

func fineTuningRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestFineTuningOwnedLifecycle(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	files := &memoryFileStore{files: map[string]filestate.File{
		"file_train": {ID: "file_train", OwnerKey: owner, Purpose: "fine-tune"},
		"file_valid": {ID: "file_valid", OwnerKey: owner, Purpose: "fine-tune"},
	}}
	store := &memoryFineTuningStore{records: map[string]finetunestate.Record{}}
	runtime := &gatewayFineTuningProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	handler := fineTuningTestHandler(store, files, runtime, "model-a")

	created := fineTuningRequest(t, handler, http.MethodPost, "/v1/fine_tuning/jobs", `{"model":"model-a","training_file":"file_train","validation_file":"file_valid"}`)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"id":"ftjob_1"`) {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	for _, call := range []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodGet, "/v1/fine_tuning/jobs", `"has_more":false`},
		{http.MethodGet, "/v1/fine_tuning/jobs/ftjob_1", `"status":"running"`},
		{http.MethodPost, "/v1/fine_tuning/jobs/ftjob_1/pause", `"status":"paused"`},
		{http.MethodPost, "/v1/fine_tuning/jobs/ftjob_1/resume", `"status":"running"`},
		{http.MethodGet, "/v1/fine_tuning/jobs/ftjob_1/events?after=event_1&limit=5", `"id":"event_1"`},
		{http.MethodGet, "/v1/fine_tuning/jobs/ftjob_1/checkpoints?limit=5", `"id":"ftckpt_1"`},
		{http.MethodPost, "/v1/fine_tuning/jobs/ftjob_1/cancel", `"status":"cancelled"`},
	} {
		response := fineTuningRequest(t, handler, call.method, call.path, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), call.want) {
			t.Fatalf("%s %s status=%d body=%s", call.method, call.path, response.Code, response.Body.String())
		}
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.lastBinding.Deployment != "deployment-hash" {
		t.Fatalf("lifecycle binding=%+v", runtime.lastBinding)
	}
}

func TestFineTuningRejectsUnownedWrongPurposeAndUnauthorizedModel(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	files := &memoryFileStore{files: map[string]filestate.File{
		"file_batch":   {ID: "file_batch", OwnerKey: owner, Purpose: "batch"},
		"file_foreign": {ID: "file_foreign", OwnerKey: "different-owner", Purpose: "fine-tune"},
	}}
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"wrong purpose", `{"model":"model-a","training_file":"file_batch"}`, http.StatusBadRequest},
		{"foreign file", `{"model":"model-a","training_file":"file_foreign"}`, http.StatusNotFound},
		{"model denied", `{"model":"model-b","training_file":"file_batch"}`, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := fineTuningTestHandler(&memoryFineTuningStore{records: map[string]finetunestate.Record{}}, files, &gatewayFineTuningProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}, "model-a")
			response := fineTuningRequest(t, handler, http.MethodPost, "/v1/fine_tuning/jobs", test.body)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestFineTuningCancelsUpstreamJobWhenPersistenceFails(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	files := &memoryFileStore{files: map[string]filestate.File{"file_train": {ID: "file_train", OwnerKey: owner, Purpose: "fine-tune"}}}
	runtime := &gatewayFineTuningProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	handler := fineTuningTestHandler(&memoryFineTuningStore{records: map[string]finetunestate.Record{}, createErr: errors.New("database unavailable")}, files, runtime, "model-a")
	response := fineTuningRequest(t, handler, http.MethodPost, "/v1/fine_tuning/jobs", `{"model":"model-a","training_file":"file_train"}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if got, want := runtime.actions, []string{"create", "cancel"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("actions=%v", got)
	}
}

func TestFineTuningCreateUsesTrainingTokenBillingLifecycle(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	files := &memoryFileStore{files: map[string]filestate.File{"file_train": {ID: "file_train", OwnerKey: owner, Purpose: "fine-tune", Content: []byte(`{"messages":[{"role":"user","content":"train me"}]}`)}}}
	runtime := &gatewayFineTuningProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	store := &memoryFineTuningStore{records: map[string]finetunestate.Record{}}
	billing := &fineTuningBillingModule{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"model-a"}}, billing}), runtime).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 1 << 20, OwnerQuotaBytes: 4 << 20}).
		WithFineTuningStore(store))
	response := fineTuningRequest(t, handler, http.MethodPost, "/v1/fine_tuning/jobs", `{"model":"model-a","training_file":"file_train","method":{"type":"supervised","supervised":{"hyperparameters":{"n_epochs":2}}}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.actions) != 1 || len(store.records) != 1 || len(billing.phases) != 2 || billing.phases[0] != "reserve" || billing.phases[1] != "commit" || billing.trainingTokens[0] <= 1 || billing.trainingTokens[0] != billing.trainingTokens[1] || billing.apiTypes[0] != "fine_tuning" {
		t.Fatalf("provider actions=%v records=%v", runtime.actions, store.records)
	}
}

func TestFineTuningTrainingTokenEstimate(t *testing.T) {
	content := []byte(`{"messages":[{"role":"user","content":"train me"}]}`)
	oneEpoch := openai.EstimateContextTokens(string(content))
	tests := []struct {
		name   string
		method string
		want   int
		ok     bool
	}{
		{name: "default", want: oneEpoch, ok: true},
		{name: "auto", method: `{"supervised":{"hyperparameters":{"n_epochs":"auto"}}}`, want: oneEpoch, ok: true},
		{name: "explicit", method: `{"supervised":{"hyperparameters":{"n_epochs":3}}}`, want: oneEpoch * 3, ok: true},
		{name: "zero", method: `{"supervised":{"hyperparameters":{"n_epochs":0}}}`},
		{name: "too large", method: `{"supervised":{"hyperparameters":{"n_epochs":51}}}`},
		{name: "fractional", method: `{"supervised":{"hyperparameters":{"n_epochs":1.5}}}`},
		{name: "malformed", method: `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := fineTuningTrainingTokenEstimate(content, json.RawMessage(test.method))
			if (err == nil) != test.ok || got != test.want {
				t.Fatalf("estimate=%d err=%v", got, err)
			}
		})
	}
}

func TestFineTuningBillingFailuresDoNotLeaveActiveProviderJobs(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	files := &memoryFileStore{files: map[string]filestate.File{"file_train": {ID: "file_train", OwnerKey: owner, Purpose: "fine-tune", Content: []byte(`{"messages":[]}`)}}}

	t.Run("reserve", func(t *testing.T) {
		runtime := &gatewayFineTuningProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
		billing := &fineTuningBillingModule{reserveErr: errors.New("billing unavailable")}
		handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"model-a"}}, billing}), runtime).
			WithFileStore(files, FileRuntimeConfig{MaxBytes: 1 << 20, OwnerQuotaBytes: 4 << 20}).
			WithFineTuningStore(&memoryFineTuningStore{records: map[string]finetunestate.Record{}}))
		response := fineTuningRequest(t, handler, http.MethodPost, "/v1/fine_tuning/jobs", `{"model":"model-a","training_file":"file_train"}`)
		if response.Code != http.StatusServiceUnavailable || len(runtime.actions) != 0 || len(billing.phases) != 1 || billing.phases[0] != "reserve" {
			t.Fatalf("status=%d actions=%v phases=%v body=%s", response.Code, runtime.actions, billing.phases, response.Body.String())
		}
	})

	t.Run("budget", func(t *testing.T) {
		runtime := &gatewayFineTuningProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
		billing := &fineTuningBillingModule{reserveErr: modules.ErrBudgetExceeded}
		handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"model-a"}}, billing}), runtime).
			WithFileStore(files, FileRuntimeConfig{MaxBytes: 1 << 20, OwnerQuotaBytes: 4 << 20}).
			WithFineTuningStore(&memoryFineTuningStore{records: map[string]finetunestate.Record{}}))
		response := fineTuningRequest(t, handler, http.MethodPost, "/v1/fine_tuning/jobs", `{"model":"model-a","training_file":"file_train"}`)
		if response.Code != http.StatusTooManyRequests || len(runtime.actions) != 0 {
			t.Fatalf("status=%d actions=%v body=%s", response.Code, runtime.actions, response.Body.String())
		}
	})

	t.Run("commit", func(t *testing.T) {
		runtime := &gatewayFineTuningProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
		billing := &fineTuningBillingModule{commitErr: errors.New("billing unavailable")}
		store := &memoryFineTuningStore{records: map[string]finetunestate.Record{}}
		handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"model-a"}}, billing}), runtime).
			WithFileStore(files, FileRuntimeConfig{MaxBytes: 1 << 20, OwnerQuotaBytes: 4 << 20}).
			WithFineTuningStore(store))
		response := fineTuningRequest(t, handler, http.MethodPost, "/v1/fine_tuning/jobs", `{"model":"model-a","training_file":"file_train"}`)
		if response.Code != http.StatusServiceUnavailable || len(runtime.actions) != 2 || runtime.actions[0] != "create" || runtime.actions[1] != "cancel" || len(billing.phases) != 3 || billing.phases[2] != "cancel" || store.records["ftjob_1"].Job.Status != "cancelled" {
			t.Fatalf("status=%d actions=%v phases=%v record=%+v body=%s", response.Code, runtime.actions, billing.phases, store.records["ftjob_1"], response.Body.String())
		}
	})
}

func TestFineTuningRejectsUnknownJSONAndUnsupportedQuery(t *testing.T) {
	files := &memoryFileStore{files: map[string]filestate.File{}}
	handler := fineTuningTestHandler(&memoryFineTuningStore{records: map[string]finetunestate.Record{}}, files, &gatewayFineTuningProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}, "model-a")
	for _, request := range []struct{ method, path string }{{http.MethodPost, "/v1/fine_tuning/jobs?unknown=1"}, {http.MethodGet, "/v1/fine_tuning/jobs/ftjob_1?unknown=1"}} {
		response := fineTuningRequest(t, handler, request.method, request.path, `{}`)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("path=%s status=%d body=%s", request.path, response.Code, response.Body.String())
		}
	}
	response := fineTuningRequest(t, handler, http.MethodPost, "/v1/fine_tuning/jobs", `{"model":"model-a","training_file":"file_train","unknown":true}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteFineTunedModelUsesOwnedPinnedJob(t *testing.T) {
	model := "ft:model-a:owner:suffix:1"
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memoryFineTuningStore{records: map[string]finetunestate.Record{
		"ftjob_1": {OwnerKey: owner, Binding: provider.FineTuningBinding{Endpoint: "primary", Model: "model-a", Deployment: "deployment-hash"}, Job: openai.FineTuningJob{ID: "ftjob_1", FineTunedModel: &model}},
	}}
	runtime := &gatewayFineTuningProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	handler := fineTuningTestHandler(store, &memoryFileStore{files: map[string]filestate.File{}}, runtime, "model-a")
	response := fineTuningRequest(t, handler, http.MethodDelete, "/v1/models/"+model, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"deleted":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.lastBinding.Deployment != "deployment-hash" || runtime.actions[len(runtime.actions)-1] != "delete_model" {
		t.Fatalf("binding=%+v actions=%v", runtime.lastBinding, runtime.actions)
	}
}

func TestDeleteFineTunedModelHidesForeignAndInvalidModels(t *testing.T) {
	foreignModel := "ft:model-a:foreign:1"
	store := &memoryFineTuningStore{records: map[string]finetunestate.Record{
		"ftjob_foreign": {OwnerKey: "foreign", Job: openai.FineTuningJob{ID: "ftjob_foreign", FineTunedModel: &foreignModel}},
	}}
	handler := fineTuningTestHandler(store, &memoryFileStore{files: map[string]filestate.File{}}, &gatewayFineTuningProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}, "model-a")
	for _, test := range []struct {
		path   string
		status int
	}{{"/v1/models/" + foreignModel, http.StatusNotFound}, {"/v1/models/bad%2Fmodel", http.StatusBadRequest}, {"/v1/models/ft:model?query=1", http.StatusBadRequest}} {
		response := fineTuningRequest(t, handler, http.MethodDelete, test.path, "")
		if response.Code != test.status {
			t.Fatalf("path=%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
}
