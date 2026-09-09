package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestOpenAICompatibleSearchContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/search" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != nil || payload["provider"] != nil || payload["search_tool_name"] != nil || payload["query"] != "gateway" || payload["max_results"] != float64(5) || payload["country"] != "US" {
			t.Fatalf("payload=%v", payload)
		}
		_, _ = io.WriteString(w, `{"object":"search","results":[{"title":"Gateway","url":"https://example.test/gateway","snippet":"result","date":"2026-09-09"}]}`)
	}))
	defer server.Close()
	maximum := 5
	request := openai.SearchRequest{Provider: "search", Model: "public-search", Query: "gateway", MaxResults: &maximum, Country: "US"}
	response, err := NewOpenAICompatible(server.URL+"/v1", "secret", false).Search(t.Context(), request)
	if err != nil || response.Object != "search" || len(response.Results) != 1 || response.Usage.SearchRequests != 1 || response.Model != "public-search" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestRouterSearchRequiresCapabilityAndAppliesAlias(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Query []string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		queries = payload.Query
		_, _ = io.WriteString(w, `{"object":"search","results":[]}`)
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "search", Type: "openai-compatible", BaseURL: server.URL, Models: []string{"public-search"}, ModelAliases: map[string]string{"public-search": "upstream-search"}, Capabilities: []string{"search"}}}}).(*Router)
	request := openai.SearchRequest{SearchToolName: "public-search", Query: []string{"one", "two"}}
	response, err := router.Search(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public-search", Messages: []openai.Message{{Role: "user", Content: "one"}, {Role: "user", Content: "two"}}}, SearchRequest: &request})
	if err != nil || len(queries) != 2 || response.Model != "upstream-search" || response.Usage.SearchRequests != 2 {
		t.Fatalf("response=%+v queries=%v err=%v", response, queries, err)
	}
	missing := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "search", Type: "openai-compatible", BaseURL: server.URL, Models: []string{"public-search"}, Capabilities: []string{"chat"}}}}).(*Router)
	if _, err := missing.Search(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public-search"}, SearchRequest: &request}); err == nil {
		t.Fatal("endpoint without search capability was selected")
	}
}

type searchLimitReader struct{ read int }

func (r *searchLimitReader) Read(buffer []byte) (int, error) {
	remaining := maxSearchResponseBytes + 1 - r.read
	if remaining <= 0 {
		return 0, io.EOF
	}
	if len(buffer) > remaining {
		buffer = buffer[:remaining]
	}
	for index := range buffer {
		buffer[index] = 'x'
	}
	r.read += len(buffer)
	return len(buffer), nil
}

func TestSearchRejectsMalformedAndOversizedResponses(t *testing.T) {
	invalid := []string{
		`{"object":"list","results":[]}`,
		`{"object":"search"}`,
		`{"object":"search","results":[{"url":"file:///tmp/result"}]}`,
		`{"object":"search","results":[{"url":"https://user@example.test/result"}]}`,
		`{"object":"search","results":[]} {}`,
	}
	for _, body := range invalid {
		if _, err := decodeSearchResponse(strings.NewReader(body)); err == nil {
			t.Fatalf("accepted response %s", body)
		}
	}
	reader := &searchLimitReader{}
	if _, err := decodeSearchResponse(reader); err == nil || reader.read != maxSearchResponseBytes+1 {
		t.Fatalf("oversized response accepted: bytes=%d err=%v", reader.read, err)
	}
}
