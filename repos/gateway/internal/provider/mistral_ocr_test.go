package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestMistralOCRContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/ocr" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["provider"] != nil || payload["model"] != "ocr-upstream" || payload["pages"] != "0,2" || payload["table_format"] != "markdown" {
			t.Fatalf("payload=%v", payload)
		}
		_, _ = io.WriteString(w, `{"pages":[{"index":0,"markdown":"first","images":[]},{"index":2,"markdown":"second","images":[]}],"model":"ocr-upstream-2026","usage_info":{"pages_processed":2,"doc_size_bytes":120}}`)
	}))
	defer server.Close()
	request := openai.OCRRequest{Provider: "private", Model: "ocr-upstream", Document: openai.OCRDocument{Type: "document_url", DocumentURL: "https://example.test/a.pdf"}, Pages: "0,2", TableFormat: "markdown"}
	response, err := NewMistral(server.URL+"/v1", "secret", false).OCR(t.Context(), request)
	if err != nil || response.Model != "ocr-upstream-2026" || response.UsageInfo.PagesProcessed != 2 || len(response.Pages) != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestOCRRejectsMalformedAndOversizedResponses(t *testing.T) {
	invalid := []string{
		`{"pages":[],"model":"ocr","usage_info":{"pages_processed":0}}`,
		`{"pages":[{"index":0,"markdown":"ok"}],"model":"ocr","usage_info":{"pages_processed":2}}`,
		`{"pages":[{"index":0,"markdown":"ok"},{"index":0,"markdown":"duplicate"}],"model":"ocr","usage_info":{"pages_processed":2}}`,
		`{"pages":[{"index":0}],"model":"ocr","usage_info":{"pages_processed":1}}`,
		`{"pages":[{"index":0,"markdown":"ok","images":{}}],"model":"ocr","usage_info":{"pages_processed":1}}`,
		`{"pages":[{"index":0,"markdown":"ok"}],"model":"ocr","usage_info":{"pages_processed":1}} {}`,
	}
	for _, body := range invalid {
		if _, err := decodeOCRResponse(strings.NewReader(body)); err == nil {
			t.Fatalf("accepted response %s", body)
		}
	}
	reader := &ocrLimitReader{}
	if _, err := decodeOCRResponse(reader); err == nil || reader.read != openai.MaxOCRResponseBytes+1 {
		t.Fatalf("oversized response accepted: bytes=%d err=%v", reader.read, err)
	}
}

type ocrLimitReader struct{ read int }

func (r *ocrLimitReader) Read(buffer []byte) (int, error) {
	remaining := openai.MaxOCRResponseBytes + 1 - r.read
	if remaining <= 0 {
		return 0, io.EOF
	}
	if len(buffer) > remaining {
		buffer = buffer[:remaining]
	}
	for index := range buffer {
		buffer[index] = 'x'
	}
	r.read += len(buffer)
	return len(buffer), nil
}

type ocrLifecycleRecorder struct {
	reserved int
	settled  int
	model    string
}

func (*ocrLifecycleRecorder) Name() string   { return "ocr-recorder" }
func (*ocrLifecycleRecorder) Required() bool { return true }
func (m *ocrLifecycleRecorder) Handle(_ context.Context, req *modules.RequestContext) error {
	m.reserved = req.InputPages
	return nil
}
func (*ocrLifecycleRecorder) PostResponseEnabled() bool { return true }
func (m *ocrLifecycleRecorder) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.settled = req.InputPages
	m.model = req.OCRResponse.Model
	return nil
}

func TestRouterOCRRequiresNativeCapabilityAndSettlesPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"pages":[{"index":2,"markdown":"text","images":[]}],"model":"ocr-versioned","usage_info":{"pages_processed":1}}`)
	}))
	defer server.Close()
	recorder := &ocrLifecycleRecorder{}
	router := New(Config{Modules: modules.NewPipeline([]modules.Module{recorder}), Endpoints: []config.ProviderEndpointConfig{{Name: "ocr", Type: "mistral", BaseURL: server.URL, Models: []string{"public-ocr"}, ModelAliases: map[string]string{"public-ocr": "upstream-ocr"}, Capabilities: []string{"ocr"}}}}).(*Router)
	request := openai.OCRRequest{Model: "public-ocr", Document: openai.OCRDocument{Type: "document_url", DocumentURL: "https://example.test/a.pdf"}, Pages: "2"}
	response, err := router.OCR(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public-ocr"}, OCRRequest: &request, InputPages: request.ReservePages()})
	if err != nil || response.Model != "ocr-versioned" || recorder.reserved != 1 || recorder.settled != 1 || recorder.model != "ocr-versioned" {
		t.Fatalf("response=%+v recorder=%+v err=%v", response, recorder, err)
	}
	withoutCapability := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "ocr", Type: "mistral", BaseURL: server.URL, Models: []string{"public-ocr"}, Capabilities: []string{"chat"}}}}).(*Router)
	if _, err := withoutCapability.OCR(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public-ocr"}, OCRRequest: &request}); err == nil {
		t.Fatal("endpoint without OCR capability was selected")
	}
}
