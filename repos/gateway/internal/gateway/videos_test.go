package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/videostate"
)

type memoryVideoStore struct {
	mu        sync.Mutex
	records   map[string]videostate.Record
	jobs      map[string]asyncstate.Job
	createErr error
}

func videoStoreKey(owner, id string) string { return owner + "/" + id }
func (s *memoryVideoStore) CreateVideoRecord(_ context.Context, record videostate.Record, quota int) (videostate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createVideoRecord(record, quota)
}
func (s *memoryVideoStore) createVideoRecord(record videostate.Record, quota int) (videostate.Record, error) {
	if s.createErr != nil {
		return videostate.Record{}, s.createErr
	}
	count := 0
	for _, item := range s.records {
		if item.OwnerKey == record.OwnerKey {
			count++
		}
	}
	if count >= quota {
		return videostate.Record{}, videostate.ErrQuotaExceeded
	}
	key := videoStoreKey(record.OwnerKey, record.Video.ID)
	if _, found := s.records[key]; found {
		return videostate.Record{}, videostate.ErrConflict
	}
	record.CreatedAt, record.UpdatedAt = time.Now(), time.Now()
	s.records[key] = record
	return record, nil
}
func (s *memoryVideoStore) CreateVideoRecordWithJob(_ context.Context, record videostate.Record, quota int, job asyncstate.Job) (videostate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	created, err := s.createVideoRecord(record, quota)
	if err != nil {
		return videostate.Record{}, err
	}
	if s.jobs == nil {
		s.jobs = map[string]asyncstate.Job{}
	}
	key := job.Kind + "/" + job.ResourceID
	if _, found := s.jobs[key]; found {
		delete(s.records, videoStoreKey(record.OwnerKey, record.Video.ID))
		return videostate.Record{}, asyncstate.ErrConflict
	}
	s.jobs[key] = job
	return created, nil
}
func (s *memoryVideoStore) GetVideoRecord(_ context.Context, owner, id string) (videostate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.records[videoStoreKey(owner, id)]
	if !found {
		return videostate.Record{}, videostate.ErrNotFound
	}
	return record, nil
}
func (s *memoryVideoStore) ListVideoRecords(_ context.Context, owner string, limit int, _ string) ([]videostate.Record, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []videostate.Record{}
	for _, record := range s.records {
		if record.OwnerKey == owner {
			result = append(result, record)
		}
	}
	if len(result) > limit {
		return result[:limit], result[limit-1].Video.ID, nil
	}
	return result, "", nil
}
func (s *memoryVideoStore) UpdateVideoRecord(_ context.Context, owner string, video openai.Video) (videostate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := videoStoreKey(owner, video.ID)
	record, found := s.records[key]
	if !found {
		return videostate.Record{}, videostate.ErrNotFound
	}
	record.Video, record.UpdatedAt = video, time.Now()
	s.records[key] = record
	return record, nil
}
func (s *memoryVideoStore) DeleteVideoRecord(_ context.Context, owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := videoStoreKey(owner, id)
	if _, found := s.records[key]; !found {
		return videostate.ErrNotFound
	}
	delete(s.records, key)
	return nil
}

func (s *memoryVideoStore) EnqueueAsyncJob(_ context.Context, job asyncstate.Job) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = map[string]asyncstate.Job{}
	}
	key := job.Kind + "/" + job.ResourceID
	if _, found := s.jobs[key]; found {
		return false, nil
	}
	s.jobs[key] = job
	return true, nil
}
func (s *memoryVideoStore) HasAsyncJob(_ context.Context, kind, resourceID, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, found := s.jobs[kind+"/"+resourceID]
	return found && job.OwnerKey == owner, nil
}
func (s *memoryVideoStore) ClaimAsyncJobs(_ context.Context, kind string, limit int, _ time.Duration) ([]asyncstate.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]asyncstate.Job, 0, limit)
	for key, job := range s.jobs {
		if job.Kind != kind || !job.LeaseUntil.IsZero() {
			continue
		}
		job.Attempts++
		job.LeaseGeneration++
		job.LeaseUntil = time.Now().Add(time.Minute)
		s.jobs[key] = job
		result = append(result, job)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}
func (s *memoryVideoStore) RetryAsyncJob(_ context.Context, kind, resourceID string, generation int64, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := kind + "/" + resourceID
	job, found := s.jobs[key]
	if !found || job.LeaseGeneration != generation {
		return asyncstate.ErrLeaseLost
	}
	job.LeaseUntil = time.Time{}
	s.jobs[key] = job
	return nil
}
func (s *memoryVideoStore) CompleteAsyncJob(_ context.Context, kind, resourceID string, generation int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := kind + "/" + resourceID
	job, found := s.jobs[key]
	if !found || job.LeaseGeneration != generation {
		return asyncstate.ErrLeaseLost
	}
	delete(s.jobs, key)
	return nil
}

type gatewayVideoProvider struct {
	*batchProvider
	actions        []string
	nextID         int
	retrieveStatus string
	providerCost   *int64
}

type videoBillingModule struct {
	phases       []string
	seconds      []int
	estimated    []bool
	providerCost []*int64
	reserveErr   error
	commitErr    error
}

func (*videoBillingModule) Name() string   { return "billing" }
func (*videoBillingModule) Required() bool { return true }
func (m *videoBillingModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.record("reserve", req)
	return m.reserveErr
}
func (*videoBillingModule) PostResponseEnabled() bool { return true }
func (m *videoBillingModule) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.record("commit", req)
	return m.commitErr
}
func (m *videoBillingModule) HandleFailure(_ context.Context, req *modules.RequestContext, _ error) error {
	m.record("cancel", req)
	return nil
}
func (m *videoBillingModule) record(phase string, req *modules.RequestContext) {
	m.phases = append(m.phases, phase)
	m.seconds = append(m.seconds, req.VideoSeconds)
	m.estimated = append(m.estimated, req.Metadata["gateway.video_usage_exact"] != "true")
	m.providerCost = append(m.providerCost, req.VideoProviderCostUSDTicks)
}

func (p *gatewayVideoProvider) CreateVideo(ctx context.Context, identity modules.RequestContext, input openai.VideoCreateRequest, admit func(context.Context, *modules.RequestContext) error) (openai.Video, provider.VideoBinding, error) {
	identity.Request.Model = input.Model
	identity.Metadata["gateway.api_type"] = "video"
	if admit != nil {
		if err := admit(ctx, &identity); err != nil {
			return openai.Video{}, provider.VideoBinding{}, err
		}
	}
	p.nextID++
	p.actions = append(p.actions, "create")
	prompt := input.Prompt
	return openai.Video{ID: "video_" + strconv.Itoa(p.nextID), Object: "video", Model: input.Model, Status: "queued", Prompt: &prompt, Seconds: "4", Size: "720x1280"}, provider.VideoBinding{Endpoint: "video", Model: input.Model, Deployment: strings.Repeat("a", 64)}, nil
}
func (p *gatewayVideoProvider) RetrieveVideo(_ context.Context, _ provider.VideoBinding, id string) (openai.Video, error) {
	p.actions = append(p.actions, "retrieve")
	status := p.retrieveStatus
	if status == "" {
		status = "completed"
	}
	return openai.Video{ID: id, Object: "video", Model: "model-a", Status: status, Seconds: "4", Size: "720x1280", ProviderCostUSDTicks: p.providerCost}, nil
}
func (p *gatewayVideoProvider) DeleteVideo(_ context.Context, _ provider.VideoBinding, id string) (openai.VideoDeletion, error) {
	p.actions = append(p.actions, "delete")
	return openai.VideoDeletion{ID: id, Object: "video.deleted", Deleted: true}, nil
}
func (p *gatewayVideoProvider) DownloadVideoContent(_ context.Context, _ provider.VideoBinding, _, _ string) (provider.VideoContent, error) {
	p.actions = append(p.actions, "content")
	return provider.VideoContent{Body: io.NopCloser(strings.NewReader("video")), ContentType: "video/mp4", ContentLength: 5}, nil
}
func (p *gatewayVideoProvider) RemixVideo(ctx context.Context, identity modules.RequestContext, binding provider.VideoBinding, _ string, input openai.VideoRemixRequest, admit func(context.Context, *modules.RequestContext) error) (openai.Video, provider.VideoBinding, error) {
	return p.CreateVideo(ctx, identity, openai.VideoCreateRequest{Model: binding.Model, Prompt: input.Prompt}, admit)
}
func (p *gatewayVideoProvider) ExtendVideo(ctx context.Context, identity modules.RequestContext, binding provider.VideoBinding, id string, input openai.VideoExtendRequest, admit func(context.Context, *modules.RequestContext) error) (openai.Video, provider.VideoBinding, error) {
	video, nextBinding, err := p.CreateVideo(ctx, identity, openai.VideoCreateRequest{Model: binding.Model, Prompt: input.Prompt, Seconds: input.Seconds}, admit)
	if err == nil {
		video.RemixedFromVideoID = &id
		video.Size = ""
	}
	return video, nextBinding, err
}

func videoTestHandler(store videostate.Store, runtime *gatewayVideoProvider, billing modules.Module) http.Handler {
	return Routes(videoTestGateway(store, runtime, billing))
}
func videoTestGateway(store videostate.Store, runtime *gatewayVideoProvider, billing modules.Module) Handler {
	mods := []modules.Module{&lifecycleAuthModule{allowedModels: []string{"model-a"}}}
	if billing != nil {
		mods = append(mods, billing)
	}
	return NewHandler(modules.NewPipeline(mods), runtime).WithVideoStore(store)
}
func videoRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestVideoOwnedLifecycle(t *testing.T) {
	store := &memoryVideoStore{records: map[string]videostate.Record{}}
	runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	handler := videoTestHandler(store, runtime, nil)
	created := videoRequest(t, handler, http.MethodPost, "/v1/videos", `{"model":"model-a","prompt":"a cat","seconds":"4","size":"720x1280"}`)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"id":"video_1"`) {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	for _, call := range []struct{ method, path, contains string }{
		{http.MethodGet, "/v1/videos", `"object":"list"`},
		{http.MethodGet, "/v1/videos/video_1", `"status":"completed"`},
		{http.MethodGet, "/v1/videos/video_1/content?variant=video", "video"},
		{http.MethodPost, "/v1/videos/video_1/remix", `"id":"video_2"`},
		{http.MethodDelete, "/v1/videos/video_1", `"deleted":true`},
	} {
		response := videoRequest(t, handler, call.method, call.path, `{"prompt":"take a bow"}`)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), call.contains) {
			t.Fatalf("%s %s: %d %s", call.method, call.path, response.Code, response.Body.String())
		}
	}
	for _, record := range store.records {
		if record.Video.ID == "video_1" {
			t.Fatal("deleted record remains")
		}
	}
}

func TestVideoCreationCompensatesPersistenceFailure(t *testing.T) {
	runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	billing := &videoBillingModule{}
	handler := videoTestHandler(&memoryVideoStore{records: map[string]videostate.Record{}, createErr: errors.New("database unavailable")}, runtime, billing)
	response := videoRequest(t, handler, http.MethodPost, "/v1/videos", `{"model":"model-a","prompt":"a cat"}`)
	if response.Code != http.StatusServiceUnavailable || strings.Join(runtime.actions, ",") != "create,delete" || strings.Join(billing.phases, ",") != "reserve,cancel" {
		t.Fatalf("status=%d actions=%v phases=%v body=%s", response.Code, runtime.actions, billing.phases, response.Body.String())
	}
}

func TestVideoCreationUsesDurationBillingLifecycle(t *testing.T) {
	runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	billing := &videoBillingModule{}
	store := &memoryVideoStore{records: map[string]videostate.Record{}}
	gateway := videoTestGateway(store, runtime, billing)
	response := videoRequest(t, Routes(gateway), http.MethodPost, "/v1/videos", `{"model":"model-a","prompt":"a cat","seconds":"8"}`)
	if processed, err := gateway.ProcessVideoSettlements(t.Context()); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	if response.Code != http.StatusOK || strings.Join(billing.phases, ",") != "reserve,commit" || len(billing.seconds) != 2 || billing.seconds[0] != 8 || billing.seconds[1] != 8 || !billing.estimated[0] || billing.estimated[1] {
		t.Fatalf("status=%d phases=%v seconds=%v estimated=%v body=%s", response.Code, billing.phases, billing.seconds, billing.estimated, response.Body.String())
	}
}

func TestVideoBillingUsesProductionResourcePipeline(t *testing.T) {
	store := &memoryVideoStore{records: map[string]videostate.Record{}}
	ticks := int64(500_000_000)
	runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}, providerCost: &ticks}
	billing := &videoBillingModule{}
	auth := modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"model-a"}}})
	providerModules := modules.NewPipeline([]modules.Module{billing})
	gateway := NewHandler(auth, runtime).WithResourceBillingPipeline(providerModules).WithVideoStore(store)
	response := videoRequest(t, Routes(gateway), http.MethodPost, "/v1/videos", `{"model":"model-a","prompt":"a cat"}`)
	if processed, err := gateway.ProcessVideoSettlements(t.Context()); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	if response.Code != http.StatusOK || strings.Join(billing.phases, ",") != "reserve,commit" || len(billing.providerCost) != 2 || billing.providerCost[0] != nil || billing.providerCost[1] == nil || *billing.providerCost[1] != ticks {
		t.Fatalf("status=%d phases=%v body=%s", response.Code, billing.phases, response.Body.String())
	}
}

func TestVideoSettlementWaitsForTerminalStateAndCancelsFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     string
		wantPhases string
		wantJobs   int
	}{
		{name: "pending", status: "in_progress", wantPhases: "reserve", wantJobs: 1},
		{name: "failed", status: "failed", wantPhases: "reserve,cancel", wantJobs: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryVideoStore{records: map[string]videostate.Record{}}
			runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}, retrieveStatus: test.status}
			billing := &videoBillingModule{}
			gateway := videoTestGateway(store, runtime, billing)
			response := videoRequest(t, Routes(gateway), http.MethodPost, "/v1/videos", `{"model":"model-a","prompt":"a cat","seconds":"4"}`)
			processed, err := gateway.ProcessVideoSettlements(t.Context())
			if err != nil || processed != 1 || response.Code != http.StatusOK || strings.Join(billing.phases, ",") != test.wantPhases || len(store.jobs) != test.wantJobs {
				t.Fatalf("status=%d processed=%d err=%v phases=%v jobs=%d", response.Code, processed, err, billing.phases, len(store.jobs))
			}
		})
	}
}

func TestVideoDeleteWaitsForDurableSettlement(t *testing.T) {
	store := &memoryVideoStore{records: map[string]videostate.Record{}}
	runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	billing := &videoBillingModule{}
	gateway := videoTestGateway(store, runtime, billing)
	created := videoRequest(t, Routes(gateway), http.MethodPost, "/v1/videos", `{"model":"model-a","prompt":"a cat"}`)
	deleted := videoRequest(t, Routes(gateway), http.MethodDelete, "/v1/videos/video_1", "")
	if created.Code != http.StatusOK || deleted.Code != http.StatusConflict || !strings.Contains(deleted.Body.String(), "video_settlement_pending") || strings.Join(runtime.actions, ",") != "create" {
		t.Fatalf("create=%d delete=%d actions=%v body=%s", created.Code, deleted.Code, runtime.actions, deleted.Body.String())
	}
}

func TestVideoBillingFailuresCompensateProviderAndOwnership(t *testing.T) {
	t.Run("reserve", func(t *testing.T) {
		runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
		billing := &videoBillingModule{reserveErr: modules.ErrBudgetExceeded}
		handler := videoTestHandler(&memoryVideoStore{records: map[string]videostate.Record{}}, runtime, billing)
		response := videoRequest(t, handler, http.MethodPost, "/v1/videos", `{"model":"model-a","prompt":"a cat"}`)
		if response.Code != http.StatusTooManyRequests || len(runtime.actions) != 0 || strings.Join(billing.phases, ",") != "reserve" {
			t.Fatalf("status=%d actions=%v phases=%v body=%s", response.Code, runtime.actions, billing.phases, response.Body.String())
		}
	})

	t.Run("commit is retried durably", func(t *testing.T) {
		runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
		billing := &videoBillingModule{commitErr: errors.New("billing unavailable")}
		store := &memoryVideoStore{records: map[string]videostate.Record{}}
		gateway := videoTestGateway(store, runtime, billing)
		response := videoRequest(t, Routes(gateway), http.MethodPost, "/v1/videos", `{"model":"model-a","prompt":"a cat"}`)
		processed, err := gateway.ProcessVideoSettlements(t.Context())
		if response.Code != http.StatusOK || processed != 1 || err != nil || strings.Join(runtime.actions, ",") != "create,retrieve" || strings.Join(billing.phases, ",") != "reserve,commit" || len(store.records) != 1 || len(store.jobs) != 1 {
			t.Fatalf("status=%d actions=%v phases=%v records=%v body=%s", response.Code, runtime.actions, billing.phases, store.records, response.Body.String())
		}
	})
}

func TestVideoRemixUsesSourceDurationForBilling(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "credential", UserID: "user"}
	owner := fileOwnerKey(identity)
	store := &memoryVideoStore{records: map[string]videostate.Record{videoStoreKey(owner, "video_source"): {OwnerKey: owner, Binding: provider.VideoBinding{Endpoint: "video", Model: "model-a", Deployment: strings.Repeat("a", 64)}, Video: openai.Video{ID: "video_source", Model: "model-a", Seconds: "12"}}}}
	runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	billing := &videoBillingModule{}
	gateway := videoTestGateway(store, runtime, billing)
	response := videoRequest(t, Routes(gateway), http.MethodPost, "/v1/videos/video_source/remix", `{"prompt":"take a bow"}`)
	if processed, err := gateway.ProcessVideoSettlements(t.Context()); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	if response.Code != http.StatusOK || strings.Join(billing.phases, ",") != "reserve,commit" || billing.seconds[0] != 12 || billing.seconds[1] != 12 {
		t.Fatalf("status=%d phases=%v seconds=%v body=%s", response.Code, billing.phases, billing.seconds, response.Body.String())
	}
}

func TestVideoExtensionUsesRequestedDurationAndDurableSettlement(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "credential", UserID: "user"}
	owner := fileOwnerKey(identity)
	store := &memoryVideoStore{records: map[string]videostate.Record{
		videoStoreKey(owner, "video_source"): {
			OwnerKey: owner,
			Binding:  provider.VideoBinding{Endpoint: "video", Model: "model-a", Deployment: strings.Repeat("a", 64)},
			Video:    openai.Video{ID: "video_source", Object: "video", Model: "model-a", Status: "completed", Seconds: "12", Size: "1280x720"},
		},
	}}
	runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	billing := &videoBillingModule{}
	gateway := videoTestGateway(store, runtime, billing)
	response := videoRequest(t, Routes(gateway), http.MethodPost, "/v1/videos/video_source/extend", `{"prompt":"continue forward","seconds":"8"}`)
	if processed, err := gateway.ProcessVideoSettlements(t.Context()); err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	if response.Code != http.StatusOK || strings.Join(billing.phases, ",") != "reserve,commit" || billing.seconds[0] != 8 || billing.seconds[1] != 8 || !strings.Contains(response.Body.String(), `"remixed_from_video_id":"video_source"`) || !strings.Contains(response.Body.String(), `"size":"1280x720"`) {
		t.Fatalf("status=%d phases=%v seconds=%v body=%s", response.Code, billing.phases, billing.seconds, response.Body.String())
	}
}

func TestVideoRejectsInvalidDurationBeforeProviderCall(t *testing.T) {
	runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	handler := videoTestHandler(&memoryVideoStore{records: map[string]videostate.Record{}}, runtime, nil)
	response := videoRequest(t, handler, http.MethodPost, "/v1/videos", `{"model":"model-a","prompt":"a cat","seconds":"7"}`)
	if response.Code != http.StatusBadRequest || len(runtime.actions) != 0 {
		t.Fatalf("status=%d actions=%v body=%s", response.Code, runtime.actions, response.Body.String())
	}
}
