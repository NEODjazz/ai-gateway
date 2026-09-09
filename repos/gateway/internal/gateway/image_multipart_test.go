package gateway

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var testPNG = []byte("\x89PNG\r\n\x1a\nfixture")

func imageEditHTTPBody(t *testing.T, fields map[string]string, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range files {
		part, err := writer.CreateFormFile(name, name+".png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func TestImageEditUsesAuthenticatedPipelineAndReservesTPM(t *testing.T) {
	llm := &chatProvider{}
	rates := &embeddingTokenRateStore{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"image-*"}}}), llm, rates))
	body, contentType := imageEditHTTPBody(t, map[string]string{"provider": "images", "model": "image-model", "prompt": "remove background", "n": "2"}, map[string][]byte{"image": testPNG, "mask": testPNG})
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer client-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || llm.request.ImageEditRequest == nil || len(llm.request.ImageEditRequest.Images) != 1 || llm.request.ImageEditRequest.Mask == nil || llm.request.APIKey != "" {
		t.Fatalf("status=%d body=%s context=%+v", response.Code, response.Body.String(), llm.request)
	}
	if rates.tokens != openai.ImageEditReserveTokens(*llm.request.ImageEditRequest) {
		t.Fatalf("TPM reserve=%d", rates.tokens)
	}
}

func TestImageEditRejectsMalformedMultipartBeforeProvider(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields map[string]string
		files  map[string][]byte
	}{
		{name: "invalid signature", fields: map[string]string{"model": "image", "prompt": "edit"}, files: map[string][]byte{"image": []byte("not an image")}},
		{name: "unknown field", fields: map[string]string{"model": "image", "prompt": "edit", "unknown": "value"}, files: map[string][]byte{"image": testPNG}},
	} {
		t.Run(test.name, func(t *testing.T) {
			llm := &chatProvider{}
			body, contentType := imageEditHTTPBody(t, test.fields, test.files)
			request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
			request.Header.Set("Content-Type", contentType)
			response := httptest.NewRecorder()
			Routes(NewHandler(modules.NewPipeline(nil), llm)).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || llm.request.ImageEditRequest != nil {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
