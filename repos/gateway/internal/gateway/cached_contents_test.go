package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/cachedstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type cachedContentAuthModule struct{}

func (cachedContentAuthModule) Name() string   { return "auth" }
func (cachedContentAuthModule) Required() bool { return true }
func (cachedContentAuthModule) Handle(_ context.Context, req *modules.RequestContext) error {
	if req.APIKey == "" {
		return modules.ErrUnauthorized
	}
	req.CredentialID = "credential"
	req.UserID = req.APIKey
	req.AllowedModels = []string{"public-model"}
	return nil
}

type cachedContentTransformModule struct{ calls int }

func (*cachedContentTransformModule) Name() string   { return "anonymizer" }
func (*cachedContentTransformModule) Required() bool { return true }
func (m *cachedContentTransformModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.calls++
	if len(req.Request.Messages) != 0 {
		req.Request.Messages[0].Content = "masked"
	}
	return nil
}

type cachedContentBillingModule struct {
	phases []string
	usage  openai.Usage
	api    string
}

func (*cachedContentBillingModule) Name() string   { return "billing" }
func (*cachedContentBillingModule) Required() bool { return true }
func (m *cachedContentBillingModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.phases = append(m.phases, "reserve")
	m.api = req.Metadata["gateway.api_type"]
	return nil
}
func (*cachedContentBillingModule) PostResponseEnabled() bool { return true }
func (m *cachedContentBillingModule) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.phases = append(m.phases, "commit")
	if req.Response != nil {
		m.usage = req.Response.Usage
	}
	return nil
}
func (m *cachedContentBillingModule) HandleFailure(_ context.Context, _ *modules.RequestContext, _ error) error {
	m.phases = append(m.phases, "cancel")
	return nil
}

type memoryCachedContentStore struct {
	mu        sync.Mutex
	records   map[string]cachedstate.Record
	createErr error
	updateErr error
	sequence  int
}

func cachedContentStoreKey(owner, name string) string { return owner + "\x00" + name }

func (s *memoryCachedContentStore) CreateCachedContentRecord(_ context.Context, record cachedstate.Record, quota int) (cachedstate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return cachedstate.Record{}, s.createErr
	}
	count := 0
	for _, current := range s.records {
		if current.OwnerKey == record.OwnerKey {
			count++
		}
	}
	if count >= quota {
		return cachedstate.Record{}, cachedstate.ErrQuotaExceeded
	}
	key := cachedContentStoreKey(record.OwnerKey, record.Content.Name)
	if _, exists := s.records[key]; exists {
		return cachedstate.Record{}, cachedstate.ErrConflict
	}
	s.sequence++
	record.CreatedAt = time.Unix(int64(s.sequence), 0)
	record.UpdatedAt = record.CreatedAt
	s.records[key] = record
	return record, nil
}

func (s *memoryCachedContentStore) GetCachedContentRecord(_ context.Context, owner, name string) (cachedstate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.records[cachedContentStoreKey(owner, name)]
	if !exists {
		return cachedstate.Record{}, cachedstate.ErrNotFound
	}
	return record, nil
}

func (s *memoryCachedContentStore) ListCachedContentRecords(_ context.Context, owner string, limit int, after string) ([]cachedstate.Record, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]cachedstate.Record, 0)
	for _, record := range s.records {
		if record.OwnerKey == owner {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
	if after != "" {
		found := false
		for index := range records {
			if records[index].Content.Name == after {
				records = records[index+1:]
				found = true
				break
			}
		}
		if !found {
			return nil, "", cachedstate.ErrNotFound
		}
	}
	next := ""
	if len(records) > limit {
		next = records[limit-1].Content.Name
		records = records[:limit]
	}
	return records, next, nil
}

func (s *memoryCachedContentStore) UpdateCachedContentRecord(_ context.Context, owner string, content openai.GeminiCachedContent) (cachedstate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return cachedstate.Record{}, s.updateErr
	}
	key := cachedContentStoreKey(owner, content.Name)
	record, exists := s.records[key]
	if !exists {
		return cachedstate.Record{}, cachedstate.ErrNotFound
	}
	record.Content = content
	record.ExpiresAt, _ = time.Parse(time.RFC3339Nano, content.ExpireTime)
	record.UpdatedAt = time.Now()
	s.records[key] = record
	return record, nil
}

func (s *memoryCachedContentStore) DeleteCachedContentRecord(_ context.Context, owner, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := cachedContentStoreKey(owner, name)
	if _, exists := s.records[key]; !exists {
		return cachedstate.ErrNotFound
	}
	delete(s.records, key)
	return nil
}

type gatewayCachedContentProvider struct {
	*batchProvider
	mu             sync.Mutex
	actions        []string
	createRequests []openai.ChatCompletionRequest
	contents       map[string]openai.GeminiCachedContent
	updates        []openai.GeminiCachedContentExpiration
	createCalls    int
}

func (p *gatewayCachedContentProvider) CreateCachedContent(ctx context.Context, identity modules.RequestContext, request openai.ChatCompletionRequest, displayName string, expiration openai.GeminiCachedContentExpiration, admit func(context.Context, *modules.RequestContext) error) (openai.GeminiCachedContent, provider.CachedContentBinding, error) {
	p.mu.Lock()
	p.createCalls++
	p.mu.Unlock()
	attempt := identity
	attempt.Request = request
	attempt.Metadata = map[string]string{"gateway.api_type": "cached_content"}
	if err := admit(ctx, &attempt); err != nil {
		return openai.GeminiCachedContent{}, provider.CachedContentBinding{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.createRequests = append(p.createRequests, attempt.Request)
	p.actions = append(p.actions, "create")
	name := "cachedContents/cache-" + strconv.Itoa(len(p.contents)+1)
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	if expiration.ExpireTime != "" {
		expires, _ = time.Parse(time.RFC3339Nano, expiration.ExpireTime)
	}
	content := openai.GeminiCachedContent{Name: name, DisplayName: displayName, Model: "models/upstream-model", CreateTime: now.Format(time.RFC3339Nano), UpdateTime: now.Format(time.RFC3339Nano), ExpireTime: expires.Format(time.RFC3339Nano), UsageMetadata: &openai.GeminiCachedContentUsage{TotalTokenCount: 17}}
	p.contents[name] = content
	attempt.Response = &openai.ChatCompletionResponse{Usage: openai.Usage{PromptTokens: 17, TotalTokens: 17, PromptTokensDetails: &openai.PromptTokenDetails{CacheWriteTokens: 17}}}
	return content, provider.CachedContentBinding{Endpoint: "gemini-primary", Model: request.Model, Deployment: strings.Repeat("a", 64)}, nil
}

func (p *gatewayCachedContentProvider) RetrieveCachedContent(_ context.Context, _ provider.CachedContentBinding, name string) (openai.GeminiCachedContent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.actions = append(p.actions, "get")
	content, exists := p.contents[name]
	if !exists {
		return openai.GeminiCachedContent{}, &provider.Error{StatusCode: http.StatusNotFound, Class: provider.FailureClientRequest}
	}
	return content, nil
}

func (p *gatewayCachedContentProvider) UpdateCachedContent(_ context.Context, _ provider.CachedContentBinding, name string, expiration openai.GeminiCachedContentExpiration) (openai.GeminiCachedContent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.actions = append(p.actions, "update")
	p.updates = append(p.updates, expiration)
	content, exists := p.contents[name]
	if !exists {
		return openai.GeminiCachedContent{}, &provider.Error{StatusCode: http.StatusNotFound, Class: provider.FailureClientRequest}
	}
	expires := time.Now().UTC().Add(2 * time.Hour)
	if expiration.ExpireTime != "" {
		expires, _ = time.Parse(time.RFC3339Nano, expiration.ExpireTime)
	}
	content.ExpireTime = expires.Format(time.RFC3339Nano)
	content.UpdateTime = time.Now().UTC().Format(time.RFC3339Nano)
	p.contents[name] = content
	return content, nil
}

func (p *gatewayCachedContentProvider) DeleteCachedContent(_ context.Context, _ provider.CachedContentBinding, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.actions = append(p.actions, "delete")
	if _, exists := p.contents[name]; !exists {
		return &provider.Error{StatusCode: http.StatusNotFound, Class: provider.FailureClientRequest}
	}
	delete(p.contents, name)
	return nil
}

func cachedContentTestHandler(store cachedstate.Store, runtime *gatewayCachedContentProvider, transform modules.Module, billing modules.Module) http.Handler {
	providerModules := make([]modules.Module, 0, 2)
	if transform != nil {
		providerModules = append(providerModules, transform)
	}
	if billing != nil {
		providerModules = append(providerModules, billing)
	}
	return Routes(NewHandler(modules.NewPipeline([]modules.Module{cachedContentAuthModule{}}), runtime).WithResourceBillingPipeline(modules.NewPipeline(providerModules)).WithCachedContentStore(store))
}

func cachedContentRequest(handler http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("x-goog-api-key", key)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestCachedContentOwnedLifecycleAppliesPolicyBillingAndPagination(t *testing.T) {
	store := &memoryCachedContentStore{records: map[string]cachedstate.Record{}}
	runtime := &gatewayCachedContentProvider{batchProvider: &batchProvider{models: []string{"public-model"}}, contents: map[string]openai.GeminiCachedContent{}}
	transform := &cachedContentTransformModule{}
	billing := &cachedContentBillingModule{}
	handler := cachedContentTestHandler(store, runtime, transform, billing)
	body := `{"model":"models/public-model","displayName":"reference","ttl":"3600s","contents":[{"parts":[{"text":"private@example.com"}]}]}`
	first := cachedContentRequest(handler, http.MethodPost, "/v1beta/cachedContents", body, "user-a")
	second := cachedContentRequest(handler, http.MethodPost, "/v1beta/cachedContents", body, "user-a")
	if first.Code != http.StatusOK || second.Code != http.StatusOK || transform.calls != 2 || strings.Join(billing.phases, ",") != "reserve,commit,reserve,commit" || billing.api != "cached_content" || billing.usage.PromptTokensDetails == nil || billing.usage.PromptTokensDetails.CacheWriteTokens != 17 {
		t.Fatalf("first=%d/%s second=%d/%s transform=%d billing=%v usage=%+v", first.Code, first.Body.String(), second.Code, second.Body.String(), transform.calls, billing.phases, billing.usage)
	}
	if len(runtime.createRequests) != 2 || runtime.createRequests[0].Messages[0].Content != "masked" {
		t.Fatalf("provider requests=%+v", runtime.createRequests)
	}
	page := cachedContentRequest(handler, http.MethodGet, "/v1beta/cachedContents?pageSize=1", "", "user-a")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"name":"cachedContents/cache-2"`) || !strings.Contains(page.Body.String(), `"nextPageToken":"`) {
		t.Fatalf("first page=%d %s", page.Code, page.Body.String())
	}
	tokenStart := strings.Index(page.Body.String(), `"nextPageToken":"`) + len(`"nextPageToken":"`)
	tokenEnd := strings.Index(page.Body.String()[tokenStart:], `"`)
	token := page.Body.String()[tokenStart : tokenStart+tokenEnd]
	next := cachedContentRequest(handler, http.MethodGet, "/v1beta/cachedContents?pageSize=1&pageToken="+token, "", "user-a")
	if next.Code != http.StatusOK || !strings.Contains(next.Body.String(), `"name":"cachedContents/cache-1"`) {
		t.Fatalf("next page=%d %s", next.Code, next.Body.String())
	}
	get := cachedContentRequest(handler, http.MethodGet, "/v1beta/cachedContents/cache-1", "", "user-a")
	patch := cachedContentRequest(handler, http.MethodPatch, "/v1beta/cachedContents/cache-1?updateMask=ttl", `{"ttl":"7200s"}`, "user-a")
	deleted := cachedContentRequest(handler, http.MethodDelete, "/v1beta/cachedContents/cache-1", "", "user-a")
	if get.Code != http.StatusOK || patch.Code != http.StatusOK || deleted.Code != http.StatusNoContent {
		t.Fatalf("get=%d/%s patch=%d/%s delete=%d/%s", get.Code, get.Body.String(), patch.Code, patch.Body.String(), deleted.Code, deleted.Body.String())
	}
}

func TestCachedContentOwnerIsolationAndCreateCompensation(t *testing.T) {
	store := &memoryCachedContentStore{records: map[string]cachedstate.Record{}}
	runtime := &gatewayCachedContentProvider{batchProvider: &batchProvider{models: []string{"public-model"}}, contents: map[string]openai.GeminiCachedContent{}}
	billing := &cachedContentBillingModule{}
	handler := cachedContentTestHandler(store, runtime, nil, billing)
	body := `{"model":"models/public-model","ttl":"3600s","contents":[{"parts":[{"text":"private"}]}]}`
	created := cachedContentRequest(handler, http.MethodPost, "/v1beta/cachedContents", body, "owner")
	before := len(runtime.actions)
	foreign := cachedContentRequest(handler, http.MethodGet, "/v1beta/cachedContents/cache-1", "", "foreign")
	if created.Code != http.StatusOK || foreign.Code != http.StatusNotFound || len(runtime.actions) != before {
		t.Fatalf("created=%d foreign=%d/%s actions=%v", created.Code, foreign.Code, foreign.Body.String(), runtime.actions)
	}

	failingStore := &memoryCachedContentStore{records: map[string]cachedstate.Record{}, createErr: cachedstate.ErrUnavailable}
	failingRuntime := &gatewayCachedContentProvider{batchProvider: &batchProvider{models: []string{"public-model"}}, contents: map[string]openai.GeminiCachedContent{}}
	failingBilling := &cachedContentBillingModule{}
	failed := cachedContentRequest(cachedContentTestHandler(failingStore, failingRuntime, nil, failingBilling), http.MethodPost, "/v1beta/cachedContents", body, "owner")
	if failed.Code != http.StatusServiceUnavailable || strings.Join(failingRuntime.actions, ",") != "create,delete" || strings.Join(failingBilling.phases, ",") != "reserve,cancel" || len(failingRuntime.contents) != 0 {
		t.Fatalf("failed=%d/%s actions=%v billing=%v contents=%v", failed.Code, failed.Body.String(), failingRuntime.actions, failingBilling.phases, failingRuntime.contents)
	}
}

func TestCachedContentUpdateRestoresProviderExpirationWhenStoreFails(t *testing.T) {
	store := &memoryCachedContentStore{records: map[string]cachedstate.Record{}}
	runtime := &gatewayCachedContentProvider{batchProvider: &batchProvider{models: []string{"public-model"}}, contents: map[string]openai.GeminiCachedContent{}}
	handler := cachedContentTestHandler(store, runtime, nil, nil)
	body := `{"model":"models/public-model","ttl":"3600s","contents":[{"parts":[{"text":"private"}]}]}`
	created := cachedContentRequest(handler, http.MethodPost, "/v1beta/cachedContents", body, "owner")
	if created.Code != http.StatusOK {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "owner"})
	record, err := store.GetCachedContentRecord(t.Context(), owner, "cachedContents/cache-1")
	if err != nil {
		t.Fatal(err)
	}
	store.updateErr = errors.New("database unavailable")
	response := cachedContentRequest(handler, http.MethodPatch, "/v1beta/cachedContents/cache-1?updateMask=ttl", `{"ttl":"7200s"}`, "owner")
	if response.Code != http.StatusServiceUnavailable || len(runtime.updates) != 2 || runtime.updates[1].ExpireTime != record.Content.ExpireTime {
		t.Fatalf("response=%d/%s updates=%+v original=%s", response.Code, response.Body.String(), runtime.updates, record.Content.ExpireTime)
	}
}

func TestCachedContentRejectsConflictingNativeAuthentication(t *testing.T) {
	store := &memoryCachedContentStore{records: map[string]cachedstate.Record{}}
	runtime := &gatewayCachedContentProvider{batchProvider: &batchProvider{models: []string{"public-model"}}, contents: map[string]openai.GeminiCachedContent{}}
	handler := cachedContentTestHandler(store, runtime, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1beta/cachedContents", nil)
	request.Header.Set("Authorization", "Bearer one")
	request.Header.Set("x-goog-api-key", "two")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || runtime.createCalls != 0 {
		t.Fatalf("status=%d body=%s createCalls=%d", response.Code, response.Body.String(), runtime.createCalls)
	}
}

func TestCachedContentListRemainsAvailableWhenProviderLifecycleIsUnavailable(t *testing.T) {
	store := &memoryCachedContentStore{records: map[string]cachedstate.Record{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{cachedContentAuthModule{}}), &batchProvider{models: []string{"public-model"}}).WithCachedContentStore(store))
	response := cachedContentRequest(handler, http.MethodGet, "/v1beta/cachedContents", "", "owner")
	if response.Code != http.StatusOK || response.Body.String() != "{\"cachedContents\":[]}\n" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
