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

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestVoyageNativeEmbeddingAndRerankProtocols(t *testing.T) {
	var embeddingCalls, rerankCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer voyage-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("method=%s authorization=%q content-type=%q", r.Method, r.Header.Get("Authorization"), r.Header.Get("Content-Type"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/proxy/v1/embeddings":
			embeddingCalls.Add(1)
			if body["model"] != "voyage-4" || body["input_type"] != "document" || body["truncation"] != false || body["output_dimension"] != float64(2) || body["output_dtype"] != "float" || body["encoding_format"] != "base64" {
				t.Errorf("embedding request=%#v", body)
			}
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"index":0,"embedding":"AACAPwAAAEA="}],"model":"voyage-4","usage":{"total_tokens":7}}`)
		case "/proxy/v1/rerank":
			rerankCalls.Add(1)
			if body["model"] != "rerank-3" || body["query"] != "query" || body["top_k"] != float64(1) || body["truncation"] != false || body["top_n"] != nil || body["return_documents"] != nil {
				t.Errorf("rerank request=%#v", body)
			}
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"index":1,"relevance_score":0.9}],"model":"rerank-3","usage":{"total_tokens":11}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewVoyage(server.URL+"/proxy", "voyage-key")
	dimensions := 2
	embedding, err := client.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "voyage-4", Input: "document", InputType: "document", EncodingFormat: "base64", Dimensions: &dimensions, OutputDType: "float"})
	if err != nil {
		t.Fatal(err)
	}
	if !embedding.UsageReported || embedding.Usage.PromptTokens != 7 || embedding.Usage.TotalTokens != 7 || len(embedding.Data) != 1 || embedding.Data[0].EmbeddingBase64 != "AACAPwAAAEA=" {
		t.Fatalf("embedding=%+v", embedding)
	}

	top := 1
	reranked, err := client.Rerank(t.Context(), openai.RerankRequest{Model: "rerank-3", Query: "query", Documents: []any{"first", "second"}, TopN: &top})
	if err != nil {
		t.Fatal(err)
	}
	if len(reranked.Results) != 1 || reranked.Results[0].Index != 1 || reranked.Meta == nil || reranked.Meta.BilledUnits == nil || reranked.Meta.BilledUnits.TotalTokens != 11 {
		t.Fatalf("rerank=%+v", reranked)
	}
	if embeddingCalls.Load() != 1 || rerankCalls.Load() != 1 {
		t.Fatalf("embedding calls=%d rerank calls=%d", embeddingCalls.Load(), rerankCalls.Load())
	}
}

func TestVoyageRejectsUnsupportedParametersBeforeExecution(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := NewVoyage(server.URL, "")
	oneThousandOne := make([]string, maxVoyageInputs+1)
	for index := range oneThousandOne {
		oneThousandOne[index] = "text"
	}
	zero, seven := 0, 7
	embeddings := []struct {
		param   string
		request openai.EmbeddingRequest
	}{
		{"input", openai.EmbeddingRequest{Model: "m", Input: []any{1.0}}},
		{"input", openai.EmbeddingRequest{Model: "m", Input: oneThousandOne}},
		{"metadata", openai.EmbeddingRequest{Model: "m", Input: "text", Metadata: map[string]string{"trace": "id"}}},
		{"user", openai.EmbeddingRequest{Model: "m", Input: "text", User: "provider-hint"}},
		{"input_type", openai.EmbeddingRequest{Model: "m", Input: "text", InputType: "classification"}},
		{"encoding_format", openai.EmbeddingRequest{Model: "m", Input: "text", EncodingFormat: "hex"}},
		{"output_dtype", openai.EmbeddingRequest{Model: "m", Input: "text", OutputDType: "float16"}},
		{"dimensions", openai.EmbeddingRequest{Model: "m", Input: "text", Dimensions: &zero}},
		{"dimensions", openai.EmbeddingRequest{Model: "m", Input: "text", Dimensions: &seven, OutputDType: "binary"}},
	}
	for _, test := range embeddings {
		_, err := client.Embeddings(t.Context(), test.request)
		var failure *Error
		if !errors.As(err, &failure) || failure.Provider != "voyage" || failure.Param != test.param {
			t.Fatalf("param=%s err=%v", test.param, err)
		}
	}
	reranks := []struct {
		param   string
		request openai.RerankRequest
	}{
		{"documents", openai.RerankRequest{Model: "m", Query: "q", Documents: []any{""}}},
		{"documents", openai.RerankRequest{Model: "m", Query: "q", Documents: []any{map[string]any{"text": "doc"}}}},
		{"rank_fields", openai.RerankRequest{Model: "m", Query: "q", Documents: []any{"doc"}, RankFields: []string{"text"}}},
	}
	for _, test := range reranks {
		_, err := client.Rerank(t.Context(), test.request)
		var failure *Error
		if !errors.As(err, &failure) || failure.Provider != "voyage" || failure.Param != test.param {
			t.Fatalf("param=%s err=%v", test.param, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
}

func TestVoyageRejectsMissingUsageAndUnsupportedOperations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"index":0,"embedding":[1,2]}],"model":"m","usage":{}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[{"index":0,"relevance_score":0.5}],"usage":{}}`)
	}))
	defer server.Close()
	client := NewVoyage(server.URL, "")
	if _, err := client.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "m", Input: "text"}); err == nil {
		t.Fatal("missing embedding usage accepted")
	}
	if _, err := client.Rerank(t.Context(), openai.RerankRequest{Model: "m", Query: "q", Documents: []any{"doc"}}); err == nil {
		t.Fatal("missing rerank usage accepted")
	}
	if _, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{}); err == nil {
		t.Fatal("chat accepted")
	}
	if _, err := client.Responses(t.Context(), openai.ResponseRequest{}); err == nil || client.SupportsResponses() {
		t.Fatal("Responses accepted")
	}
}

func TestVoyageRouterUsesCapabilitiesAliasesAndExactUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/v1/embeddings" {
			if request.Model != "voyage-upstream" {
				t.Errorf("embedding model=%q", request.Model)
			}
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"index":0,"embedding":[1,2]}],"model":"voyage-upstream","usage":{"total_tokens":5}}`)
			return
		}
		if request.Model != "rerank-upstream" {
			t.Errorf("rerank model=%q", request.Model)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"index":0,"relevance_score":0.7}],"usage":{"total_tokens":9}}`)
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "embedding", Type: "voyage", BaseURL: server.URL, Models: []string{"public-embed"}, ModelAliases: map[string]string{"public-embed": "voyage-upstream"}, Capabilities: []string{"embeddings"}},
		{Name: "rerank", Type: "voyage", BaseURL: server.URL, Models: []string{"public-rerank"}, ModelAliases: map[string]string{"public-rerank": "rerank-upstream"}, Capabilities: []string{"rerank"}},
	}}).(*Router)
	embeddingRequest := openai.EmbeddingRequest{Model: "public-embed", Input: "text"}
	embedding, err := router.Embeddings(t.Context(), modules.RequestContext{RequestID: "embed", EmbeddingRequest: &embeddingRequest})
	if err != nil || embedding.Model != "voyage-upstream" || embedding.Usage.TotalTokens != 5 {
		t.Fatalf("embedding=%+v err=%v", embedding, err)
	}
	returnDocuments := true
	rerankRequest := openai.RerankRequest{Model: "public-rerank", Query: "q", Documents: []any{"policy-filtered document"}, ReturnDocuments: &returnDocuments}
	rerank, err := router.Rerank(t.Context(), modules.RequestContext{RequestID: "rerank", RerankRequest: &rerankRequest})
	if err != nil || len(rerank.Results) != 1 || rerank.Results[0].Document != "policy-filtered document" || rerank.Meta.BilledUnits.TotalTokens != 9 {
		t.Fatalf("rerank=%+v err=%v", rerank, err)
	}
	if !validProviderType("voyage") {
		t.Fatal("Voyage provider type rejected")
	}
}
