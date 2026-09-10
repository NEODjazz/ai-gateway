package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestGeminiImageGenerationContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/gemini-image:generateContent" || r.URL.RawQuery != "" || r.Header.Get("x-goog-api-key") != "secret" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.String(), r.Header)
		}
		var body struct {
			Contents   []geminiContent `json:"contents"`
			Generation struct {
				ResponseModalities []string           `json:"responseModalities"`
				ImageConfig        *geminiImageConfig `json:"imageConfig"`
			} `json:"generationConfig"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Contents) != 1 || len(body.Contents[0].Parts) != 1 || body.Contents[0].Parts[0].Text != "draw" || len(body.Generation.ResponseModalities) != 1 || body.Generation.ResponseModalities[0] != "IMAGE" || body.Generation.ImageConfig == nil || body.Generation.ImageConfig.AspectRatio != "16:9" || body.Generation.ImageConfig.ImageSize != "2K" {
			t.Fatalf("request=%+v err=%v", body, err)
		}
		_, _ = fmt.Fprint(w, `{"responseId":"image-id","candidates":[{"index":0,"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":10}}`)
	}))
	defer server.Close()

	n := 1
	response, err := NewGemini(server.URL, "secret", false).GenerateImage(context.Background(), openai.ImageGenerationRequest{Model: "models/gemini-image", Prompt: "draw", N: &n, ResponseFormat: "b64_json", Resolution: "2K", AspectRatio: "16:9"})
	if err != nil || len(response.Data) != 1 || response.Data[0].B64JSON != "aW1hZ2U=" || response.Data[0].MediaType != "image/png" || response.Usage == nil || response.Usage.InputTokens != 3 || response.Usage.OutputTokens != 7 || response.Usage.TotalTokens != 10 || response.Created <= 0 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGeminiImageGenerationOmitsAutomaticImageConfigValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		generation := body["generationConfig"].(map[string]any)
		if generation["imageConfig"] != nil {
			t.Fatalf("imageConfig=%v", generation["imageConfig"])
		}
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"aW1hZ2U="}}]}}],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":2}}`)
	}))
	defer server.Close()
	_, err := NewGemini(server.URL, "secret", false).GenerateImage(t.Context(), openai.ImageGenerationRequest{Model: "image", Prompt: "draw", AspectRatio: "auto"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGeminiImageGenerationRejectsUnsupportedParametersBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	two := 2
	compression := 90
	seed := int64(1)
	tests := []struct {
		name    string
		request openai.ImageGenerationRequest
		param   string
	}{
		{"n", openai.ImageGenerationRequest{N: &two}, "n"},
		{"response format", openai.ImageGenerationRequest{ResponseFormat: "url"}, "response_format"},
		{"quality", openai.ImageGenerationRequest{Quality: "high"}, "quality"},
		{"size", openai.ImageGenerationRequest{Size: "1024x1024"}, "size"},
		{"style", openai.ImageGenerationRequest{Style: "vivid"}, "style"},
		{"user", openai.ImageGenerationRequest{User: "user"}, "user"},
		{"background", openai.ImageGenerationRequest{Background: "opaque"}, "background"},
		{"output format", openai.ImageGenerationRequest{OutputFormat: "png"}, "output_format"},
		{"compression", openai.ImageGenerationRequest{OutputCompression: &compression}, "output_compression"},
		{"seed", openai.ImageGenerationRequest{Seed: &seed}, "seed"},
		{"aspect ratio", openai.ImageGenerationRequest{AspectRatio: "7:5"}, "aspect_ratio"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.request.Model = "image"
			test.request.Prompt = "draw"
			_, err := NewGemini(server.URL, "secret", false).GenerateImage(t.Context(), test.request)
			var providerErr *Error
			if !errors.As(err, &providerErr) || providerErr.UpstreamCode != "unsupported_parameter" || providerErr.Param != test.param {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("network calls=%d", calls)
	}
}

func TestGeminiImageGenerationRejectsMalformedResponses(t *testing.T) {
	validCandidate := `{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}]}}`
	tests := []string{
		`{}`,
		`{"candidates":[],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":2}}`,
		`{"candidates":[` + validCandidate + `,` + validCandidate + `],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":2}}`,
		`{"candidates":[{"content":{"parts":[{"text":"no image"}]}}],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":2}}`,
		`{"candidates":[{"content":{"parts":[{"text":"caption"},{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}]}}],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":2}}`,
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"invalid"}}]}}],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":2}}`,
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"text/html","data":"aW1hZ2U="}}]}}],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":2}}`,
		`{"candidates":[` + validCandidate + `],"usageMetadata":{"promptTokenCount":3,"totalTokenCount":2}}`,
		`{"candidates":[` + validCandidate + `],"usageMetadata":{"promptTokenCount":-1,"totalTokenCount":2}}`,
		`{"candidates":[` + validCandidate + `],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":-1,"totalTokenCount":2}}`,
		`{"candidates":[` + validCandidate + `],"usageMetadata":{"promptTokenCount":1,"thoughtsTokenCount":-1,"totalTokenCount":2}}`,
		`{"candidates":[` + validCandidate + `],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"thoughtsTokenCount":1,"totalTokenCount":3}}`,
		`{"candidates":[` + validCandidate + `],"usageMetadata":{"promptTokenCount":1,"cachedContentTokenCount":2,"totalTokenCount":2}}`,
		`{"candidates":[` + validCandidate + `],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":2}} trailing`,
	}
	request := openai.ImageGenerationRequest{Model: "image", Prompt: "draw"}
	for index, body := range tests {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			if _, err := decodeGeminiImageResponse(strings.NewReader(body), request); err == nil {
				t.Fatalf("accepted body: %s", body)
			}
		})
	}
}

func TestGeminiImageGenerationResponseLimit(t *testing.T) {
	body := strings.NewReader(strings.Repeat("x", maxImageGenerationResponseBytes+1))
	if _, err := decodeGeminiImageResponse(body, openai.ImageGenerationRequest{Model: "image", Prompt: "draw"}); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestGeminiImageGenerationMapsPolicyFailure(t *testing.T) {
	_, err := decodeGeminiImageResponse(strings.NewReader(`{"promptFeedback":{"blockReason":"SAFETY"}}`), openai.ImageGenerationRequest{Model: "image", Prompt: "draw"})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.Class != FailureContentPolicy {
		t.Fatalf("err=%v", err)
	}
}

func TestRouterRoutesNativeGeminiImageGenerationWithAlias(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/upstream-image:generateContent" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`)
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "gemini-images", Type: "gemini", BaseURL: server.URL, APIKey: "secret", Models: []string{"public-image"},
		ModelAliases: map[string]string{"public-image": "upstream-image"}, Capabilities: []string{"image_generation"},
	}}}).(*Router)
	request := openai.ImageGenerationRequest{Model: "public-image", Prompt: "draw"}
	response, err := router.GenerateImage(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, ImageGenerationRequest: &request})
	if err != nil || len(response.Data) != 1 || response.Usage == nil || response.Usage.TotalTokens != 3 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
