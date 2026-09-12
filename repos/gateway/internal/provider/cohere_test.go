package provider

import (
	"context"
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

func TestCohereRerankV2ThroughRouter(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/proxy/v2/rerank" || r.Header.Get("Authorization") != "Bearer provider-key" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != "rerank-upstream" || request["query"] != "capital" || request["top_n"] != float64(1) || request["max_tokens_per_doc"] != float64(2048) {
			t.Fatalf("request fields lost: %+v", request)
		}
		if _, found := request["return_documents"]; found {
			t.Fatal("gateway-only return_documents leaked upstream")
		}
		_, _ = fmt.Fprint(w, `{"id":"rank-1","results":[{"index":1,"relevance_score":0.9}],"meta":{"api_version":{"version":"2"},"billed_units":{"search_units":1.5}}}`)
	}))
	defer server.Close()

	topN, maxTokens, returnDocuments := 1, 2048, true
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "cohere-native", Type: "cohere", BaseURL: server.URL + "/proxy/v1", APIKey: "provider-key",
		Models: []string{"rerank-public"}, ModelAliases: map[string]string{"rerank-public": "rerank-upstream"}, Capabilities: []string{"rerank"},
	}}}).(*Router)
	response, err := router.Rerank(context.Background(), modules.RequestContext{RerankRequest: &openai.RerankRequest{
		Model: "rerank-public", Query: "capital", Documents: []any{"one", "two"}, TopN: &topN, MaxTokensPerDoc: &maxTokens, ReturnDocuments: &returnDocuments,
	}, Request: openai.ChatCompletionRequest{Model: "rerank-public"}})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || response.ID != "rank-1" || len(response.Results) != 1 || response.Results[0].Document != "two" || response.Meta == nil || response.Meta.BilledUnits == nil || response.Meta.BilledUnits.SearchUnits != 1.5 {
		t.Fatalf("response=%+v calls=%d", response, calls.Load())
	}
}

func TestCohereRejectsUnsupportedRerankParametersBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	maxChunks := 1
	for _, test := range []struct {
		name, param string
		request     openai.RerankRequest
	}{
		{name: "objects", param: "documents", request: openai.RerankRequest{Documents: []any{map[string]any{"text": "one"}}}},
		{name: "rank fields", param: "rank_fields", request: openai.RerankRequest{Documents: []any{"one"}, RankFields: []string{"text"}}},
		{name: "chunks", param: "max_chunks_per_doc", request: openai.RerankRequest{Documents: []any{"one"}, MaxChunksPerDoc: &maxChunks}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewCohere(server.URL, "key").Rerank(context.Background(), test.request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Param != test.param || failure.UpstreamCode != "unsupported_parameter" {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported request reached upstream")
	}
}

func TestCohereRejectsWebFetchOptions(t *testing.T) {
	request := openai.ChatCompletionRequest{
		Model: "command", Messages: []openai.Message{{Role: "user", Content: "hello"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{WebFetchOptions: &openai.ChatWebFetchOptions{AllowedDomains: []string{"example.com"}, MaxContentTokens: 1000}},
	}
	var failure *Error
	if err := NewCohere("http://unused.invalid", "").ValidateChatParameters(request); !errors.As(err, &failure) || failure.Param != "web_fetch_options" || failure.UpstreamCode != "unsupported_parameter" {
		t.Fatalf("web_fetch_options was not rejected explicitly: %v", err)
	}
}

func TestCohereRerankRejectsInvalidTransportAndUsage(t *testing.T) {
	if _, err := cohereEndpoint("https://user:secret@example.test", "v2/rerank"); err == nil {
		t.Fatal("credential-bearing base URL accepted")
	}
	if err := validateRerankResponse(openai.RerankResponse{Meta: &openai.RerankResponseMeta{BilledUnits: &openai.RerankBilledUnits{SearchUnits: -1}}}, 1); err == nil {
		t.Fatal("negative billed units accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"results":[]} {}`)
	}))
	defer server.Close()
	_, err := NewCohere(server.URL, "").Rerank(context.Background(), openai.RerankRequest{Documents: []any{"one"}})
	if err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("trailing response accepted: %v", err)
	}
}
