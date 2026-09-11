package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/videostate"
)

type memoryVideoStore struct {
	records   map[string]videostate.Record
	createErr error
}

func videoStoreKey(owner, id string) string { return owner + "/" + id }
func (s *memoryVideoStore) CreateVideoRecord(_ context.Context, record videostate.Record, quota int) (videostate.Record, error) {
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
func (s *memoryVideoStore) GetVideoRecord(_ context.Context, owner, id string) (videostate.Record, error) {
	record, found := s.records[videoStoreKey(owner, id)]
	if !found {
		return videostate.Record{}, videostate.ErrNotFound
	}
	return record, nil
}
func (s *memoryVideoStore) ListVideoRecords(_ context.Context, owner string, limit int, _ string) ([]videostate.Record, string, error) {
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
	key := videoStoreKey(owner, id)
	if _, found := s.records[key]; !found {
		return videostate.ErrNotFound
	}
	delete(s.records, key)
	return nil
}

type gatewayVideoProvider struct {
	*batchProvider
	actions []string
	nextID  int
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
	return openai.Video{ID: id, Object: "video", Model: "model-a", Status: "completed", Seconds: "4", Size: "720x1280"}, nil
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

func videoTestHandler(store videostate.Store, runtime *gatewayVideoProvider, billing modules.Module) http.Handler {
	mods := []modules.Module{&lifecycleAuthModule{allowedModels: []string{"model-a"}}}
	if billing != nil {
		mods = append(mods, billing)
	}
	return Routes(NewHandler(modules.NewPipeline(mods), runtime).WithVideoStore(store))
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
	handler := videoTestHandler(&memoryVideoStore{records: map[string]videostate.Record{}, createErr: errors.New("database unavailable")}, runtime, nil)
	response := videoRequest(t, handler, http.MethodPost, "/v1/videos", `{"model":"model-a","prompt":"a cat"}`)
	if response.Code != http.StatusServiceUnavailable || strings.Join(runtime.actions, ",") != "create,delete" {
		t.Fatalf("status=%d actions=%v body=%s", response.Code, runtime.actions, response.Body.String())
	}
}

func TestVideoCreationFailsClosedWithBilling(t *testing.T) {
	runtime := &gatewayVideoProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	handler := videoTestHandler(&memoryVideoStore{records: map[string]videostate.Record{}}, runtime, &lifecycleBillingModule{})
	response := videoRequest(t, handler, http.MethodPost, "/v1/videos", `{"model":"model-a","prompt":"a cat"}`)
	if response.Code != http.StatusNotImplemented || len(runtime.actions) != 0 {
		t.Fatalf("status=%d actions=%v body=%s", response.Code, runtime.actions, response.Body.String())
	}
}
