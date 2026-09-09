package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestOpenAICompatibleImageGenerationContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["provider"] != nil || request["prompt"] != "draw" || request["n"] != float64(1) {
			t.Fatalf("request=%#v err=%v", request, err)
		}
		_, _ = w.Write([]byte(`{"created":7,"data":[{"url":"https://images.example/result.png","revised_prompt":"draw clearly"}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`))
	}))
	defer server.Close()
	n := 1
	response, err := NewOpenAICompatible(server.URL+"/v1", "secret", false).GenerateImage(context.Background(), openai.ImageGenerationRequest{Provider: "deployment", Model: "image", Prompt: "draw", N: &n})
	if err != nil || response.Created != 7 || response.Usage == nil || response.Usage.TotalTokens != 8 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestAzureImageGenerationContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/images/generations" || r.URL.Query().Get("api-version") != "2025-04-01-preview" || r.Header.Get("api-key") != "secret" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected Azure request: %s headers=%v", r.URL.String(), r.Header)
		}
		_, _ = w.Write([]byte(`{"created":7,"data":[{"url":"https://images.example/result.png"}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`))
	}))
	defer server.Close()

	response, err := NewAzureOpenAI(server.URL, "secret", false, "2025-04-01-preview", "api_key").GenerateImage(t.Context(), openai.ImageGenerationRequest{Model: "image", Prompt: "draw"})
	if err != nil || len(response.Data) != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestRouterImageGenerationRequiresCapabilityAndAppliesAlias(t *testing.T) {
	var model string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openai.ImageGenerationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		model = request.Model
		_, _ = w.Write([]byte(`{"created":7,"data":[{"url":"https://images.example/result.png"}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "images", Type: "openai-compatible", BaseURL: server.URL, Models: []string{"public-image"},
		ModelAliases: map[string]string{"public-image": "upstream-image"}, Capabilities: []string{"image_generation"},
	}}}).(*Router)
	request := openai.ImageGenerationRequest{Model: "public-image", Prompt: "draw"}
	response, err := router.GenerateImage(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, ImageGenerationRequest: &request})
	if err != nil || model != "upstream-image" || len(response.Data) != 1 {
		t.Fatalf("response=%+v model=%q err=%v", response, model, err)
	}
}

func TestRouterImageGenerationSkipsUnknownGuardrailPolicy(t *testing.T) {
	misconfiguredCalls := 0
	misconfigured := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		misconfiguredCalls++
	}))
	defer misconfigured.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"created":7,"data":[{"url":"https://images.example/result.png"}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`))
	}))
	defer fallback.Close()

	router := New(Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "misconfigured", Type: "openai-compatible", BaseURL: misconfigured.URL, Models: []string{"image"}, Capabilities: []string{"image_generation"}, GuardrailPolicy: "missing", Priority: 1},
		{Name: "fallback", Type: "openai-compatible", BaseURL: fallback.URL, Models: []string{"image"}, Capabilities: []string{"image_generation"}, Priority: 2},
	}}).(*Router)
	request := openai.ImageGenerationRequest{Model: "image", Prompt: "draw"}
	response, err := router.GenerateImage(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, ImageGenerationRequest: &request})
	if err != nil || len(response.Data) != 1 || misconfiguredCalls != 0 {
		t.Fatalf("response=%+v calls=%d err=%v", response, misconfiguredCalls, err)
	}
}

func TestImageGenerationResponseValidation(t *testing.T) {
	request := openai.ImageGenerationRequest{Model: "image", Prompt: "draw"}
	valid := openai.ImageGenerationResponse{Data: []openai.ImageData{{B64JSON: "aW1hZ2U="}}, Usage: &openai.ImageUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}}
	if err := validateImageGenerationResponse(valid, request); err != nil {
		t.Fatal(err)
	}
	for _, response := range []openai.ImageGenerationResponse{
		{},
		{Created: -1, Data: []openai.ImageData{{URL: "https://example.com/image"}}, Usage: &openai.ImageUsage{}},
		{Quality: "unknown", Data: []openai.ImageData{{URL: "https://example.com/image"}}, Usage: &openai.ImageUsage{}},
		{Data: []openai.ImageData{{URL: "file:///secret"}}},
		{Data: []openai.ImageData{{URL: "https://user:pass@example.com/image"}}},
		{Data: []openai.ImageData{{B64JSON: "invalid"}}},
		{Data: []openai.ImageData{{B64JSON: "aW1hZ2U=", URL: "https://example.com/image"}}},
		{Data: []openai.ImageData{{URL: "https://example.com/image"}}, Usage: &openai.ImageUsage{InputTokens: 2, OutputTokens: 2, TotalTokens: 3}},
	} {
		if err := validateImageGenerationResponse(response, request); err == nil {
			t.Fatalf("invalid response accepted: %+v", response)
		}
	}
}

func TestMistralImageGenerationRejectedBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls++
	}))
	defer server.Close()

	client := NewMistral(server.URL, "secret", false)
	_, err := client.GenerateImage(t.Context(), openai.ImageGenerationRequest{Model: "image", Prompt: "draw"})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.UpstreamCode != "unsupported_operation" || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
