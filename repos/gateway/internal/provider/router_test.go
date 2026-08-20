package provider

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type failingProvider struct{}

func (failingProvider) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, errors.New("provider is down")
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

func testBoolPtr(value bool) *bool {
	return &value
}
