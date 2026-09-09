package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

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
