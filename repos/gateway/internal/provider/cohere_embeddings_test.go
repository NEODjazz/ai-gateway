package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestCohereEmbeddingsV2ThroughRouter(t *testing.T) {
	var calls atomic.Int64
	vector := make([]float64, 256)
	for index := range vector {
		vector[index] = float64(index) / 256
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/proxy/v2/embed" || r.Header.Get("Authorization") != "Bearer provider-key" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var request cohereEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "embed-upstream" || request.InputType != "search_document" || len(request.Texts) != 2 || request.Texts[1] != "two" || len(request.EmbeddingTypes) != 1 || request.EmbeddingTypes[0] != "float" || request.OutputDimension == nil || *request.OutputDimension != 256 {
			t.Fatalf("request fields lost: %+v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": map[string]any{"float": [][]float64{vector, vector}}, "meta": map[string]any{"billed_units": map[string]any{"input_tokens": 7}}})
	}))
	defer server.Close()

	dimensions := 256
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "cohere-native", Type: "cohere", BaseURL: server.URL + "/proxy/v1", APIKey: "provider-key",
		Models: []string{"embed-public"}, ModelAliases: map[string]string{"embed-public": "embed-upstream"}, Capabilities: []string{"embeddings"},
	}}}).(*Router)
	response, err := router.Embeddings(context.Background(), modules.RequestContext{EmbeddingRequest: &openai.EmbeddingRequest{
		Model: "embed-public", Input: []string{"one", "two"}, InputType: "search_document", EncodingFormat: "base64", Dimensions: &dimensions,
	}, Request: openai.ChatCompletionRequest{Model: "embed-public"}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(response.Data[0].EmbeddingBase64)
	if calls.Load() != 1 || decodeErr != nil || len(decoded) != dimensions*4 || len(response.Data) != 2 || len(response.Data[0].Embedding) != 0 || response.Usage.PromptTokens != 7 || !response.UsageReported {
		t.Fatalf("response=%+v decoded=%d calls=%d err=%v", response, len(decoded), calls.Load(), decodeErr)
	}
}

func TestCohereEmbeddingsRejectUnsupportedParametersBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	dimensions := 3
	tooMany := make([]string, maxCohereEmbeddingInputs+1)
	for index := range tooMany {
		tooMany[index] = "text"
	}
	for _, test := range []struct {
		param   string
		request openai.EmbeddingRequest
	}{
		{param: "input_type", request: openai.EmbeddingRequest{Model: "embed", Input: "one"}},
		{param: "input_type", request: openai.EmbeddingRequest{Model: "embed", Input: "one", InputType: "image"}},
		{param: "input", request: openai.EmbeddingRequest{Model: "embed", Input: []int{1}, InputType: "search_query"}},
		{param: "input", request: openai.EmbeddingRequest{Model: "embed", Input: tooMany, InputType: "search_query"}},
		{param: "user", request: openai.EmbeddingRequest{Model: "embed", Input: "one", InputType: "classification", User: "user"}},
		{param: "dimensions", request: openai.EmbeddingRequest{Model: "embed", Input: "one", InputType: "clustering", Dimensions: &dimensions}},
		{param: "model", request: openai.EmbeddingRequest{Input: "one", InputType: "search_query"}},
	} {
		_, err := NewCohere(server.URL, "key").Embeddings(context.Background(), test.request)
		var failure *Error
		if !errors.As(err, &failure) || failure.Param != test.param {
			t.Fatalf("param=%s error=%v", test.param, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached upstream")
	}
}

func TestCohereEmbeddingsRejectMalformedResponseAndEstimateMissingUsage(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"embeddings":{"float":[[1,2],[3,4]]}}`,
		`{"embeddings":{"float":[[]]}}`,
		`{"embeddings":{"float":[[1,2]]},"meta":{"billed_units":{"input_tokens":-1}}}`,
		`{"embeddings":{"float":[[1e999,2]]}}`,
		`{"embeddings":{"float":[[1,2]]}} {}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, body) }))
		_, err := NewCohere(server.URL, "").Embeddings(context.Background(), openai.EmbeddingRequest{Model: "embed", Input: "one", InputType: "search_query"})
		server.Close()
		if err == nil {
			t.Fatalf("invalid response accepted: %s", body)
		}
	}

	for _, test := range []struct {
		name, usage   string
		reported      bool
		expectedToken int
	}{
		{name: "estimate", expectedToken: openai.EstimateContextTokens("one two")},
		{name: "reported zero", usage: `,"meta":{"billed_units":{"input_tokens":0}}`, reported: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, `{"embeddings":{"float":[[1,2]]}`+test.usage+`}`)
			}))
			defer server.Close()
			response, err := NewCohere(server.URL, "").Embeddings(context.Background(), openai.EmbeddingRequest{Model: "embed", Input: "one two", InputType: "search_query"})
			if err != nil || response.UsageReported != test.reported || response.Usage.PromptTokens != test.expectedToken {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
}

func TestOtherEmbeddingAdaptersRejectCohereInputType(t *testing.T) {
	request := openai.EmbeddingRequest{Model: "embed", Input: "one", InputType: "search_query"}
	for name, client := range map[string]EmbeddingClient{
		"compatible": NewOpenAICompatible("http://unused.invalid", "", false),
		"gemini":     NewGemini("http://unused.invalid", "", false),
		"ollama":     NewOllama("http://unused.invalid", false),
		"demo":       Demo{},
	} {
		t.Run(name, func(t *testing.T) {
			var failure *Error
			_, err := client.Embeddings(context.Background(), request)
			if !errors.As(err, &failure) || failure.Param != "input_type" || failure.UpstreamCode != "unsupported_parameter" || !strings.Contains(err.Error(), "input_type") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCohereEmbeddingsRefuseRedirectAndHonorCancellation(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/credential-target", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := NewCohere(server.URL, "secret")
	request := openai.EmbeddingRequest{Model: "embed", Input: "one", InputType: "search_query"}
	if _, err := client.Embeddings(context.Background(), request); err == nil || calls.Load() != 1 {
		t.Fatalf("redirect followed: calls=%d err=%v", calls.Load(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Embeddings(ctx, request); err == nil || calls.Load() != 1 {
		t.Fatalf("cancellation ignored: calls=%d err=%v", calls.Load(), err)
	}
}
