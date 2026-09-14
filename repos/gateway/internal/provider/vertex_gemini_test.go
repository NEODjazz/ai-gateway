package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestVertexGeminiUsesPublisherModelPathAndWorkloadIdentity(t *testing.T) {
	var tokenCalls atomic.Int64
	var generationCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metadata/token":
			tokenCalls.Add(1)
			if r.Header.Get("Metadata-Flavor") != "Google" {
				t.Error("metadata request omitted required header")
			}
			w.Header().Set("Metadata-Flavor", "Google")
			_, _ = fmt.Fprint(w, `{"access_token":"workload-token","expires_in":3600,"token_type":"Bearer"}`)
		case "/v1/projects/project-1/locations/us-central1/publishers/google/models/gemini-test:generateContent":
			generationCalls.Add(1)
			if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer workload-token" || r.Header.Get("x-goog-api-key") != "" {
				t.Errorf("invalid generation transport: %s headers=%v", r.URL.String(), r.Header)
			}
			var body map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body["contents"]) == 0 || len(body["generateContentRequest"]) != 0 {
				t.Errorf("invalid Vertex request body: %v", body)
			}
			_, _ = fmt.Fprint(w, `{"responseId":"vertex-response","candidates":[{"index":0,"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)
		case "/v1/projects/project-1/locations/us-central1/publishers/google/models/gemini-test:countTokens":
			if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer workload-token" {
				t.Errorf("invalid count transport: %s headers=%v", r.URL.String(), r.Header)
			}
			var body map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body["contents"]) == 0 || len(body["generateContentRequest"]) != 0 {
				t.Errorf("invalid Vertex count body: %v", body)
			}
			_, _ = fmt.Fprint(w, `{"totalTokens":7}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	baseURL := server.URL + "/v1/projects/project-1/locations/us-central1/publishers/google"
	client := NewVertexGemini(baseURL, false)
	client.gemini.tokenSource.metadataURL = server.URL + "/metadata/token"
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || len(response.Choices) != 1 || openai.ContentText(response.Choices[0].Message.Content) != "ok" || response.Usage.TotalTokens != 3 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	count, err := client.CountTokens(t.Context(), TokenCountRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || count.InputTokens != 7 || count.Source != "vertex-gemini" {
		t.Fatalf("count=%+v err=%v", count, err)
	}
	if tokenCalls.Load() != 1 || generationCalls.Load() != 1 {
		t.Fatalf("token calls=%d generation calls=%d", tokenCalls.Load(), generationCalls.Load())
	}
}

func TestVertexGeminiStreamsFromPublisherModelPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata/token" {
			w.Header().Set("Metadata-Flavor", "Google")
			_, _ = fmt.Fprint(w, `{"access_token":"workload-token","expires_in":3600,"token_type":"Bearer"}`)
			return
		}
		if r.URL.Path != "/v1/projects/project-1/locations/us-central1/publishers/google/models/gemini-test:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" || r.Header.Get("Authorization") != "Bearer workload-token" {
			t.Errorf("invalid stream request: %s headers=%v", r.URL.String(), r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"responseId\":\"vertex-stream\",\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\" world\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":2,\"totalTokenCount\":4}}\n\n")
	}))
	defer server.Close()
	client := NewVertexGemini(server.URL+"/v1/projects/project-1/locations/us-central1/publishers/google", true)
	client.gemini.tokenSource.metadataURL = server.URL + "/metadata/token"
	var chunks []string
	response, err := client.StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, func(payload string) error {
		chunks = append(chunks, payload)
		return nil
	})
	if err != nil || openai.ContentText(response.Choices[0].Message.Content) != "hello world" || response.Usage.TotalTokens != 4 || len(chunks) != 2 || !strings.Contains(chunks[1], `"total_tokens":4`) {
		t.Fatalf("response=%+v chunks=%v err=%v", response, chunks, err)
	}
}

func TestVertexGeminiEmbeddingsUsePredictAndAggregateUsage(t *testing.T) {
	var tokenCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata/token" {
			tokenCalls.Add(1)
			w.Header().Set("Metadata-Flavor", "Google")
			_, _ = fmt.Fprint(w, `{"access_token":"workload-token","expires_in":3600,"token_type":"Bearer"}`)
			return
		}
		if r.URL.Path != "/v1/projects/project-1/locations/us-central1/publishers/google/models/text-embedding-005:predict" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer workload-token" {
			t.Errorf("invalid embedding transport: %s headers=%v", r.URL.String(), r.Header)
		}
		var body struct {
			Instances []struct {
				Content  string `json:"content"`
				TaskType string `json:"task_type"`
			} `json:"instances"`
			Parameters struct {
				AutoTruncate         bool `json:"autoTruncate"`
				OutputDimensionality int  `json:"outputDimensionality"`
			} `json:"parameters"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Instances) != 2 || body.Instances[0].Content != "one" || body.Instances[1].Content != "two" || body.Instances[0].TaskType != "RETRIEVAL_DOCUMENT" || body.Parameters.AutoTruncate || body.Parameters.OutputDimensionality != 2 {
			t.Errorf("embedding request=%+v", body)
		}
		_, _ = fmt.Fprint(w, `{"predictions":[{"embeddings":{"values":[1,2],"statistics":{"token_count":3}}},{"embeddings":{"values":[3,4],"statistics":{"tokenCount":5}}}]}`)
	}))
	defer server.Close()
	dimensions := 2
	client := NewVertexGemini(server.URL+"/v1/projects/project-1/locations/us-central1/publishers/google", false)
	client.gemini.tokenSource.metadataURL = server.URL + "/metadata/token"
	response, err := client.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "text-embedding-005", Input: []string{"one", "two"}, InputType: "search_document", Dimensions: &dimensions})
	if err != nil || len(response.Data) != 2 || response.Data[1].Index != 1 || response.Data[1].Embedding[0] != 3 || response.Usage.PromptTokens != 8 || response.Usage.TotalTokens != 8 || !response.UsageReported {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("token calls=%d", tokenCalls.Load())
	}
}

func TestVertexGeminiEmbeddingsRejectInvalidInputsAndResponses(t *testing.T) {
	client := NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)
	dimension := 3073
	for _, request := range []openai.EmbeddingRequest{
		{Model: "embed", Input: []int{1}},
		{Model: "embed", Input: []string{"1", "2", "3", "4", "5", "6"}},
		{Model: "gemini-embedding-001", Input: []string{"one", "two"}},
		{Model: "embed", Input: "one", Metadata: map[string]string{"key": "value"}},
		{Model: "embed", Input: "one", EncodingFormat: "base64"},
		{Model: "embed", Input: "one", InputType: "unsupported"},
		{Model: "embed", Input: "one", Dimensions: &dimension},
	} {
		if _, err := client.Embeddings(t.Context(), request); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}

	for _, test := range []struct {
		body  string
		input any
	}{
		{body: `{}`, input: "one"},
		{body: `{"predictions":[]}`, input: "one"},
		{body: `{"predictions":[{"embeddings":{"values":[]}}]}`, input: "one"},
		{body: `{"predictions":[{"embeddings":{"values":[1,2],"statistics":{"token_count":-1}}}]}`, input: "one"},
		{body: `{"predictions":[{"embeddings":{"values":[1,2]}},{"embeddings":{"values":[3,4],"statistics":{"token_count":1}}}]}`, input: []string{"one", "two"}},
		{body: `{"predictions":[{"embeddings":{"values":[1e999]}}]}`, input: "one"},
		{body: `{"predictions":[{"embeddings":{"values":[1,2]}}],"error":{"code":500}}`, input: "one"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/metadata/token" {
				w.Header().Set("Metadata-Flavor", "Google")
				_, _ = fmt.Fprint(w, `{"access_token":"token","expires_in":3600,"token_type":"Bearer"}`)
				return
			}
			_, _ = fmt.Fprint(w, test.body)
		}))
		invalid := NewVertexGemini(server.URL+"/v1/projects/project-1/locations/us-central1/publishers/google", false)
		invalid.gemini.tokenSource.metadataURL = server.URL + "/metadata/token"
		_, err := invalid.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "embed", Input: test.input})
		server.Close()
		if err == nil {
			t.Fatalf("invalid response accepted: %s", test.body)
		}
	}
}

func TestVertexGeminiEmbeddingsEstimateUsageWhenProviderOmitsStatistics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata/token" {
			w.Header().Set("Metadata-Flavor", "Google")
			_, _ = fmt.Fprint(w, `{"access_token":"token","expires_in":3600,"token_type":"Bearer"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"predictions":[{"embeddings":{"values":[1,2]}}]}`)
	}))
	defer server.Close()
	client := NewVertexGemini(server.URL+"/v1/projects/project-1/locations/us-central1/publishers/google", false)
	client.gemini.tokenSource.metadataURL = server.URL + "/metadata/token"
	response, err := client.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "text-embedding-005", Input: "one two"})
	if err != nil || response.UsageReported || response.Usage.PromptTokens <= 0 || response.Usage.TotalTokens != response.Usage.PromptTokens {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestVertexGeminiRejectsMalformedResourceBaseURL(t *testing.T) {
	for _, value := range []string{
		"https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1",
		"https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/other",
		"https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/../publishers/google",
		"https://user@example.com/v1/projects/project-1/locations/us-central1/publishers/google",
	} {
		if validVertexGeminiBaseURL(value) {
			t.Fatalf("invalid base URL accepted: %q", value)
		}
	}
}

func TestVertexGeminiDiscoveryFailsWithoutOutboundRequest(t *testing.T) {
	router := New(Config{}).(*Router)
	baseURL := "https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google"
	if _, err := router.CreateProvider(ManagedProvider{ID: "vertex", Type: "vertex-gemini", BaseURL: baseURL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.DiscoverProviderModels(t.Context(), "vertex", ""); !errors.Is(err, ErrProviderProbeFailed) {
		t.Fatalf("discovery error=%v", err)
	}
}
