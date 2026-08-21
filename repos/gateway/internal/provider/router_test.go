package provider

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type failingProvider struct{}

func (failingProvider) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, errors.New("provider is down")
}

type embeddingTestClient struct {
	err    error
	calls  int
	inputs []string
}

type affinityResponseClient struct {
	id       string
	calls    int
	previous []string
}

func (p *affinityResponseClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}
func (p *affinityResponseClient) Responses(_ context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	p.calls++
	p.previous = append(p.previous, request.PreviousResponse)
	return openai.ResponseResponse{ID: p.id, Object: "response", Model: request.Model, Status: "completed"}, nil
}

func (p *embeddingTestClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}
func (p *embeddingTestClient) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}
func (p *embeddingTestClient) Embeddings(_ context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	p.calls++
	p.inputs, _ = openai.EmbeddingInputStrings(request.Input)
	if p.err != nil {
		return openai.EmbeddingResponse{}, p.err
	}
	return openai.EmbeddingResponse{Object: "list", Model: request.Model, Data: []openai.Embedding{{Object: "embedding", Embedding: []float64{1}, Index: 0}}, Usage: openai.Usage{PromptTokens: 1, TotalTokens: 1}}, nil
}

func TestRouterEmbeddingsFailoverAndCapabilityFilter(t *testing.T) {
	unsupported := &embeddingTestClient{}
	failing := &embeddingTestClient{err: &Error{Class: FailureUnavailable, Err: errors.New("temporary")}}
	success := &embeddingTestClient{}
	router := Router{
		endpoints: []Endpoint{
			{Name: "chat-only", Type: "demo", Priority: 0, Capabilities: []string{"chat"}, Provider: unsupported},
			{Name: "embed-a", Type: "demo", Priority: 1, Capabilities: []string{"embeddings"}, Provider: failing},
			{Name: "embed-b", Type: "demo", Priority: 2, Capabilities: []string{"embeddings"}, Provider: success},
		},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.EmbeddingRequest{Model: "embed-model", Input: "hello"}
	response, err := router.Embeddings(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, EmbeddingRequest: &request})
	if err != nil {
		t.Fatal(err)
	}
	if unsupported.calls != 0 || failing.calls != 1 || success.calls != 1 {
		t.Fatalf("unexpected routing calls: unsupported=%d failing=%d success=%d", unsupported.calls, failing.calls, success.calls)
	}
	if response.Model != "embed-model" || len(response.Data) != 1 {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestRouterResponsesSessionAffinityIsTenantScoped(t *testing.T) {
	first := &affinityResponseClient{id: "resp-first"}
	second := &affinityResponseClient{id: "resp-second"}
	router := Router{
		endpoints: []Endpoint{
			{Name: "endpoint-a", Type: "demo", Provider: first},
			{Name: "endpoint-b", Type: "demo", Provider: second},
		},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		affinity: newAffinityStore(time.Hour, nil),
	}
	initial := openai.ResponseRequest{Model: "test-model", Input: "first"}
	response, err := router.Responses(context.Background(), modules.RequestContext{CredentialID: "tenant-a", Request: openai.ChatCompletionRequest{Model: initial.Model}, ResponseRequest: &initial})
	if err != nil {
		t.Fatal(err)
	}
	continued := openai.ResponseRequest{Model: "test-model", Input: "continue", PreviousResponse: response.ID}
	if _, err := router.Responses(context.Background(), modules.RequestContext{CredentialID: "tenant-a", Request: openai.ChatCompletionRequest{Model: continued.Model}, ResponseRequest: &continued}); err != nil {
		t.Fatal(err)
	}
	if first.calls != 2 || second.calls != 0 {
		t.Fatalf("session was not pinned: first=%d second=%d", first.calls, second.calls)
	}
	router.routeCounter.Store(1)
	if _, err := router.Responses(context.Background(), modules.RequestContext{CredentialID: "tenant-b", Request: openai.ChatCompletionRequest{Model: continued.Model}, ResponseRequest: &continued}); err != nil {
		t.Fatal(err)
	}
	if second.calls != 1 {
		t.Fatalf("affinity leaked across tenants: second=%d", second.calls)
	}
}

func TestRouterResponsesAffinityFailsClosedWhenEndpointUnavailable(t *testing.T) {
	affinity := newAffinityStore(time.Hour, nil)
	req := modules.RequestContext{CredentialID: "tenant-a"}
	if err := affinity.set(context.Background(), affinityKey(req, "resp-existing"), "removed-endpoint"); err != nil {
		t.Fatal(err)
	}
	client := &affinityResponseClient{id: "resp-new"}
	router := Router{
		endpoints: []Endpoint{{Name: "other-endpoint", Type: "demo", Provider: client}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, affinity: affinity,
	}
	request := openai.ResponseRequest{Model: "test-model", Input: "continue", PreviousResponse: "resp-existing"}
	req.Request = openai.ChatCompletionRequest{Model: request.Model}
	req.ResponseRequest = &request
	_, err := router.Responses(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "removed-endpoint") {
		t.Fatalf("expected explicit affinity error, got %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("continuation leaked to another endpoint: %d", client.calls)
	}
}

func TestStreamResponsesAffinityFallsBackToPinnedNonStreamingEndpoint(t *testing.T) {
	affinity := newAffinityStore(time.Hour, nil)
	req := modules.RequestContext{CredentialID: "tenant-a"}
	if err := affinity.set(context.Background(), affinityKey(req, "resp-existing"), "endpoint-a"); err != nil {
		t.Fatal(err)
	}
	client := &affinityResponseClient{id: "resp-next"}
	router := Router{
		endpoints: []Endpoint{{Name: "endpoint-a", Type: "demo", Capabilities: []string{"responses"}, Provider: client}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, affinity: affinity,
	}
	request := openai.ResponseRequest{Model: "test-model", Input: "continue", PreviousResponse: "resp-existing", Stream: true}
	req.Request = openai.ChatCompletionRequest{Model: request.Model}
	req.ResponseRequest = &request
	if _, streamed, err := router.StreamResponses(context.Background(), req, func(string, string) error { return nil }); err != nil || streamed {
		t.Fatalf("expected non-stream fallback, streamed=%v err=%v", streamed, err)
	}
	request.Stream = false
	if _, err := router.Responses(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 || client.previous[0] != "resp-existing" {
		t.Fatalf("pinned non-streaming endpoint was not used: %+v", client)
	}
}

func TestAdaptiveRoutingPrefersLowLatencyHealthyEndpoint(t *testing.T) {
	adaptive := newAdaptiveRouter(0.5)
	adaptive.observe("fast", 20*time.Millisecond, nil)
	adaptive.observe("slow", 200*time.Millisecond, nil)
	adaptive.observe("flaky", 5*time.Millisecond, errors.New("failed"))
	router := Router{
		routingStrategy: "adaptive", adaptive: adaptive, routeCounter: &atomic.Uint64{},
	}
	ordered := router.weightedOrder([]Endpoint{{Name: "slow"}, {Name: "flaky"}, {Name: "fast"}})
	if len(ordered) != 3 || ordered[0].Name != "fast" {
		t.Fatalf("unexpected adaptive order: %+v", ordered)
	}
}

func TestAdaptiveRoutingPreservesPriorityBoundary(t *testing.T) {
	adaptive := newAdaptiveRouter(0.5)
	adaptive.observe("high-priority-slow", time.Second, nil)
	adaptive.observe("low-priority-fast", time.Millisecond, nil)
	router := Router{routingStrategy: "adaptive", adaptive: adaptive, routeCounter: &atomic.Uint64{}}
	ordered := router.weightedOrder([]Endpoint{{Name: "high-priority-slow", Priority: 1}, {Name: "low-priority-fast", Priority: 2}})
	if ordered[0].Name != "high-priority-slow" {
		t.Fatalf("adaptive routing crossed priority boundary: %+v", ordered)
	}
}

func (failingProvider) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, errors.New("provider is down")
}

type staticProvider struct {
	content string
}

type countingProvider struct {
	calls     int
	err       error
	content   string
	responses int
}

type recordingProviderObserver struct {
	providerCalls int
	cacheCalls    int
	endpoint      string
	operation     string
	result        string
}

func (o *recordingProviderObserver) ObserveProvider(endpoint, _ string, operation, result string, _ time.Duration) {
	o.providerCalls++
	o.endpoint, o.operation, o.result = endpoint, operation, result
}

func (o *recordingProviderObserver) ObserveCache(_, _ string) { o.cacheCalls++ }

func (p *countingProvider) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	p.calls++
	if p.err != nil {
		return openai.ChatCompletionResponse{}, p.err
	}
	return staticProvider{content: p.content}.ChatCompletions(context.Background(), openai.ChatCompletionRequest{})
}

func (p *countingProvider) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	p.responses++
	if p.err != nil {
		return openai.ResponseResponse{}, p.err
	}
	return staticProvider{content: p.content}.Responses(context.Background(), openai.ResponseRequest{})
}

type failingPostModule struct{}

func (failingPostModule) Name() string                                          { return "post" }
func (failingPostModule) Required() bool                                        { return true }
func (failingPostModule) Handle(context.Context, *modules.RequestContext) error { return nil }
func (failingPostModule) PostResponseEnabled() bool                             { return true }
func (failingPostModule) HandlePostResponse(context.Context, *modules.RequestContext) error {
	return errors.New("storage unavailable")
}

type rejectingModule struct{}

func (rejectingModule) Name() string   { return "guardrail" }
func (rejectingModule) Required() bool { return true }
func (rejectingModule) Handle(context.Context, *modules.RequestContext) error {
	return modules.ErrContentRejected
}

type budgetRejectingModule struct{}

func (budgetRejectingModule) Name() string   { return "billing" }
func (budgetRejectingModule) Required() bool { return true }
func (budgetRejectingModule) Handle(context.Context, *modules.RequestContext) error {
	return modules.ErrBudgetExceeded
}

type failureCaptureModule struct {
	calls int
	cause error
}

func (m *failureCaptureModule) Name() string                                          { return "billing" }
func (m *failureCaptureModule) Required() bool                                        { return true }
func (m *failureCaptureModule) Handle(context.Context, *modules.RequestContext) error { return nil }
func (m *failureCaptureModule) HandleFailure(_ context.Context, _ *modules.RequestContext, cause error) error {
	m.calls++
	m.cause = cause
	return nil
}

type attemptMetadataModule struct {
	metadata map[string]string
}

func (m *attemptMetadataModule) Name() string                                          { return "telemetry" }
func (m *attemptMetadataModule) Required() bool                                        { return true }
func (m *attemptMetadataModule) Handle(context.Context, *modules.RequestContext) error { return nil }
func (m *attemptMetadataModule) PostResponseEnabled() bool                             { return true }
func (m *attemptMetadataModule) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.metadata = cloneMetadata(req.Metadata)
	return nil
}

func (p staticProvider) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{
		Model: "test-model",
		Choices: []openai.Choice{
			{Message: openai.Message{Role: "assistant", Content: p.content}},
		},
	}, nil
}

type echoProvider struct {
	seen string
}

type modelCaptureProvider struct {
	seenModel string
	content   string
}

func (p *modelCaptureProvider) ChatCompletions(_ context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	p.seenModel = request.Model
	return staticProvider{content: p.content}.ChatCompletions(context.Background(), request)
}

func (p *modelCaptureProvider) Responses(_ context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	p.seenModel = request.Model
	return staticProvider{content: p.content}.Responses(context.Background(), request)
}

type metadataModule struct {
	key  string
	seen []string
}

func (m *metadataModule) Name() string {
	return "metadata"
}

func (m *metadataModule) Required() bool {
	return true
}

func (m *metadataModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.seen = append(m.seen, req.Metadata[m.key])
	return nil
}

func (p *echoProvider) ChatCompletions(_ context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if len(request.Messages) > 0 {
		p.seen = openai.ContentText(request.Messages[len(request.Messages)-1].Content)
	}
	return openai.ChatCompletionResponse{
		Model: request.Model,
		Choices: []openai.Choice{
			{Message: openai.Message{Role: "assistant", Content: p.seen}},
		},
	}, nil
}

func (p *echoProvider) Responses(_ context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if value, ok := request.Input.(string); ok {
		p.seen = value
	}
	return openai.ResponseResponse{
		Model:      request.Model,
		OutputText: p.seen,
		Output: []openai.ResponseOutputItem{
			{Type: "message", Content: []openai.ResponseOutputContent{{Type: "output_text", Text: p.seen}}},
		},
	}, nil
}

func (p staticProvider) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{
		Model:      "test-model",
		OutputText: p.content,
		Output: []openai.ResponseOutputItem{
			{Type: "message", Content: []openai.ResponseOutputContent{{Type: "output_text", Text: p.content}}},
		},
	}, nil
}

func TestRouterFallsBackToNextEndpoint(t *testing.T) {
	router := Router{
		defaultProvider: "ollama",
		endpoints: []Endpoint{
			{
				Name:     "primary",
				Type:     "ollama",
				Models:   []string{"test-model"},
				Priority: 10,
				Provider: failingProvider{},
			},
			{
				Name:     "secondary",
				Type:     "ollama",
				Models:   []string{"test-model"},
				Priority: 20,
				Provider: staticProvider{content: "fallback ok"},
			},
		},
	}

	response, err := router.ChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{
			Provider: "ollama",
			Model:    "test-model",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "fallback ok" {
		t.Fatalf("unexpected fallback response: %s", openai.ContentText(response.Choices[0].Message.Content))
	}
}

func TestRouterRetriesUnavailableEndpointBeforeFallback(t *testing.T) {
	primary := &countingProvider{err: statusError("primary", 503)}
	secondary := &countingProvider{content: "fallback"}
	router := Router{
		defaultProvider: "auto",
		health:          newEndpointHealthTracker(),
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, MaxRetries: 2, Provider: primary},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Provider: secondary},
		},
	}

	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	if primary.calls != 3 || secondary.calls != 1 {
		t.Fatalf("expected 3 primary attempts and 1 fallback, got primary=%d secondary=%d", primary.calls, secondary.calls)
	}
}

func TestRouterObservesEveryProviderRetryAndCacheOperation(t *testing.T) {
	observer := &recordingProviderObserver{}
	primary := &countingProvider{err: statusError("primary", 503)}
	secondary := &countingProvider{content: "fallback"}
	router := Router{
		health: newEndpointHealthTracker(), observer: observer,
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, MaxRetries: 1, Provider: primary},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Provider: secondary},
		},
	}
	if _, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}}); err != nil {
		t.Fatal(err)
	}
	if observer.providerCalls != 3 || observer.endpoint != "secondary" || observer.operation != "chat" || observer.result != "ok" {
		t.Fatalf("unexpected provider observations: %+v", observer)
	}
	if observer.cacheCalls == 0 {
		t.Fatal("cache-disabled outcome was not observed")
	}
}

func TestRouterDoesNotFallbackForClientRequestError(t *testing.T) {
	primary := &countingProvider{err: statusError("primary", 400)}
	secondary := &countingProvider{content: "must not run"}
	router := Router{health: newEndpointHealthTracker(), endpoints: []Endpoint{
		{Name: "primary", Type: "openai", Models: []string{"model"}, Provider: primary},
		{Name: "secondary", Type: "openai", Models: []string{"model"}, Provider: secondary},
	}}

	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
	if err == nil {
		t.Fatal("expected client request error")
	}
	if secondary.calls != 0 {
		t.Fatalf("client error must not trigger fallback, secondary calls=%d", secondary.calls)
	}
}

func TestRouterDoesNotFallbackAfterBudgetRejection(t *testing.T) {
	primary := &countingProvider{content: "must not run"}
	secondary := &countingProvider{content: "must not run"}
	router := Router{
		modules: modules.NewPipeline([]modules.Module{budgetRejectingModule{}}),
		health:  newEndpointHealthTracker(),
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, Provider: primary},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Provider: secondary},
		},
	}
	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
	if !errors.Is(err, modules.ErrBudgetExceeded) {
		t.Fatalf("expected terminal budget error, got %v", err)
	}
	if primary.calls != 0 || secondary.calls != 0 {
		t.Fatalf("budget rejection reached providers: primary=%d secondary=%d", primary.calls, secondary.calls)
	}
}

func TestRouterRunsFailureLifecycleOnceAfterExhaustedFailover(t *testing.T) {
	hook := &failureCaptureModule{}
	router := Router{
		health:  newEndpointHealthTracker(),
		modules: modules.NewPipeline([]modules.Module{hook}),
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, Provider: &countingProvider{err: statusError("primary", 503)}},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Provider: &countingProvider{err: statusError("secondary", 503)}},
		},
	}
	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
	if err == nil || hook.calls != 1 || hook.cause == nil {
		t.Fatalf("expected one cancellation hook after final failure, err=%v calls=%d cause=%v", err, hook.calls, hook.cause)
	}
}

func TestRouterDoesNotFallbackAfterPostResponseFailure(t *testing.T) {
	primary := &countingProvider{content: "generated"}
	secondary := &countingProvider{content: "duplicate generation"}
	router := Router{
		health:  newEndpointHealthTracker(),
		modules: modules.NewPipeline([]modules.Module{failingPostModule{}}),
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, Provider: primary},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Provider: secondary},
		},
	}

	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
	if failureClass(err) != FailurePostProcessing {
		t.Fatalf("expected post-processing failure, got %v", err)
	}
	if primary.calls != 1 || secondary.calls != 0 {
		t.Fatalf("post-processing failure must not regenerate, primary=%d secondary=%d", primary.calls, secondary.calls)
	}
}

func TestRouterDoesNotFallbackAfterContentRejection(t *testing.T) {
	primary := &countingProvider{content: "must not run"}
	secondary := &countingProvider{content: "must not run"}
	router := Router{
		modules: modules.NewPipeline([]modules.Module{rejectingModule{}}),
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, Provider: primary},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Provider: secondary},
		},
	}

	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
	if !errors.Is(err, modules.ErrContentRejected) {
		t.Fatalf("expected content rejection, got %v", err)
	}
	if primary.calls != 0 || secondary.calls != 0 {
		t.Fatalf("rejected content must not reach providers, primary=%d secondary=%d", primary.calls, secondary.calls)
	}
}

func TestRouterPublishesAttemptMetadataToPostModules(t *testing.T) {
	telemetry := &attemptMetadataModule{}
	router := Router{
		health:    newEndpointHealthTracker(),
		modules:   modules.NewPipeline([]modules.Module{telemetry}),
		endpoints: []Endpoint{{Name: "primary", Type: "openai", Models: []string{"model"}, Provider: staticProvider{content: "ok"}}},
	}

	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	if telemetry.metadata["provider.status"] != "ok" || telemetry.metadata["provider.latency_ms"] == "" {
		t.Fatalf("missing attempt metadata: %+v", telemetry.metadata)
	}
}

func TestRouterExactCacheIsTenantScopedAndRunsPostModulesOnHit(t *testing.T) {
	upstream := &countingProvider{content: "cached"}
	telemetry := &attemptMetadataModule{}
	router := Router{
		cache: newExactCache(time.Minute), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		modules:   modules.NewPipeline([]modules.Module{telemetry}),
		endpoints: []Endpoint{{Name: "primary", Type: "openai", Models: []string{"model"}, Provider: upstream}},
	}
	request := func(credential string) error {
		_, err := router.ChatCompletions(context.Background(), modules.RequestContext{
			CredentialID: credential,
			Request:      openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "same prompt"}}},
		})
		return err
	}
	if err := request("tenant-a"); err != nil {
		t.Fatal(err)
	}
	if telemetry.metadata["provider.cache.status"] != "miss" {
		t.Fatalf("expected first request miss, metadata=%v", telemetry.metadata)
	}
	if err := request("tenant-a"); err != nil {
		t.Fatal(err)
	}
	if telemetry.metadata["provider.cache.status"] != "hit" || upstream.calls != 1 {
		t.Fatalf("expected tenant cache hit and post-module execution, calls=%d metadata=%v", upstream.calls, telemetry.metadata)
	}
	if err := request("tenant-b"); err != nil {
		t.Fatal(err)
	}
	if upstream.calls != 2 {
		t.Fatalf("different tenant must not share cache, calls=%d", upstream.calls)
	}
}

func TestRouterFallsBackToNextResponsesEndpoint(t *testing.T) {
	router := Router{
		defaultProvider: "ollama",
		endpoints: []Endpoint{
			{Name: "primary", Type: "ollama", Models: []string{"test-model"}, Priority: 10, Provider: failingProvider{}},
			{Name: "secondary", Type: "ollama", Models: []string{"test-model"}, Priority: 20, Provider: staticProvider{content: "responses fallback ok"}},
		},
	}

	response, err := router.Responses(context.Background(), modules.RequestContext{
		ResponseRequest: &openai.ResponseRequest{
			Provider: "ollama",
			Model:    "test-model",
			Input:    "hello",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.OutputText != "responses fallback ok" {
		t.Fatalf("unexpected fallback response: %s", response.OutputText)
	}
}

func TestRouterAppliesProviderLevelModulesForChat(t *testing.T) {
	echo := &echoProvider{}
	router := Router{
		defaultProvider: "demo",
		modules: modules.NewPipeline([]modules.Module{
			modules.NewAnonymizerModule(true, modules.RuleEmail),
		}),
		endpoints: []Endpoint{
			{Name: "echo", Type: "demo", Provider: echo},
		},
	}

	response, err := router.ChatCompletions(context.Background(), modules.RequestContext{
		UserID: "user-1",
		Roles:  []string{"developer"},
		Request: openai.ChatCompletionRequest{
			Model: "test-model",
			Messages: []openai.Message{
				{Role: "user", Content: "user@example.com"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if echo.seen != "{{EMAIL_1}}" {
		t.Fatalf("expected provider to receive anonymized input, got %q", echo.seen)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "user@example.com" {
		t.Fatalf("expected client response to be deanonymized, got %q", response.Choices[0].Message.Content)
	}
}

func TestRouterAppliesProviderLevelModulesForResponses(t *testing.T) {
	echo := &echoProvider{}
	router := Router{
		defaultProvider: "demo",
		modules: modules.NewPipeline([]modules.Module{
			modules.NewAnonymizerModule(true, modules.RuleEmail),
		}),
		endpoints: []Endpoint{
			{Name: "echo", Type: "demo", Provider: echo},
		},
	}

	response, err := router.Responses(context.Background(), modules.RequestContext{
		UserID: "user-1",
		Roles:  []string{"developer"},
		ResponseRequest: &openai.ResponseRequest{
			Model: "test-model",
			Input: "user@example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if echo.seen != "{{EMAIL_1}}" {
		t.Fatalf("expected provider to receive anonymized input, got %q", echo.seen)
	}
	if !strings.Contains(response.OutputText, "user@example.com") {
		t.Fatalf("expected client response to be deanonymized, got %q", response.OutputText)
	}
}

func TestRouterAddsProviderModuleFlagsToAttemptContext(t *testing.T) {
	dlpModule := &metadataModule{key: "provider.modules.dlp.enabled"}
	avModule := &metadataModule{key: "provider.modules.av.enabled"}
	router := New(Config{
		Default: "auto",
		Modules: modules.NewPipeline([]modules.Module{
			dlpModule,
			avModule,
		}),
		Endpoints: []config.ProviderEndpointConfig{
			{Name: "checked", Type: "demo", Models: []string{"checked-model"}, DLPEnabled: true, AVEnabled: true, Enabled: testBoolPtr(true)},
			{Name: "plain", Type: "demo", Models: []string{"plain-model"}, Enabled: testBoolPtr(true)},
		},
	})

	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{
			Model: "checked-model",
			Messages: []openai.Message{
				{Role: "user", Content: "hello"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dlpModule.seen[0] != "true" || avModule.seen[0] != "true" {
		t.Fatalf("expected enabled flags for checked provider, got dlp=%q av=%q", dlpModule.seen[0], avModule.seen[0])
	}

	_, err = router.ChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{
			Model: "plain-model",
			Messages: []openai.Message{
				{Role: "user", Content: "hello"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dlpModule.seen[1] != "false" || avModule.seen[1] != "false" {
		t.Fatalf("expected disabled flags for plain provider, got dlp=%q av=%q", dlpModule.seen[1], avModule.seen[1])
	}
}

func TestRouterResolvesNamedGuardrailPolicy(t *testing.T) {
	dlpModule := &metadataModule{key: "provider.modules.dlp.enabled"}
	avModule := &metadataModule{key: "provider.modules.av.enabled"}
	router := New(Config{
		Default: "auto",
		GuardrailPolicies: map[string]config.GuardrailPolicyConfig{
			"strict": {DLP: true, AV: true},
		},
		Modules: modules.NewPipeline([]modules.Module{dlpModule, avModule}),
		Endpoints: []config.ProviderEndpointConfig{{
			Name: "checked", Type: "demo", Models: []string{"model"}, GuardrailPolicy: "strict", Enabled: testBoolPtr(true),
		}},
	})
	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	if dlpModule.seen[0] != "true" || avModule.seen[0] != "true" {
		t.Fatalf("named guardrail policy was not resolved: dlp=%v av=%v", dlpModule.seen, avModule.seen)
	}
}

func TestRouterSkipsEndpointWithUnknownGuardrailPolicy(t *testing.T) {
	endpointModule := &metadataModule{key: "provider.endpoint.name"}
	router := New(Config{
		Default: "auto",
		Modules: modules.NewPipeline([]modules.Module{endpointModule}),
		Endpoints: []config.ProviderEndpointConfig{
			{Name: "misconfigured", Type: "demo", Models: []string{"model"}, GuardrailPolicy: "missing", Priority: 1, Enabled: testBoolPtr(true)},
			{Name: "safe-fallback", Type: "demo", Models: []string{"model"}, Priority: 2, Enabled: testBoolPtr(true)},
		},
	})
	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(endpointModule.seen) != 1 || endpointModule.seen[0] != "safe-fallback" {
		t.Fatalf("unknown policy endpoint should be skipped, got %v", endpointModule.seen)
	}
}

func TestRouterFiltersByProviderAndModel(t *testing.T) {
	router := New(Config{
		Default: "auto",
		Endpoints: []config.ProviderEndpointConfig{
			{
				Name:    "demo-a",
				Type:    "demo",
				Models:  []string{"model-a"},
				Enabled: testBoolPtr(true),
			},
		},
	})

	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{
			Provider: "demo",
			Model:    "model-b",
		},
	})
	if err == nil {
		t.Fatal("expected no provider endpoint error")
	}
}

func TestRouterDoesNotFallbackToDemoForUnsupportedDefaultProviderModel(t *testing.T) {
	router := Router{
		defaultProvider: "ollama",
		endpoints: []Endpoint{
			{Name: "ollama-local", Type: "ollama", Models: []string{"lfm2.5-thinking:1.2b"}, Provider: staticProvider{content: "ollama"}},
			{Name: "demo", Type: "demo", Models: []string{"fallback-demo-model"}, Provider: staticProvider{content: "demo"}},
		},
	}

	_, err := router.Responses(context.Background(), modules.RequestContext{
		ResponseRequest: &openai.ResponseRequest{
			Model: "lfm2.5-thinking:2b",
			Input: "test",
		},
	})
	if err == nil {
		t.Fatal("expected unsupported model error")
	}
	if !strings.Contains(err.Error(), `model="lfm2.5-thinking:2b"`) {
		t.Fatalf("expected model in error, got %v", err)
	}
}

func TestRouterRoutesByModelWhenProviderIsOmitted(t *testing.T) {
	router := Router{
		defaultProvider: "ollama",
		endpoints: []Endpoint{
			{Name: "ollama-local", Type: "ollama", Models: []string{"lfm2.5-thinking:1.2b"}, Provider: staticProvider{content: "ollama"}},
			{Name: "azure-open-ai", Type: "openai", Models: []string{"gpt-5.3"}, Provider: staticProvider{content: "azure"}},
		},
	}

	response, err := router.ChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{
			Model: "gpt-5.3",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "azure" {
		t.Fatalf("expected azure provider, got %q", response.Choices[0].Message.Content)
	}
}

func TestRouterRewritesModelGroupAliasForEndpoint(t *testing.T) {
	upstream := &modelCaptureProvider{content: "ok"}
	router := Router{routeCounter: &atomic.Uint64{}, endpoints: []Endpoint{{
		Name: "azure-a", Type: "openai", ModelAliases: map[string]string{"fast": "deployment-gpt-5-mini"}, Provider: upstream,
	}}}
	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "fast"}})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.seenModel != "deployment-gpt-5-mini" {
		t.Fatalf("expected endpoint-specific upstream model, got %q", upstream.seenModel)
	}
}

func TestRouterUsesEndpointWeightsWithinPriority(t *testing.T) {
	router := Router{routeCounter: &atomic.Uint64{}, endpoints: []Endpoint{
		{Name: "a", Type: "openai", Models: []string{"model"}, Priority: 10, Weight: 1, Provider: staticProvider{content: "a"}},
		{Name: "b", Type: "openai", Models: []string{"model"}, Priority: 10, Weight: 3, Provider: staticProvider{content: "b"}},
	}}
	counts := map[string]int{}
	for range 4 {
		response, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
		if err != nil {
			t.Fatal(err)
		}
		counts[openai.ContentText(response.Choices[0].Message.Content)]++
	}
	if counts["a"] != 1 || counts["b"] != 3 {
		t.Fatalf("unexpected weighted distribution: %v", counts)
	}
}

func TestRouterFiltersEndpointsByCapability(t *testing.T) {
	router := Router{routeCounter: &atomic.Uint64{}, endpoints: []Endpoint{
		{Name: "responses-only", Type: "openai", Models: []string{"model"}, Capabilities: []string{"responses"}, Provider: staticProvider{content: "wrong"}},
		{Name: "chat", Type: "openai", Models: []string{"model"}, Capabilities: []string{"chat"}, Provider: staticProvider{content: "chat"}},
	}}
	response, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "chat" {
		t.Fatalf("capability filter selected wrong endpoint: %+v", response)
	}
}

func TestRouterModelsReturnsConfiguredModels(t *testing.T) {
	router := Router{
		endpoints: []Endpoint{
			{Name: "ollama-local", Type: "ollama", Models: []string{"qwen3.5:9b", "lfm2.5-thinking:1.2b"}},
			{Name: "azure-open-ai", Type: "openai", Models: []string{"gpt-5.3"}},
			{Name: "demo", Type: "demo"},
		},
	}

	models := router.Models()
	if len(models) != 4 {
		t.Fatalf("expected 4 models, got %d: %+v", len(models), models)
	}
	expected := []string{"demo", "gpt-5.3", "lfm2.5-thinking:1.2b", "qwen3.5:9b"}
	for index, model := range models {
		if model.ID != expected[index] {
			t.Fatalf("expected model %q at index %d, got %q", expected[index], index, model.ID)
		}
		if model.Object != "model" {
			t.Fatalf("expected model object, got %q", model.Object)
		}
	}
}

func TestCatalogRoutesByModelCapabilitiesAndOutputLimit(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{
		"version":"v1","unknown_model_policy":"deny","models":[
			{"provider":"basic","model":"model","capabilities":["chat"],"max_output_tokens":100},
			{"provider":"tools","model":"model","capabilities":["chat","tools","structured_output"],"max_output_tokens":10},
			{"provider":"large","model":"model","capabilities":["chat","tools","structured_output"],"max_output_tokens":1000}
		]}`)
	if err != nil {
		t.Fatal(err)
	}
	basic := &countingProvider{content: "basic"}
	tools := &countingProvider{content: "tools"}
	large := &countingProvider{content: "large"}
	router := Router{catalog: catalog, health: newEndpointHealthTracker(), endpoints: []Endpoint{
		{Name: "basic", Type: "openai", Models: []string{"model"}, Priority: 1, Provider: basic},
		{Name: "tools", Type: "openai", Models: []string{"model"}, Priority: 2, Provider: tools},
		{Name: "large", Type: "openai", Models: []string{"model"}, Priority: 3, Provider: large},
	}}
	maxTokens := 50
	request := openai.ChatCompletionRequest{
		Model: "model", MaxTokens: &maxTokens,
		Tools:          []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup"}}},
		ResponseFormat: &openai.ResponseFormat{Type: "json_object"},
	}
	response, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: request})
	if err != nil {
		t.Fatal(err)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "large" || basic.calls != 0 || tools.calls != 0 || large.calls != 1 {
		t.Fatalf("catalog did not select capable endpoint: response=%+v calls=%d/%d/%d", response, basic.calls, tools.calls, large.calls)
	}
}

func TestCatalogStrictModeRejectsUnknownModelAndFiltersModels(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","unknown_model_policy":"deny","models":[{"provider":"known","model":"known-model","capabilities":["chat"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	router := Router{catalog: catalog, health: newEndpointHealthTracker(), endpoints: []Endpoint{
		{Name: "known", Type: "openai", Models: []string{"known-model", "unknown-model"}, Provider: staticProvider{content: "ok"}},
	}}
	if _, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "unknown-model"}}); err == nil {
		t.Fatal("strict catalog accepted an unknown model")
	}
	models := router.Models()
	if len(models) != 1 || models[0].ID != "known-model" {
		t.Fatalf("strict catalog exposed unknown models: %+v", models)
	}
}

func TestCatalogMatchesUpstreamModelAlias(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","unknown_model_policy":"deny","models":[{"provider":"azure","model":"deployment-model","capabilities":["chat","tools"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &modelCaptureProvider{content: "ok"}
	router := Router{catalog: catalog, health: newEndpointHealthTracker(), endpoints: []Endpoint{{
		Name: "azure", Type: "openai", ModelAliases: map[string]string{"public-model": "deployment-model"}, Provider: upstream,
	}}}
	_, err = router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{
		Model: "public-model", Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup"}}},
	}})
	if err != nil || upstream.seenModel != "deployment-model" {
		t.Fatalf("catalog alias lookup failed: model=%q err=%v", upstream.seenModel, err)
	}
}

func TestResponsesCatalogRequirementsIncludeToolsStructuredOutputAndStream(t *testing.T) {
	required := requiredResponseCapabilities(openai.ResponseRequest{
		Tools: []openai.ResponseTool{{Type: "function", Name: "lookup"}},
		Text:  map[string]any{"format": map[string]any{"type": "json_object"}},
	}, true)
	if strings.Join(required, ",") != "responses,stream,tools,structured_output" {
		t.Fatalf("unexpected Responses capabilities: %v", required)
	}
}

func testBoolPtr(value bool) *bool {
	return &value
}
