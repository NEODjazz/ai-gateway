package provider

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func editAttachment() openai.ImageAttachment {
	attachment, err := openai.ParseDataImageURL("data:image/png;base64,iVBORw0KGgpmaXh0dXJl")
	if err != nil {
		panic(err)
	}
	return attachment
}

func TestOpenAICompatibleImageEditContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/edits" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(openai.MaxInferenceBodyBytes); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("provider") != "" || r.FormValue("model") != "upstream-image" || r.FormValue("prompt") != "edit" || r.FormValue("n") != "1" {
			t.Fatalf("form=%v", r.MultipartForm.Value)
		}
		files := r.MultipartForm.File["image"]
		if len(files) != 1 || files[0].Header.Get("Content-Type") != "image/png" {
			t.Fatalf("files=%+v", files)
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if !bytes.Equal(data, []byte("\x89PNG\r\n\x1a\nfixture")) {
			t.Fatalf("image=%q", data)
		}
		_, _ = w.Write([]byte(`{"created":7,"data":[{"url":"https://images.example/edited.png"}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`))
	}))
	defer server.Close()

	n := 1
	request := openai.ImageEditRequest{Provider: "deployment", Model: "upstream-image", Prompt: "edit", Images: []openai.ImageAttachment{editAttachment()}, N: &n}
	response, err := NewOpenAICompatible(server.URL+"/v1", "secret", false).EditImage(t.Context(), request)
	if err != nil || response.Usage == nil || response.Usage.TotalTokens != 8 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestAzureImageEditContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/images/edits" || r.URL.Query().Get("api-version") != "2025-04-01-preview" || r.Header.Get("api-key") != "secret" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected Azure request: %s headers=%v", r.URL.String(), r.Header)
		}
		_, _ = w.Write([]byte(`{"created":7,"data":[{"url":"https://images.example/edited.png"}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`))
	}))
	defer server.Close()
	request := openai.ImageEditRequest{Model: "image", Prompt: "edit", Images: []openai.ImageAttachment{editAttachment()}}
	response, err := NewAzureOpenAI(server.URL, "secret", false, "2025-04-01-preview", "api_key").EditImage(t.Context(), request)
	if err != nil || len(response.Data) != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestRouterImageEditRequiresCapabilityAndAppliesAlias(t *testing.T) {
	var model string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(openai.MaxInferenceBodyBytes); err != nil {
			t.Fatal(err)
		}
		model = r.FormValue("model")
		_, _ = w.Write([]byte(`{"created":7,"data":[{"url":"https://images.example/edited.png"}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "edits", Type: "openai-compatible", BaseURL: server.URL, Models: []string{"public-image"},
		ModelAliases: map[string]string{"public-image": "upstream-image"}, Capabilities: []string{"image_edit"},
	}}}).(*Router)
	request := openai.ImageEditRequest{Model: "public-image", Prompt: "edit", Images: []openai.ImageAttachment{editAttachment()}}
	response, err := router.EditImage(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, ImageEditRequest: &request})
	if err != nil || model != "upstream-image" || len(response.Data) != 1 {
		t.Fatalf("response=%+v model=%q err=%v", response, model, err)
	}
}

func TestMistralImageEditRejectedBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	client := NewMistral(server.URL, "secret", false)
	_, err := client.EditImage(t.Context(), openai.ImageEditRequest{Model: "image", Prompt: "edit", Images: []openai.ImageAttachment{editAttachment()}})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.UpstreamCode != "unsupported_operation" || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
