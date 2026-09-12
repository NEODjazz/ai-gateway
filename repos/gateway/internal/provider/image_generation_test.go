package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type imageStreamUsageModule struct{ total int }

func (*imageStreamUsageModule) Name() string              { return "image-stream-usage" }
func (*imageStreamUsageModule) Required() bool            { return true }
func (*imageStreamUsageModule) PostResponseEnabled() bool { return true }
func (*imageStreamUsageModule) Handle(context.Context, *modules.RequestContext) error {
	return nil
}
func (m *imageStreamUsageModule) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	if req.ImageGenerationResponse != nil && req.ImageGenerationResponse.Usage != nil {
		m.total = req.ImageGenerationResponse.Usage.TotalTokens
	}
	return nil
}

func TestOpenAICompatibleImageGenerationStreamingContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["stream"] != true || request["partial_images"] != float64(1) || request["resolution"] != "2K" || request["aspect_ratio"] != "16:9" || request["seed"] != float64(42) || request["provider"] != nil {
			t.Fatalf("request=%#v err=%v", request, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"cGFydGlhbA==\",\"created_at\":7,\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\",\"partial_image_index\":0}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"image_generation.completed\",\"b64_json\":\"ZmluYWw=\",\"created_at\":8,\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\",\"usage\":{\"input_tokens\":3,\"output_tokens\":5,\"total_tokens\":8}}\n\n"))
	}))
	defer server.Close()

	partials := 1
	seed := int64(42)
	request := openai.ImageGenerationRequest{Provider: "deployment", Model: "image", Prompt: "draw", Resolution: "2K", AspectRatio: "16:9", Seed: &seed, Stream: true, PartialImages: &partials}
	var payloads []string
	response, err := NewOpenAICompatible(server.URL, "", true).StreamGenerateImage(t.Context(), request, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil || len(payloads) != 2 || response.Created != 8 || response.Usage == nil || response.Usage.TotalTokens != 8 || response.Data[0].B64JSON != "ZmluYWw=" {
		t.Fatalf("response=%+v payloads=%v err=%v", response, payloads, err)
	}
}

func TestOpenAICompatibleImageGenerationStreamRejectsNonSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"image_generation.completed"}`))
	}))
	defer server.Close()
	_, err := NewOpenAICompatible(server.URL, "", true).StreamGenerateImage(t.Context(), openai.ImageGenerationRequest{Model: "image", Prompt: "draw", Stream: true}, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "non-SSE") {
		t.Fatalf("err=%v", err)
	}
}

func TestImageGenerationStreamRejectsMalformedLifecycle(t *testing.T) {
	partialImages := 1
	request := openai.ImageGenerationRequest{Model: "image", Prompt: "draw", Stream: true, PartialImages: &partialImages}
	for name, stream := range map[string]string{
		"missing completion": "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"cGFydGlhbA==\",\"partial_image_index\":0}\n\n",
		"out of order":       "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"cGFydGlhbA==\",\"partial_image_index\":1}\n\n",
		"invalid usage":      "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"ZmluYWw=\",\"usage\":{\"input_tokens\":3,\"output_tokens\":5,\"total_tokens\":7}}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := streamImageGeneration(strings.NewReader(stream), request, func(string) error { return nil }); err == nil {
				t.Fatal("invalid image stream accepted")
			}
		})
	}
}

func TestRouterStreamsImageGenerationAndSettlesUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"image_generation.completed\",\"b64_json\":\"ZmluYWw=\",\"created_at\":8,\"output_format\":\"png\",\"usage\":{\"input_tokens\":3,\"output_tokens\":5,\"total_tokens\":8}}\n\n"))
	}))
	defer server.Close()
	usage := &imageStreamUsageModule{}
	router := New(Config{Modules: modules.NewPipeline([]modules.Module{usage}), Endpoints: []config.ProviderEndpointConfig{{Name: "images", Type: "openai-compatible", BaseURL: server.URL, Stream: true, Models: []string{"image"}, Capabilities: []string{"image_generation"}}}}).(*Router)
	request := openai.ImageGenerationRequest{Model: "image", Prompt: "draw", Stream: true}
	writes := 0
	response, streamed, err := router.StreamGenerateImage(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "image"}, ImageGenerationRequest: &request}, func(string) error { writes++; return nil })
	if err != nil || !streamed || writes != 1 || response.Usage == nil || response.Usage.TotalTokens != 8 || usage.total != 8 {
		t.Fatalf("response=%+v streamed=%v writes=%d settled=%d err=%v", response, streamed, writes, usage.total, err)
	}
}

func TestRouterImageGenerationStreamFallsBackBeforeFirstEvent(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer first.Close()
	secondCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"image_generation.completed\",\"b64_json\":\"ZmluYWw=\",\"usage\":{\"input_tokens\":3,\"output_tokens\":5,\"total_tokens\":8}}\n\n"))
	}))
	defer second.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "first", Type: "openai-compatible", BaseURL: first.URL, Stream: true, Models: []string{"image"}, Capabilities: []string{"image_generation"}, Priority: 1},
		{Name: "second", Type: "openai-compatible", BaseURL: second.URL, Stream: true, Models: []string{"image"}, Capabilities: []string{"image_generation"}, Priority: 2},
	}}).(*Router)
	request := openai.ImageGenerationRequest{Model: "image", Prompt: "draw", Stream: true}
	_, streamed, err := router.StreamGenerateImage(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "image"}, ImageGenerationRequest: &request}, func(string) error { return nil })
	if err != nil || !streamed || secondCalls != 1 {
		t.Fatalf("streamed=%v secondCalls=%d err=%v", streamed, secondCalls, err)
	}
}

func TestRouterImageGenerationStreamDoesNotFallbackAfterFirstEvent(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"cGFydGlhbA==\",\"partial_image_index\":0}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"image_generation.completed\",\"b64_json\":\"ZmluYWw=\",\"usage\":{\"input_tokens\":3,\"output_tokens\":5,\"total_tokens\":7}}\n\n"))
	}))
	defer first.Close()
	secondCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { secondCalls++ }))
	defer second.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "first", Type: "openai-compatible", BaseURL: first.URL, Stream: true, Models: []string{"image"}, Capabilities: []string{"image_generation"}, Priority: 1},
		{Name: "second", Type: "openai-compatible", BaseURL: second.URL, Stream: true, Models: []string{"image"}, Capabilities: []string{"image_generation"}, Priority: 2},
	}}).(*Router)
	partials := 1
	request := openai.ImageGenerationRequest{Model: "image", Prompt: "draw", Stream: true, PartialImages: &partials}
	writes := 0
	_, streamed, err := router.StreamGenerateImage(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "image"}, ImageGenerationRequest: &request}, func(string) error { writes++; return nil })
	if err == nil || !streamed || writes != 1 || secondCalls != 0 {
		t.Fatalf("streamed=%v writes=%d secondCalls=%d err=%v", streamed, writes, secondCalls, err)
	}
}

func TestOpenAICompatibleImageGenerationContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["provider"] != nil || request["prompt"] != "draw" || request["n"] != float64(1) || request["resolution"] != "2K" || request["aspect_ratio"] != "16:9" || request["seed"] != float64(42) {
			t.Fatalf("request=%#v err=%v", request, err)
		}
		_, _ = w.Write([]byte(`{"created":7,"data":[{"url":"https://images.example/result.png","revised_prompt":"draw clearly"}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}`))
	}))
	defer server.Close()
	n := 1
	seed := int64(42)
	response, err := NewOpenAICompatible(server.URL+"/v1", "secret", false).GenerateImage(context.Background(), openai.ImageGenerationRequest{Provider: "deployment", Model: "image", Prompt: "draw", N: &n, Resolution: "2K", AspectRatio: "16:9", Seed: &seed})
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
		{Data: []openai.ImageData{{B64JSON: "aW1hZ2U=", MediaType: "text/html"}}, Usage: &openai.ImageUsage{}},
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
