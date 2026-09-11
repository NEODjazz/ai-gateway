package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func geminiOCRImageDocument() openai.OCRDocument {
	data := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	return openai.OCRDocument{Type: "image_url", ImageURL: "data:image/png;base64," + data}
}

func TestGeminiOCRContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiRequest
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/gemini-ocr:generateContent" || r.Header.Get("x-goog-api-key") != "secret" || json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatalf("request=%s body=%+v", r.URL.String(), body)
		}
		parts := body.Contents[0].Parts
		if len(parts) != 2 || parts[0].InlineData == nil || parts[0].InlineData.MIMEType != "image/png" || !strings.Contains(parts[1].Text, "page indices: [0]") || body.Generation.ResponseMIMEType != "application/json" || body.Generation.ResponseJSONSchema == nil {
			t.Fatalf("body=%+v", body)
		}
		_, _ = io.WriteString(w, `{"modelVersion":"gemini-ocr-2026","candidates":[{"content":{"parts":[{"text":"{\"pages\":[{\"index\":0,\"markdown\":\"# Invoice\"}]}"}]}}],"usageMetadata":{"promptTokenCount":258,"candidatesTokenCount":8,"totalTokenCount":266}}`)
	}))
	defer server.Close()
	response, err := NewGemini(server.URL, "secret", false).OCR(t.Context(), openai.OCRRequest{Model: "gemini-ocr", Document: geminiOCRImageDocument(), Pages: []int{0}, TableFormat: "markdown"})
	if err != nil || response.Model != "gemini-ocr-2026" || response.UsageInfo.PagesProcessed != 1 || response.UsageInfo.DocSizeBytes == nil || *response.UsageInfo.DocSizeBytes != 13 || len(response.Pages) != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGeminiOCRSelectedPDFPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiRequest
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Contents) != 1 || len(body.Contents[0].Parts) != 2 || body.Contents[0].Parts[0].InlineData == nil || body.Contents[0].Parts[0].InlineData.MIMEType != "application/pdf" || !strings.Contains(body.Contents[0].Parts[1].Text, "[1,3]") {
			t.Fatalf("body=%+v", body)
		}
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"{\"pages\":[{\"index\":1,\"markdown\":\"one\"},{\"index\":3,\"markdown\":\"three\"}]}"}]}}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":4,"totalTokenCount":24}}`)
	}))
	defer server.Close()
	pdf := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7\nbody"))
	request := openai.OCRRequest{Model: "gemini-ocr", Document: openai.OCRDocument{Type: "document_url", DocumentURL: "data:application/pdf;base64," + pdf}, Pages: "1,3"}
	response, err := NewGemini(server.URL, "secret", false).OCR(t.Context(), request)
	if err != nil || response.UsageInfo.PagesProcessed != 2 || len(response.Pages) != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGeminiOCRRejectsUnsupportedControlsBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	include := false
	for _, test := range []struct {
		name, param string
		request     openai.OCRRequest
	}{
		{"remote document", "document", openai.OCRRequest{Document: openai.OCRDocument{Type: "image_url", ImageURL: "https://example.test/a.png"}}},
		{"image extraction", "include_image_base64", openai.OCRRequest{Document: geminiOCRImageDocument(), IncludeImageBase64: &include}},
		{"html tables", "table_format", openai.OCRRequest{Document: geminiOCRImageDocument(), TableFormat: "html"}},
		{"image pages", "pages", openai.OCRRequest{Document: geminiOCRImageDocument(), Pages: []int{1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.request.Model = "model"
			_, err := NewGemini(server.URL, "secret", false).OCR(t.Context(), test.request)
			var providerErr *Error
			if !errors.As(err, &providerErr) || providerErr.Param != test.param {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestRouterRoutesNativeGeminiOCRAndSettlesPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/upstream:generateContent" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"{\"pages\":[{\"index\":0,\"markdown\":\"text\"}]}"}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)
	}))
	defer server.Close()
	recorder := &ocrLifecycleRecorder{}
	router := New(Config{Modules: modules.NewPipeline([]modules.Module{recorder}), Endpoints: []config.ProviderEndpointConfig{{Name: "gemini-ocr", Type: "gemini", BaseURL: server.URL, Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"ocr"}}}}).(*Router)
	request := openai.OCRRequest{Model: "public", Document: geminiOCRImageDocument()}
	response, err := router.OCR(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public"}, OCRRequest: &request, InputPages: request.ReservePages()})
	if err != nil || response.UsageInfo.PagesProcessed != 1 || recorder.reserved != 1 || recorder.settled != 1 || recorder.model != "upstream" {
		t.Fatalf("response=%+v recorder=%+v err=%v", response, recorder, err)
	}
}

func TestGeminiOCRRejectsMalformedAndUnexpectedPages(t *testing.T) {
	request := openai.OCRRequest{Model: "model", Document: geminiOCRImageDocument(), Pages: []int{0}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"{\"pages\":[{\"index\":1,\"markdown\":\"wrong\"}]}"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	defer server.Close()
	if _, err := NewGemini(server.URL, "secret", false).OCR(t.Context(), request); err == nil {
		t.Fatal("unexpected page was accepted")
	}
	for _, payload := range []string{
		`{"candidates":[],"usageMetadata":{"totalTokenCount":1}}`,
		`{"candidates":[{"content":{"parts":[{"text":"{\"pages\":[]}"}]}}],"usageMetadata":{"totalTokenCount":1}}`,
		`{"candidates":[{"content":{"parts":[{"text":"{\"pages\":[{\"index\":0,\"markdown\":\"ok\",\"extra\":true}]}"}]}}],"usageMetadata":{"totalTokenCount":1}}`,
	} {
		if _, err := decodeGeminiOCRResponse(io.NopCloser(strings.NewReader(payload)), "model", 1); err == nil {
			t.Fatalf("accepted payload=%s", payload)
		}
	}
}
