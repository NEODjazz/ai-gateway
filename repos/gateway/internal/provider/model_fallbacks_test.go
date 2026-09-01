package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type fallbackTestClient struct {
	err       error
	content   string
	calls     int
	seenModel string
}

func (p *fallbackTestClient) ChatCompletions(_ context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	p.calls++
	p.seenModel = request.Model
	if p.err != nil {
		return openai.ChatCompletionResponse{}, p.err
	}
	return staticProvider{content: p.content}.ChatCompletions(context.Background(), request)
}

func (p *fallbackTestClient) Responses(_ context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	p.calls++
	p.seenModel = request.Model
	if p.err != nil {
		return openai.ResponseResponse{}, p.err
	}
	return staticProvider{content: p.content}.Responses(context.Background(), request)
}

func fallbackTestRouter(primary, general, contextWindow, contentPolicy Client) Router {
	groups := map[string]ModelGroup{
		"primary": {
			ID: "primary", DeploymentIDs: []string{"primary-deployment"}, Strategy: "weighted", Enabled: true,
			Fallbacks: map[string][]string{
				FallbackGeneral:       {"general"},
				FallbackContextWindow: {"context"},
				FallbackContentPolicy: {"content"},
			},
		},
		"general": {ID: "general", DeploymentIDs: []string{"general-deployment"}, Strategy: "weighted", Enabled: true},
		"context": {ID: "context", DeploymentIDs: []string{"context-deployment"}, Strategy: "weighted", Enabled: true},
		"content": {ID: "content", DeploymentIDs: []string{"content-deployment"}, Strategy: "weighted", Enabled: true},
	}
	registry := &modelGroupRegistry{}
	registry.current.Store(&groups)
	return Router{
		endpoints: []Endpoint{
			{Name: "primary-deployment", Type: "test", Models: []string{"primary"}, ModelAliases: map[string]string{"primary": "upstream-primary"}, Provider: primary},
			{Name: "general-deployment", Type: "test", Models: []string{"general"}, ModelAliases: map[string]string{"general": "upstream-general"}, Provider: general},
			{Name: "context-deployment", Type: "test", Models: []string{"context"}, ModelAliases: map[string]string{"context": "upstream-context"}, Provider: contextWindow},
			{Name: "content-deployment", Type: "test", Models: []string{"content"}, ModelAliases: map[string]string{"content": "upstream-content"}, Provider: contentPolicy},
		},
		modelGroups: registry, modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
}

func TestModelGroupFallbackSelectsChainByFailureClass(t *testing.T) {
	tests := []struct {
		name          string
		failure       FailureClass
		expectedModel string
		expectedType  string
	}{
		{name: "general", failure: FailureUnavailable, expectedModel: "upstream-general", expectedType: FallbackGeneral},
		{name: "context window", failure: FailureContextLength, expectedModel: "upstream-context", expectedType: FallbackContextWindow},
		{name: "content policy", failure: FailureContentPolicy, expectedModel: "upstream-content", expectedType: FallbackContentPolicy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := &fallbackTestClient{err: &Error{Class: test.failure, Provider: "primary", Err: errors.New("failed")}}
			general := &fallbackTestClient{content: "general"}
			contextWindow := &fallbackTestClient{content: "context"}
			contentPolicy := &fallbackTestClient{content: "content"}
			telemetry := &attemptMetadataModule{}
			router := fallbackTestRouter(primary, general, contextWindow, contentPolicy)
			router.modules = modules.NewPipeline([]modules.Module{telemetry})

			response, err := router.ChatCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "primary"}})
			if err != nil {
				t.Fatal(err)
			}
			selected := map[string]*fallbackTestClient{"upstream-general": general, "upstream-context": contextWindow, "upstream-content": contentPolicy}[test.expectedModel]
			if primary.calls != 1 || selected.calls != 1 || selected.seenModel != test.expectedModel || openai.ContentText(response.Choices[0].Message.Content) == "" {
				t.Fatalf("wrong fallback route: primary=%d selected=%+v response=%+v", primary.calls, selected, response)
			}
			if telemetry.metadata["provider.original_model"] != "primary" || telemetry.metadata["provider.routed_model"] == "" || telemetry.metadata["provider.fallback_type"] != test.expectedType || telemetry.metadata["provider.fallback_count"] != "1" {
				t.Fatalf("missing fallback metadata: %+v", telemetry.metadata)
			}
		})
	}
}

func TestModelGroupFallbackHonorsAuthorizedTargetsAndTerminalErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure FailureClass
		allowed []string
	}{
		{name: "unauthorized target", failure: FailureUnavailable},
		{name: "client request", failure: FailureClientRequest, allowed: []string{"general", "context", "content"}},
		{name: "provider authentication", failure: FailureAuthentication, allowed: []string{"general", "context", "content"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			primary := &fallbackTestClient{err: &Error{Class: test.failure, Provider: "primary", Err: errors.New("failed")}}
			general := &fallbackTestClient{content: "must not run"}
			contextWindow := &fallbackTestClient{content: "must not run"}
			contentPolicy := &fallbackTestClient{content: "must not run"}
			router := fallbackTestRouter(primary, general, contextWindow, contentPolicy)
			_, err := router.ChatCompletions(t.Context(), modules.RequestContext{
				Request: openai.ChatCompletionRequest{Model: "primary"}, FallbackPolicyEvaluated: true, AllowedFallbackModels: test.allowed,
			})
			if err == nil {
				t.Fatal("expected terminal routing error")
			}
			if general.calls != 0 || contextWindow.calls != 0 || contentPolicy.calls != 0 {
				t.Fatalf("terminal or unauthorized fallback ran: general=%d context=%d content=%d", general.calls, contextWindow.calls, contentPolicy.calls)
			}
		})
	}
}

func TestModelGroupFallbackRunsWhenPrimaryHasNoEligibleDeployment(t *testing.T) {
	general := &fallbackTestClient{content: "available"}
	router := fallbackTestRouter(&fallbackTestClient{}, general, &fallbackTestClient{}, &fallbackTestClient{})
	router.endpoints = router.endpoints[1:]
	response, err := router.ChatCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "primary"}})
	if err != nil {
		t.Fatal(err)
	}
	if general.calls != 1 || openai.ContentText(response.Choices[0].Message.Content) != "available" {
		t.Fatalf("unavailable primary did not activate general fallback: calls=%d response=%+v", general.calls, response)
	}
}

func TestModelGroupFallbackPlanningDoesNotAdvancePrimaryRouteCounter(t *testing.T) {
	router := fallbackTestRouter(&fallbackTestClient{}, &fallbackTestClient{}, &fallbackTestClient{}, &fallbackTestClient{})
	router.endpoints = append(router.endpoints, Endpoint{
		Name: "general-deployment-secondary", Type: "test", Models: []string{"general"}, Weight: 1, Provider: &fallbackTestClient{},
	})

	before := router.routeCounter.Load()
	candidates := router.routeCandidates(t.Context(), modules.RequestContext{RequestID: "request-1"}, openai.ChatCompletionRequest{Model: "primary"}, "chat")
	if len(candidates) < 3 {
		t.Fatalf("expected primary and fallback candidates, got %d", len(candidates))
	}
	if after := router.routeCounter.Load(); after != before {
		t.Fatalf("planning unused fallbacks advanced the primary route counter: before=%d after=%d", before, after)
	}
}

func TestStreamingModelGroupFallbackOnlyBeforeFirstChunk(t *testing.T) {
	primary := &scriptedStreamingProvider{failChatBeforeWrite: 1}
	general := &scriptedStreamingProvider{}
	router := fallbackTestRouter(primary, general, &scriptedStreamingProvider{}, &scriptedStreamingProvider{})
	for index := range router.endpoints {
		router.endpoints[index].Capabilities = []string{"chat", "stream"}
	}
	writes := 0
	_, streamed, err := router.StreamChatCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "primary", Stream: true}}, func(string) error {
		writes++
		return nil
	})
	if err != nil || !streamed || primary.chatCalls != 1 || general.chatCalls != 1 || writes != 1 {
		t.Fatalf("stream fallback failed: streamed=%v err=%v primary=%d general=%d writes=%d", streamed, err, primary.chatCalls, general.chatCalls, writes)
	}
}

func TestModelGroupFallbackCoversResponsesEmbeddingsAndRerank(t *testing.T) {
	t.Run("responses", func(t *testing.T) {
		primary := &fallbackTestClient{err: &Error{Class: FailureUnavailable, Provider: "primary", Err: errors.New("failed")}}
		general := &fallbackTestClient{content: "response fallback"}
		router := fallbackTestRouter(primary, general, &fallbackTestClient{}, &fallbackTestClient{})
		request := openai.ResponseRequest{Model: "primary", Input: "hello"}
		response, err := router.Responses(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "primary"}, ResponseRequest: &request})
		if err != nil || general.calls != 1 || general.seenModel != "upstream-general" || response.OutputText != "response fallback" {
			t.Fatalf("responses fallback failed: response=%+v calls=%d model=%q err=%v", response, general.calls, general.seenModel, err)
		}
	})

	t.Run("embeddings", func(t *testing.T) {
		primary := &embeddingTestClient{err: &Error{Class: FailureUnavailable, Provider: "primary", Err: errors.New("failed")}}
		general := &embeddingTestClient{}
		router := fallbackTestRouter(primary, general, &embeddingTestClient{}, &embeddingTestClient{})
		for index := range router.endpoints {
			router.endpoints[index].Capabilities = []string{"embeddings"}
		}
		request := openai.EmbeddingRequest{Model: "primary", Input: "hello"}
		response, err := router.Embeddings(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "primary"}, EmbeddingRequest: &request})
		if err != nil || general.calls != 1 || general.model != "upstream-general" || response.Model != "upstream-general" {
			t.Fatalf("embedding fallback failed: response=%+v calls=%d model=%q err=%v", response, general.calls, general.model, err)
		}
	})

	t.Run("rerank", func(t *testing.T) {
		primary := &rerankTestClient{err: &Error{Class: FailureUnavailable, Provider: "primary", Err: errors.New("failed")}}
		general := &rerankTestClient{}
		router := fallbackTestRouter(primary, general, &rerankTestClient{}, &rerankTestClient{})
		for index := range router.endpoints {
			router.endpoints[index].Capabilities = []string{"rerank"}
			router.endpoints[index].Admission = newAdmissionController(0, 0, 0)
		}
		request := openai.RerankRequest{Model: "primary", Query: "query", Documents: []any{"first", "second"}}
		response, err := router.Rerank(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "primary"}, RerankRequest: &request})
		if err != nil || general.calls != 1 || general.model != "upstream-general" || len(response.Results) != 1 {
			t.Fatalf("rerank fallback failed: response=%+v calls=%d model=%q err=%v", response, general.calls, general.model, err)
		}
	})
}

func TestStreamingResponsesModelGroupFallbackOnlyBeforeFirstEvent(t *testing.T) {
	primary := &scriptedStreamingProvider{failRespBeforeWrite: 1}
	general := &scriptedStreamingProvider{}
	router := fallbackTestRouter(primary, general, &scriptedStreamingProvider{}, &scriptedStreamingProvider{})
	for index := range router.endpoints {
		router.endpoints[index].Capabilities = []string{"responses", "stream"}
	}
	request := openai.ResponseRequest{Model: "primary", Stream: true, Input: "hello"}
	writes := 0
	_, streamed, err := router.StreamResponses(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "primary"}, ResponseRequest: &request}, func(string, string) error {
		writes++
		return nil
	})
	if err != nil || !streamed || primary.responseCalls != 1 || general.responseCalls != 1 || writes != 1 {
		t.Fatalf("response stream fallback failed: streamed=%v err=%v primary=%d general=%d writes=%d", streamed, err, primary.responseCalls, general.responseCalls, writes)
	}
}

func TestRoutingSimulationExplainsTypedModelFallback(t *testing.T) {
	router := fallbackTestRouter(&fallbackTestClient{}, &fallbackTestClient{}, &fallbackTestClient{}, &fallbackTestClient{})
	result := router.SimulateRouting(t.Context(), RoutingSimulationRequest{Model: "primary", FailureClass: string(FailureContextLength)})
	if result.Selected != "context-deployment" || result.FallbackType != FallbackContextWindow || result.FailureClass != string(FailureContextLength) {
		t.Fatalf("unexpected fallback simulation: %+v", result)
	}
	found := false
	for _, candidate := range result.Candidates {
		if candidate.DeploymentID == "context-deployment" {
			found = candidate.Model == "context" && candidate.Stage > 0 && candidate.FallbackType == FallbackContextWindow
		}
	}
	if !found {
		t.Fatalf("simulation omitted fallback stage metadata: %+v", result.Candidates)
	}
}

func TestResponsesAffinityKeepsFallbackEndpointPinned(t *testing.T) {
	primary := &fallbackTestClient{err: &Error{Class: FailureUnavailable, Provider: "primary", Err: errors.New("failed")}}
	general := &affinityResponseClient{id: "resp-fallback"}
	router := fallbackTestRouter(primary, general, &fallbackTestClient{}, &fallbackTestClient{})
	router.affinity = newAffinityStore(time.Hour, nil)
	req := modules.RequestContext{CredentialID: "tenant-a", Request: openai.ChatCompletionRequest{Model: "primary"}}
	initial := openai.ResponseRequest{Model: "primary", Input: "first"}
	req.ResponseRequest = &initial
	response, err := router.Responses(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	continued := openai.ResponseRequest{Model: "primary", Input: "continue", PreviousResponse: response.ID}
	req.ResponseRequest = &continued
	if _, err := router.Responses(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 1 || general.calls != 2 || general.previous[1] != "resp-fallback" {
		t.Fatalf("fallback affinity was not preserved: primary=%d general=%+v", primary.calls, general)
	}
}
