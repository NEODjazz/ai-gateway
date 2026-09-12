package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/containerstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type memoryContainerStore struct {
	mu        sync.Mutex
	records   map[string]containerstate.Record
	createErr error
}

func containerKey(owner, id string) string { return owner + "/" + id }
func (s *memoryContainerStore) CreateContainerRecord(_ context.Context, record containerstate.Record, quota int) (containerstate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return containerstate.Record{}, s.createErr
	}
	count := 0
	for _, existing := range s.records {
		if existing.OwnerKey == record.OwnerKey {
			count++
		}
	}
	if count >= quota {
		return containerstate.Record{}, containerstate.ErrQuotaExceeded
	}
	key := containerKey(record.OwnerKey, record.Container.ID)
	if _, found := s.records[key]; found {
		return containerstate.Record{}, containerstate.ErrConflict
	}
	record.CreatedAt, record.UpdatedAt = time.Now(), time.Now()
	s.records[key] = record
	return record, nil
}
func (s *memoryContainerStore) GetContainerRecord(_ context.Context, owner, id string) (containerstate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.records[containerKey(owner, id)]
	if !found {
		return containerstate.Record{}, containerstate.ErrNotFound
	}
	return record, nil
}
func (s *memoryContainerStore) ListContainerRecords(_ context.Context, owner string, limit int, _ string) ([]containerstate.Record, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []containerstate.Record{}
	for _, record := range s.records {
		if record.OwnerKey == owner {
			result = append(result, record)
		}
	}
	if len(result) > limit {
		return result[:limit], result[limit-1].Container.ID, nil
	}
	return result, "", nil
}
func (s *memoryContainerStore) UpdateContainerRecord(_ context.Context, owner string, container openai.Container) (containerstate.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := containerKey(owner, container.ID)
	record, found := s.records[key]
	if !found {
		return containerstate.Record{}, containerstate.ErrNotFound
	}
	record.Container, record.UpdatedAt = container, time.Now()
	s.records[key] = record
	return record, nil
}
func (s *memoryContainerStore) DeleteContainerRecord(_ context.Context, owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := containerKey(owner, id)
	if _, found := s.records[key]; !found {
		return containerstate.ErrNotFound
	}
	delete(s.records, key)
	return nil
}

type gatewayContainerProvider struct {
	*batchProvider
	actions []string
}

func (p *gatewayContainerProvider) CreateContainer(ctx context.Context, identity modules.RequestContext, input openai.ContainerCreateRequest, admit func(context.Context, *modules.RequestContext) error) (openai.Container, provider.ContainerBinding, error) {
	identity.Request.Model = input.Model
	identity.Metadata["gateway.api_type"] = "container"
	if admit != nil {
		if err := admit(ctx, &identity); err != nil {
			return openai.Container{}, provider.ContainerBinding{}, err
		}
	}
	p.actions = append(p.actions, "create")
	return openai.Container{ID: "cntr_1", Object: "container", Name: input.Name, Status: "running", MemoryLimit: "1g"}, provider.ContainerBinding{Endpoint: "sandbox", Model: input.Model, Deployment: strings.Repeat("a", 64)}, nil
}
func (p *gatewayContainerProvider) RetrieveContainer(_ context.Context, _ provider.ContainerBinding, id string) (openai.Container, error) {
	p.actions = append(p.actions, "retrieve")
	return openai.Container{ID: id, Object: "container", Name: "analysis", Status: "running", MemoryLimit: "1g"}, nil
}
func (p *gatewayContainerProvider) DeleteContainer(_ context.Context, _ provider.ContainerBinding, id string) (openai.ContainerDeletion, error) {
	p.actions = append(p.actions, "delete")
	return openai.ContainerDeletion{ID: id, Object: "container.deleted", Deleted: true}, nil
}
func (p *gatewayContainerProvider) CreateContainerFile(_ context.Context, _ provider.ContainerBinding, containerID string, upload provider.ContainerFileUpload) (openai.ContainerFile, error) {
	p.actions = append(p.actions, "file-create")
	return openai.ContainerFile{ID: "cfile_1", Object: "container.file", ContainerID: containerID, Path: "/mnt/data/" + upload.Filename, Source: "user", Bytes: int64(len(upload.Content))}, nil
}
func (p *gatewayContainerProvider) ListContainerFiles(_ context.Context, _ provider.ContainerBinding, containerID string, _ provider.ContainerFileListOptions) (openai.ContainerFileList, error) {
	p.actions = append(p.actions, "file-list")
	return openai.ContainerFileList{Object: "list", Data: []openai.ContainerFile{{ID: "cfile_1", Object: "container.file", ContainerID: containerID, Path: "/mnt/data/a.txt", Source: "user"}}}, nil
}
func (p *gatewayContainerProvider) RetrieveContainerFile(_ context.Context, _ provider.ContainerBinding, containerID, fileID string) (openai.ContainerFile, error) {
	p.actions = append(p.actions, "file-get")
	return openai.ContainerFile{ID: fileID, Object: "container.file", ContainerID: containerID, Path: "/mnt/data/a.txt", Source: "user"}, nil
}
func (p *gatewayContainerProvider) DeleteContainerFile(_ context.Context, _ provider.ContainerBinding, _, fileID string) (openai.ContainerDeletion, error) {
	p.actions = append(p.actions, "file-delete")
	return openai.ContainerDeletion{ID: fileID, Object: "container.file.deleted", Deleted: true}, nil
}
func (p *gatewayContainerProvider) DownloadContainerFile(_ context.Context, _ provider.ContainerBinding, _, _ string) (provider.ContainerFileContent, error) {
	p.actions = append(p.actions, "file-content")
	return provider.ContainerFileContent{Body: io.NopCloser(strings.NewReader("hello")), ContentType: "text/plain", ContentLength: 5}, nil
}

type containerBillingModule struct {
	phases    []string
	commitErr error
}

func (*containerBillingModule) Name() string   { return "billing" }
func (*containerBillingModule) Required() bool { return true }
func (m *containerBillingModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.phases = append(m.phases, "reserve")
	return nil
}
func (*containerBillingModule) PostResponseEnabled() bool { return true }
func (m *containerBillingModule) HandlePostResponse(_ context.Context, _ *modules.RequestContext) error {
	m.phases = append(m.phases, "commit")
	return m.commitErr
}
func (m *containerBillingModule) HandleFailure(_ context.Context, _ *modules.RequestContext, _ error) error {
	m.phases = append(m.phases, "cancel")
	return nil
}

func containerTestHandler(store containerstate.Store, runtime *gatewayContainerProvider, billing modules.Module) http.Handler {
	mods := []modules.Module{&lifecycleAuthModule{allowedModels: []string{"model-a"}}}
	if billing != nil {
		mods = append(mods, billing)
	}
	return Routes(NewHandler(modules.NewPipeline(mods), runtime).WithContainerStore(store))
}
func containerRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestContainerOwnedLifecycleAndBilling(t *testing.T) {
	store := &memoryContainerStore{records: map[string]containerstate.Record{}}
	runtime := &gatewayContainerProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	billing := &containerBillingModule{}
	handler := containerTestHandler(store, runtime, billing)
	created := containerRequest(t, handler, http.MethodPost, "/v1/containers", `{"model":"model-a","name":"analysis","memory_limit":"1g"}`)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"id":"cntr_1"`) || strings.Join(billing.phases, ",") != "reserve,commit" {
		t.Fatalf("create=%d/%s billing=%v", created.Code, created.Body.String(), billing.phases)
	}
	for _, call := range []struct{ method, path, contains string }{{http.MethodGet, "/v1/containers", `"object":"list"`}, {http.MethodGet, "/v1/containers/cntr_1", `"status":"running"`}, {http.MethodDelete, "/v1/containers/cntr_1", `"deleted":true`}} {
		response := containerRequest(t, handler, call.method, call.path, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), call.contains) {
			t.Fatalf("%s %s: %d %s", call.method, call.path, response.Code, response.Body.String())
		}
	}
	if strings.Join(runtime.actions, ",") != "create,retrieve,delete" || len(store.records) != 0 {
		t.Fatalf("actions=%v records=%v", runtime.actions, store.records)
	}
}

func TestContainerCreationCompensatesPersistenceAndBillingFailures(t *testing.T) {
	for _, test := range []struct {
		name                string
		storeErr, commitErr error
	}{{name: "storage", storeErr: errors.New("database unavailable")}, {name: "billing commit", commitErr: errors.New("billing unavailable")}} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryContainerStore{records: map[string]containerstate.Record{}, createErr: test.storeErr}
			runtime := &gatewayContainerProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
			billing := &containerBillingModule{commitErr: test.commitErr}
			response := containerRequest(t, containerTestHandler(store, runtime, billing), http.MethodPost, "/v1/containers", `{"model":"model-a","name":"analysis"}`)
			if response.Code != http.StatusServiceUnavailable || strings.Join(runtime.actions, ",") != "create,delete" || strings.Join(billing.phases, ",") != "reserve,commit,cancel" && test.commitErr != nil || strings.Join(billing.phases, ",") != "reserve,cancel" && test.storeErr != nil || len(store.records) != 0 {
				t.Fatalf("status=%d actions=%v billing=%v records=%v body=%s", response.Code, runtime.actions, billing.phases, store.records, response.Body.String())
			}
		})
	}
}

func TestContainerRejectsFilesAndCrossOwnerLookupBeforeProvider(t *testing.T) {
	store := &memoryContainerStore{records: map[string]containerstate.Record{containerKey("another-owner", "cntr_1"): {OwnerKey: "another-owner", Container: openai.Container{ID: "cntr_1"}}}}
	runtime := &gatewayContainerProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	handler := containerTestHandler(store, runtime, nil)
	files := containerRequest(t, handler, http.MethodPost, "/v1/containers", `{"model":"model-a","name":"analysis","file_ids":["file_1"]}`)
	lookup := containerRequest(t, handler, http.MethodGet, "/v1/containers/cntr_1", "")
	fileLookup := containerRequest(t, handler, http.MethodGet, "/v1/containers/cntr_1/files", "")
	if files.Code != http.StatusBadRequest || !strings.Contains(files.Body.String(), "unsupported_parameter") || lookup.Code != http.StatusNotFound || fileLookup.Code != http.StatusNotFound || len(runtime.actions) != 0 {
		t.Fatalf("files=%d/%s lookup=%d/%s file_lookup=%d/%s actions=%v", files.Code, files.Body.String(), lookup.Code, lookup.Body.String(), fileLookup.Code, fileLookup.Body.String(), runtime.actions)
	}
}

func TestContainerFileCopiesOnlyOwnedGatewayFileContent(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	containers := &memoryContainerStore{records: map[string]containerstate.Record{containerKey(owner, "cntr_1"): {OwnerKey: owner, Binding: provider.ContainerBinding{Endpoint: "sandbox", Model: "model-a", Deployment: strings.Repeat("a", 64)}, Container: openai.Container{ID: "cntr_1"}}}}
	files := &memoryFileStore{files: map[string]filestate.File{
		"file_owned": {ID: "file_owned", OwnerKey: owner, Filename: "owned.txt", ContentType: "text/plain", Bytes: 5, Content: []byte("hello")},
		"file_other": {ID: "file_other", OwnerKey: "another-owner", Filename: "secret.txt", ContentType: "text/plain", Bytes: 6, Content: []byte("secret")},
	}}
	runtime := &gatewayContainerProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	pipeline := modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"model-a"}}})
	handler := Routes(NewHandler(pipeline, runtime).WithContainerStore(containers).WithFileStore(files, FileRuntimeConfig{MaxBytes: maxGatewayContainerFileBytes, OwnerQuotaBytes: maxGatewayContainerFileBytes}))
	owned := containerRequest(t, handler, http.MethodPost, "/v1/containers/cntr_1/files", `{"file_id":"file_owned"}`)
	other := containerRequest(t, handler, http.MethodPost, "/v1/containers/cntr_1/files", `{"file_id":"file_other"}`)
	if owned.Code != http.StatusOK || !strings.Contains(owned.Body.String(), `"bytes":5`) || other.Code != http.StatusNotFound || strings.Join(runtime.actions, ",") != "file-create" {
		t.Fatalf("owned=%d/%s other=%d/%s actions=%v", owned.Code, owned.Body.String(), other.Code, other.Body.String(), runtime.actions)
	}
}

func TestContainerFileOwnedLifecycle(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memoryContainerStore{records: map[string]containerstate.Record{containerKey(owner, "cntr_1"): {OwnerKey: owner, Binding: provider.ContainerBinding{Endpoint: "sandbox", Model: "model-a", Deployment: strings.Repeat("a", 64)}, Container: openai.Container{ID: "cntr_1"}}}}
	runtime := &gatewayContainerProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	handler := containerTestHandler(store, runtime, nil)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "a.txt")
	_, _ = part.Write([]byte("hello"))
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/containers/cntr_1/files", &body)
	request.Header.Set("Authorization", "Bearer test")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, request)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"id":"cfile_1"`) {
		t.Fatalf("create=%d/%s", created.Code, created.Body.String())
	}
	for _, call := range []struct{ method, path, contains string }{{http.MethodGet, "/v1/containers/cntr_1/files?limit=2&order=asc", `"object":"list"`}, {http.MethodGet, "/v1/containers/cntr_1/files/cfile_1", `"path":"/mnt/data/a.txt"`}, {http.MethodGet, "/v1/containers/cntr_1/files/cfile_1/content", "hello"}, {http.MethodDelete, "/v1/containers/cntr_1/files/cfile_1", `"deleted":true`}} {
		response := containerRequest(t, handler, call.method, call.path, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), call.contains) {
			t.Fatalf("%s %s: %d/%s", call.method, call.path, response.Code, response.Body.String())
		}
	}
	if strings.Join(runtime.actions, ",") != "file-create,file-list,file-get,file-content,file-delete" {
		t.Fatalf("actions=%v", runtime.actions)
	}
}

func TestContainerFileCreateRejectsQueryBeforeProvider(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memoryContainerStore{records: map[string]containerstate.Record{containerKey(owner, "cntr_1"): {OwnerKey: owner, Container: openai.Container{ID: "cntr_1"}}}}
	runtime := &gatewayContainerProvider{batchProvider: &batchProvider{models: []string{"model-a"}}}
	response := containerRequest(t, containerTestHandler(store, runtime, nil), http.MethodPost, "/v1/containers/cntr_1/files?unexpected=true", `{}`)
	if response.Code != http.StatusBadRequest || len(runtime.actions) != 0 {
		t.Fatalf("status=%d body=%s actions=%v", response.Code, response.Body.String(), runtime.actions)
	}
}

func TestBoundedContainerFileReaderReportsOverflow(t *testing.T) {
	reader := &boundedContainerFileReader{reader: strings.NewReader("abcdef"), remaining: 5}
	content, err := io.ReadAll(reader)
	if string(content) != "abcde" || err == nil || !strings.Contains(err.Error(), "exceeds 512 MiB") {
		t.Fatalf("content=%q err=%v", content, err)
	}
}
