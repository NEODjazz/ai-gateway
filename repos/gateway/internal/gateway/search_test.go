package gateway

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type ocrAttachmentModule struct{ mediaType string }

func (*ocrAttachmentModule) Name() string   { return "ocr-attachment" }
func (*ocrAttachmentModule) Required() bool { return true }
func (m *ocrAttachmentModule) Handle(_ context.Context, req *modules.RequestContext) error {
	if req.OCRRequest == nil {
		return nil
	}
	attachment, err := req.OCRRequest.Document.Attachment()
	if err != nil {
		return err
	}
	if attachment != nil {
		m.mediaType = attachment.MediaType
	}
	return nil
}

func TestOCRResolvesOwnedFileBeforePolicyAndProvider(t *testing.T) {
	store := &memoryFileStore{files: map[string]filestate.File{
		"file_invoice": {ID: "file_invoice", OwnerKey: fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"}), Filename: "invoice.pdf", Purpose: "user_data", ContentType: "application/pdf", Bytes: 9, Content: []byte("%PDF-1.7\n")},
	}}
	llm := &chatProvider{}
	attachmentPolicy := &ocrAttachmentModule{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}, attachmentPolicy}), llm).
		WithFileStore(store, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}))
	request := httptest.NewRequest(http.MethodPost, "/v1/ocr", strings.NewReader(`{"model":"ocr-document","document":{"type":"file","file_id":"file_invoice"},"pages":[0]}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || llm.request.OCRRequest == nil {
		t.Fatalf("status=%d body=%s request=%+v", response.Code, response.Body.String(), llm.request.OCRRequest)
	}
	want := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("%PDF-1.7\n"))
	if llm.request.OCRRequest.Document.Type != "document_url" || llm.request.OCRRequest.Document.DocumentURL != want || llm.request.OCRRequest.Document.FileID != "" {
		t.Fatalf("unresolved provider document: %+v", llm.request.OCRRequest.Document)
	}
	if attachmentPolicy.mediaType != "application/pdf" {
		t.Fatalf("resolved file was not projected to attachment policy: %q", attachmentPolicy.mediaType)
	}
}

func TestOCRFileReferenceEnforcesOwnerAndContent(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memoryFileStore{files: map[string]filestate.File{
		"file_foreign": {ID: "file_foreign", OwnerKey: owner + "-other", Filename: "foreign.pdf", Purpose: "user_data", ContentType: "application/pdf", Bytes: 9, Content: []byte("%PDF-1.7\n")},
		"file_text":    {ID: "file_text", OwnerKey: owner, Filename: "notes.txt", Purpose: "user_data", ContentType: "text/plain", Bytes: 4, Content: []byte("text")},
	}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), &chatProvider{}).
		WithFileStore(store, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}))
	for _, test := range []struct {
		id     string
		status int
	}{{"file_missing", http.StatusNotFound}, {"file_foreign", http.StatusNotFound}, {"file_text", http.StatusBadRequest}} {
		request := httptest.NewRequest(http.MethodPost, "/v1/ocr", strings.NewReader(`{"model":"ocr","document":{"type":"file","file_id":"`+test.id+`"}}`))
		request.Header.Set("Authorization", "Bearer key")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("id=%s status=%d body=%s", test.id, response.Code, response.Body.String())
		}
	}
}

func TestOCRImageFileReservesOnePage(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nimage")
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	store := &memoryFileStore{files: map[string]filestate.File{
		"file_image": {ID: "file_image", OwnerKey: owner, Filename: "image.png", Purpose: "user_data", ContentType: "image/png", Bytes: int64(len(png)), Content: png},
	}}
	llm := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), llm).
		WithFileStore(store, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}))
	request := httptest.NewRequest(http.MethodPost, "/v1/ocr", strings.NewReader(`{"model":"ocr","document":{"type":"file","file_id":"file_image"}}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || llm.request.InputPages != 1 || llm.request.OCRRequest.Document.Type != "image_url" {
		t.Fatalf("status=%d pages=%d document=%+v body=%s", response.Code, llm.request.InputPages, llm.request.OCRRequest.Document, response.Body.String())
	}
}

type searchRewriteModule struct{}

func (searchRewriteModule) Name() string   { return "search-rewrite" }
func (searchRewriteModule) Required() bool { return true }
func (searchRewriteModule) Handle(_ context.Context, req *modules.RequestContext) error {
	for index := range req.Request.Messages {
		req.Request.Messages[index].Content = "redacted"
	}
	return nil
}

func TestSearchUsesAuthenticatedPolicyAndTokenAdmission(t *testing.T) {
	llm := &chatProvider{}
	rates := &embeddingTokenRateStore{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{searchRewriteModule{}, accessPolicyModule{models: []string{"search-*"}}}), llm, rates))
	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"provider":"search-deployment","search_tool_name":"search-web","query":["private one","private two"],"max_results":5,"search_domain_filter":["example.com"],"country":"US"}`))
	request.Header.Set("Authorization", "Bearer client-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || llm.request.SearchRequest == nil || llm.request.APIKey != "" || !strings.Contains(response.Body.String(), `"object":"search"`) {
		t.Fatalf("status=%d body=%s context=%+v", response.Code, response.Body.String(), llm.request)
	}
	queries, err := llm.request.SearchRequest.Queries()
	if err != nil || len(queries) != 2 || queries[0] != "redacted" || queries[1] != "redacted" {
		t.Fatalf("queries=%v err=%v", queries, err)
	}
	if rates.tokens != openai.SearchReserveTokens(*llm.request.SearchRequest) {
		t.Fatalf("TPM=%d want=%d", rates.tokens, openai.SearchReserveTokens(*llm.request.SearchRequest))
	}
}

func TestSearchRejectsInvalidJSONBeforeProvider(t *testing.T) {
	for _, body := range []string{
		`{"model":"search","query":"hello","unknown":true}`,
		`{"model":"search","query":"hello","country":"us"}`,
		`{"model":"search","query":"hello","search_domain_filter":["https://example.com"]}`,
		`{"model":"one","search_tool_name":"two","query":"hello"}`,
	} {
		llm := &chatProvider{}
		response := httptest.NewRecorder()
		Routes(NewHandler(modules.NewPipeline(nil), llm)).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest || llm.request.SearchRequest != nil {
			t.Fatalf("status=%d body=%s provider_request=%+v", response.Code, response.Body.String(), llm.request.SearchRequest)
		}
	}
}

func TestOCRUsesAuthenticatedPolicyAndPageAdmission(t *testing.T) {
	llm := &chatProvider{}
	rates := &embeddingTokenRateStore{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"ocr-*"}}}), llm, rates))
	request := httptest.NewRequest(http.MethodPost, "/v1/ocr", strings.NewReader(`{"provider":"mistral-private","model":"ocr-document","document":{"type":"document_url","document_url":"https://example.test/invoice.pdf"},"pages":"0,2-4"}`))
	request.Header.Set("Authorization", "Bearer client-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || llm.request.OCRRequest == nil || llm.request.APIKey != "" || llm.request.InputPages != 4 || !strings.Contains(response.Body.String(), `"pages_processed":1`) {
		t.Fatalf("status=%d body=%s context=%+v", response.Code, response.Body.String(), llm.request)
	}
	if rates.tokens != llm.request.OCRRequest.InputTokens() {
		t.Fatalf("TPM=%d want=%d", rates.tokens, llm.request.OCRRequest.InputTokens())
	}
}

func TestOCRRejectsInvalidJSONBeforeProvider(t *testing.T) {
	for _, body := range []string{
		`{"model":"ocr","document":{"type":"document_url","document_url":"http://example.test/a.pdf"}}`,
		`{"model":"ocr","document":{"type":"file","document_url":"https://example.test/a.pdf"}}`,
		`{"model":"ocr","document":{"type":"document_url","document_url":"https://example.test/a.pdf"},"unknown":true}`,
	} {
		llm := &chatProvider{}
		response := httptest.NewRecorder()
		Routes(NewHandler(modules.NewPipeline(nil), llm)).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/ocr", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest || llm.request.OCRRequest != nil {
			t.Fatalf("status=%d body=%s provider_request=%+v", response.Code, response.Body.String(), llm.request.OCRRequest)
		}
	}
}
