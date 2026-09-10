package gateway

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
)

type memoryFileStore struct {
	mu    sync.Mutex
	files map[string]filestate.File
}

type fileAuthModule struct {
	credential string
	user       string
	rpm        int
}

func (*fileAuthModule) Name() string   { return "auth" }
func (*fileAuthModule) Required() bool { return true }
func (m *fileAuthModule) Handle(_ context.Context, req *modules.RequestContext) error {
	req.CredentialID = m.credential
	req.UserID = m.user
	req.RateLimitRPM = m.rpm
	return nil
}

func (s *memoryFileStore) Create(_ context.Context, file filestate.File, quota int64) (filestate.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var used int64
	for _, existing := range s.files {
		if existing.OwnerKey == file.OwnerKey {
			used += existing.Bytes
		}
	}
	if file.Bytes > quota || used > quota-file.Bytes {
		return filestate.File{}, filestate.ErrQuotaExceeded
	}
	if _, found := s.files[file.ID]; found {
		return filestate.File{}, filestate.ErrConflict
	}
	file.CreatedAt = time.Unix(123, 0).UTC()
	s.files[file.ID] = file
	file.Content = nil
	return file, nil
}

func (s *memoryFileStore) List(_ context.Context, owner, purpose string, limit int, after string) ([]filestate.File, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]filestate.File, 0, len(s.files))
	for _, file := range s.files {
		if file.OwnerKey == owner && (purpose == "" || file.Purpose == purpose) {
			file.Content = nil
			result = append(result, file)
		}
	}
	return result, "", nil
}

func (s *memoryFileStore) Get(_ context.Context, owner, id string, content bool) (filestate.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, found := s.files[id]
	if !found || file.OwnerKey != owner {
		return filestate.File{}, filestate.ErrNotFound
	}
	if !content {
		file.Content = nil
	}
	return file, nil
}

func (s *memoryFileStore) Delete(_ context.Context, owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, found := s.files[id]
	if !found || file.OwnerKey != owner {
		return filestate.ErrNotFound
	}
	delete(s.files, id)
	return nil
}

func fileUploadBody(t *testing.T, purpose, filename string, payload []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if purpose != "" {
		if err := writer.WriteField("purpose", purpose); err != nil {
			t.Fatal(err)
		}
	}
	if filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func TestFileHTTPLifecycle(t *testing.T) {
	store := &memoryFileStore{files: map[string]filestate.File{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), modelsProvider{}).
		WithFileStore(store, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}))
	body, contentType := fileUploadBody(t, "batch", "input.jsonl", []byte("payload"))
	create := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	create.Header.Set("Authorization", "Bearer key")
	create.Header.Set("Content-Type", contentType)
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"object":"file"`) || !strings.Contains(created.Body.String(), `"status":"processed"`) {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var id string
	store.mu.Lock()
	for storedID, file := range store.files {
		id = storedID
		if file.OwnerKey != fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"}) {
			t.Fatalf("unexpected owner key %q", file.OwnerKey)
		}
	}
	store.mu.Unlock()
	if !strings.HasPrefix(id, "file_") {
		t.Fatalf("generated id=%q", id)
	}
	otherOwner := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "credential", user: "other-user"}}), modelsProvider{}).
		WithFileStore(store, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}))
	foreign := authorizedFileRequest(t, otherOwner, http.MethodGet, "/v1/files/"+id)
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("cross-user read status=%d body=%s", foreign.Code, foreign.Body.String())
	}

	list := authorizedFileRequest(t, handler, http.MethodGet, "/v1/files?purpose=batch&limit=20")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), id) || !strings.Contains(list.Body.String(), `"has_more":false`) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	metadata := authorizedFileRequest(t, handler, http.MethodGet, "/v1/files/"+id)
	if metadata.Code != http.StatusOK || !strings.Contains(metadata.Body.String(), `"filename":"input.jsonl"`) {
		t.Fatalf("metadata status=%d body=%s", metadata.Code, metadata.Body.String())
	}
	content := authorizedFileRequest(t, handler, http.MethodGet, "/v1/files/"+id+"/content")
	if content.Code != http.StatusOK || content.Body.String() != "payload" || content.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(content.Header().Get("Content-Disposition"), "input.jsonl") {
		t.Fatalf("content status=%d headers=%v body=%q", content.Code, content.Header(), content.Body.String())
	}
	deleted := authorizedFileRequest(t, handler, http.MethodDelete, "/v1/files/"+id)
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := authorizedFileRequest(t, handler, http.MethodGet, "/v1/files/"+id)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestFilesEnforceAuthenticationLimitsAndDurability(t *testing.T) {
	store := &memoryFileStore{files: map[string]filestate.File{}}
	configured := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), modelsProvider{}).
		WithFileStore(store, FileRuntimeConfig{MaxBytes: 4, OwnerQuotaBytes: 4}))

	unauthorized := httptest.NewRecorder()
	Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{})).ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	missingStore := authorizedFileRequest(t, Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), modelsProvider{})), http.MethodGet, "/v1/files")
	if missingStore.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing store status=%d body=%s", missingStore.Code, missingStore.Body.String())
	}

	oversizedBody, oversizedType := fileUploadBody(t, "batch", "large.txt", []byte("12345"))
	oversizedRequest := httptest.NewRequest(http.MethodPost, "/v1/files", oversizedBody)
	oversizedRequest.Header.Set("Authorization", "Bearer key")
	oversizedRequest.Header.Set("Content-Type", oversizedType)
	oversized := httptest.NewRecorder()
	configured.ServeHTTP(oversized, oversizedRequest)
	if oversized.Code != http.StatusBadRequest || len(store.files) != 0 {
		t.Fatalf("oversized status=%d body=%s files=%d", oversized.Code, oversized.Body.String(), len(store.files))
	}

	body, contentType := fileUploadBody(t, "batch", "one.txt", []byte("1234"))
	request := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	request.Header.Set("Authorization", "Bearer key")
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	configured.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("quota fill status=%d body=%s", response.Code, response.Body.String())
	}
	body, contentType = fileUploadBody(t, "batch", "two.txt", []byte("x"))
	request = httptest.NewRequest(http.MethodPost, "/v1/files", body)
	request.Header.Set("Authorization", "Bearer key")
	request.Header.Set("Content-Type", contentType)
	response = httptest.NewRecorder()
	configured.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("quota status=%d body=%s", response.Code, response.Body.String())
	}

	rateLimited := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "limited", user: "user", rpm: 1}}), modelsProvider{}).
		WithFileStore(store, FileRuntimeConfig{MaxBytes: 4, OwnerQuotaBytes: 4}))
	first := authorizedFileRequest(t, rateLimited, http.MethodGet, "/v1/files")
	second := authorizedFileRequest(t, rateLimited, http.MethodGet, "/v1/files")
	if first.Code != http.StatusOK || second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit statuses=%d,%d retry=%q", first.Code, second.Code, second.Header().Get("Retry-After"))
	}
}

func TestFilesRejectMalformedInputsBeforeStore(t *testing.T) {
	store := &memoryFileStore{files: map[string]filestate.File{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), modelsProvider{}).
		WithFileStore(store, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}))
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   func(*testing.T) (*bytes.Buffer, string)
		status int
	}{
		{name: "missing purpose", method: http.MethodPost, path: "/v1/files", body: func(t *testing.T) (*bytes.Buffer, string) { return fileUploadBody(t, "", "x.txt", []byte("x")) }, status: http.StatusBadRequest},
		{name: "invalid purpose", method: http.MethodPost, path: "/v1/files", body: func(t *testing.T) (*bytes.Buffer, string) {
			return fileUploadBody(t, "bad purpose", "x.txt", []byte("x"))
		}, status: http.StatusBadRequest},
		{name: "invalid limit", method: http.MethodGet, path: "/v1/files?limit=0", status: http.StatusBadRequest},
		{name: "unknown query", method: http.MethodGet, path: "/v1/files?secret=x", status: http.StatusBadRequest},
		{name: "invalid id", method: http.MethodGet, path: "/v1/files/bad%20id", status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body *bytes.Buffer
			var contentType string
			if test.body != nil {
				body, contentType = test.body(t)
			} else {
				body = &bytes.Buffer{}
			}
			request := httptest.NewRequest(test.method, test.path, body)
			request.Header.Set("Authorization", "Bearer key")
			if contentType != "" {
				request.Header.Set("Content-Type", contentType)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestFileNameValidation(t *testing.T) {
	for _, valid := range []string{"input.jsonl", "данные 01.jsonl"} {
		if !validFileName(valid) {
			t.Errorf("valid filename rejected: %q", valid)
		}
	}
	for _, invalid := range []string{"", ".", "..", "line\nbreak", string([]byte{0xff})} {
		if validFileName(invalid) {
			t.Errorf("invalid filename accepted: %q", invalid)
		}
	}
}

func authorizedFileRequest(t *testing.T, handler http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
