package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type semanticTestEmbedder struct {
	calls int
	seen  []string
	err   error
}

func (e *semanticTestEmbedder) embed(_ context.Context, text string) ([]float64, error) {
	e.calls++
	e.seen = append(e.seen, text)
	if e.err != nil {
		return nil, e.err
	}
	if strings.Contains(text, "unrelated") {
		return []float64{0, 1}, nil
	}
	if strings.Contains(text, "similar") {
		return []float64{0.99, 0.01}, nil
	}
	return []float64{1, 0}, nil
}

type semanticTestClient struct{ calls int }

func (p *semanticTestClient) ChatCompletions(_ context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	p.calls++
	return openai.ChatCompletionResponse{
		ID: "chat-semantic", Model: request.Model,
		Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "answer-" + openai.ContentText(request.Messages[len(request.Messages)-1].Content)}}},
		Usage:   openai.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}, nil
}
func (p *semanticTestClient) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}

func TestSemanticCacheHitsOnlyWithinCredentialScope(t *testing.T) {
	embedder := &semanticTestEmbedder{}
	client := &semanticTestClient{}
	metadata := &attemptMetadataModule{}
	router := Router{
		endpoints: []Endpoint{{Name: "endpoint-a", Type: "demo", Provider: client}},
		modules:   modules.NewPipeline([]modules.Module{metadata}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		semantic: newSemanticResponseCache(semanticCacheConfig{ttl: time.Hour, threshold: 0.95, maxEntries: 10, maxBytes: 4096, embedder: embedder}),
	}
	first := semanticChatContext("credential-a", "original question")
	firstResponse, err := router.ChatCompletions(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := semanticChatContext("credential-a", "similar question")
	secondResponse, err := router.ChatCompletions(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 || openai.ContentText(secondResponse.Choices[0].Message.Content) != openai.ContentText(firstResponse.Choices[0].Message.Content) {
		t.Fatalf("expected semantic hit: calls=%d first=%+v second=%+v", client.calls, firstResponse, secondResponse)
	}
	if secondResponse.Usage.TotalTokens != 0 || metadata.metadata["provider.cache.status"] != "hit" || metadata.metadata["provider.cache.kind"] != "semantic" {
		t.Fatalf("semantic hit was not billed/marked as cache: response=%+v metadata=%+v", secondResponse, metadata.metadata)
	}
	third := semanticChatContext("credential-b", "similar question")
	sharedCredentialOtherUser := semanticChatContext("credential-a", "similar question")
	sharedCredentialOtherUser.UserID = "user-other"
	if _, err := router.ChatCompletions(context.Background(), sharedCredentialOtherUser); err != nil {
		t.Fatal(err)
	}
	if client.calls != 2 {
		t.Fatalf("semantic cache leaked across users sharing a credential: calls=%d", client.calls)
	}
	if _, err := router.ChatCompletions(context.Background(), third); err != nil {
		t.Fatal(err)
	}
	if client.calls != 3 {
		t.Fatalf("semantic cache leaked across credentials: calls=%d", client.calls)
	}
}

func TestSemanticCacheRunsAfterAnonymizationAndBypassesTools(t *testing.T) {
	embedder := &semanticTestEmbedder{}
	client := &semanticTestClient{}
	router := Router{
		endpoints: []Endpoint{{Name: "endpoint-a", Type: "demo", Provider: client}},
		modules:   modules.NewPipeline([]modules.Module{modules.NewAnonymizerModule(true, modules.RuleEmail)}),
		health:    newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		semantic: newSemanticResponseCache(semanticCacheConfig{ttl: time.Hour, threshold: 0.95, embedder: embedder}),
	}
	request := semanticChatContext("credential-a", "contact user@example.com")
	if _, err := router.ChatCompletions(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(embedder.seen) != 1 || strings.Contains(embedder.seen[0], "user@example.com") || !strings.Contains(embedder.seen[0], "{{EMAIL_1}}") {
		t.Fatalf("embedder received non-anonymized prompt: %v", embedder.seen)
	}
	toolRequest := semanticChatContext("credential-a", "use a tool")
	toolRequest.Request.Tools = []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup"}}}
	if _, err := router.ChatCompletions(context.Background(), toolRequest); err != nil {
		t.Fatal(err)
	}
	if embedder.calls != 1 || client.calls != 2 {
		t.Fatalf("tool request was semantically cached: embed=%d provider=%d", embedder.calls, client.calls)
	}
	historyRequest := semanticChatContext("credential-a", "follow up")
	historyRequest.Request.Messages = append([]openai.Message{{Role: "assistant", Content: "prior answer"}}, historyRequest.Request.Messages...)
	if _, err := router.ChatCompletions(context.Background(), historyRequest); err != nil {
		t.Fatal(err)
	}
	if embedder.calls != 1 || client.calls != 3 {
		t.Fatalf("assistant history was semantically cached: embed=%d provider=%d", embedder.calls, client.calls)
	}
}

func TestSemanticCacheRequiresExactSystemContext(t *testing.T) {
	embedder := &semanticTestEmbedder{}
	client := &semanticTestClient{}
	router := Router{
		endpoints: []Endpoint{{Name: "endpoint-a", Type: "demo", Provider: client}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		semantic: newSemanticResponseCache(semanticCacheConfig{ttl: time.Hour, threshold: 0.95, embedder: embedder}),
	}
	first := semanticChatContext("credential-a", "original question")
	first.Request.Messages = append([]openai.Message{{Role: "system", Content: "policy A"}}, first.Request.Messages...)
	second := semanticChatContext("credential-a", "similar question")
	second.Request.Messages = append([]openai.Message{{Role: "system", Content: "policy B"}}, second.Request.Messages...)
	if _, err := router.ChatCompletions(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := router.ChatCompletions(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if client.calls != 2 {
		t.Fatalf("semantic cache crossed system context: provider calls=%d", client.calls)
	}
}

func TestSemanticCacheEmbedderFailureFailsOpen(t *testing.T) {
	embedder := &semanticTestEmbedder{err: errors.New("embedding unavailable")}
	client := &semanticTestClient{}
	router := Router{
		endpoints: []Endpoint{{Name: "endpoint-a", Type: "demo", Provider: client}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		semantic: newSemanticResponseCache(semanticCacheConfig{ttl: time.Hour, embedder: embedder}),
	}
	if _, err := router.ChatCompletions(context.Background(), semanticChatContext("credential-a", "question")); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("embedder outage blocked provider: calls=%d", client.calls)
	}
}

func TestSemanticCacheThresholdExpiryAndEntryLimit(t *testing.T) {
	cache := newSemanticResponseCache(semanticCacheConfig{ttl: time.Minute, threshold: 0.99, maxEntries: 1, maxBytes: 16, embedder: &semanticTestEmbedder{}})
	now := time.Unix(1_700_000_000, 0)
	cache.now = func() time.Time { return now }
	cache.set("scope", []float64{1, 0}, []byte("first"))
	if _, found := cache.lookup("scope", []float64{0.8, 0.2}); found {
		t.Fatal("below-threshold vector produced a hit")
	}
	cache.set("other-scope", []float64{0, 1}, []byte("second"))
	if _, found := cache.lookup("scope", []float64{1, 0}); found {
		t.Fatal("global entry limit did not evict oldest value")
	}
	now = now.Add(time.Minute)
	if _, found := cache.lookup("other-scope", []float64{0, 1}); found {
		t.Fatal("expired semantic entry produced a hit")
	}
	cache.set("scope", []float64{1, 0}, []byte("payload larger than configured limit"))
	if _, found := cache.lookup("scope", []float64{1, 0}); found {
		t.Fatal("oversized semantic payload was cached")
	}
}

func TestSemanticEmbedderUsesOnlyDedicatedProviderCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer embedding-service-key" {
			t.Fatalf("unexpected credential: %q", r.Header.Get("Authorization"))
		}
		var request openAICompatibleEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "embedding-model" || request.Input != "safe anonymized text" {
			t.Fatalf("unexpected embedding request: %+v", request)
		}
		_ = json.NewEncoder(w).Encode(openai.EmbeddingResponse{Object: "list", Data: []openai.Embedding{{Object: "embedding", Embedding: []float64{1, 0}, Index: 0}}})
	}))
	defer server.Close()
	embedder := newOpenAIEmbedder(server.URL, "embedding-service-key", "embedding-model")
	vector, err := embedder.embed(context.Background(), "safe anonymized text")
	if err != nil || len(vector) != 2 {
		t.Fatalf("unexpected embedding result: vector=%v err=%v", vector, err)
	}
}

func semanticChatContext(credential, prompt string) modules.RequestContext {
	return modules.RequestContext{
		CredentialID: credential, UserID: "user-default",
		Request: openai.ChatCompletionRequest{Model: "test-model", Messages: []openai.Message{{Role: "user", Content: prompt}}},
	}
}
