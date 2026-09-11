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
	model  string
}

type rerankTestClient struct {
	calls int
	err   error
	model string
}

type moderationTestClient struct {
	calls int
	model string
}

func (p *moderationTestClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}
func (p *moderationTestClient) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}
func (p *moderationTestClient) Moderations(_ context.Context, request openai.ModerationRequest) (openai.ModerationResponse, error) {
	p.calls++
	p.model = request.Model
	value := false
	return openai.ModerationResponse{ID: "modr-1", Model: request.Model, Results: []openai.ModerationResult{{Categories: map[string]*bool{"violence": &value}, CategoryScores: map[string]float64{"violence": .1}, CategoryAppliedInputTypes: map[string][]string{"violence": {"text"}}}}}, nil
}

func (p *rerankTestClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}
func (p *rerankTestClient) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}
func (p *rerankTestClient) Rerank(_ context.Context, request openai.RerankRequest) (openai.RerankResponse, error) {
	p.calls++
	p.model = request.Model
	if p.err != nil {
		return openai.RerankResponse{}, p.err
	}
	return openai.RerankResponse{Results: []openai.RerankResult{{Index: 1, RelevanceScore: 0.8, Document: "anonymized"}}}, nil
}

type affinityResponseClient struct {
	id       string
	calls    int
	previous []string
}

type splitStreamingProvider struct{}

type scriptedStreamingProvider struct {
	chatCalls           int
	responseCalls       int
	failChatBeforeWrite int
	failRespBeforeWrite int
	failChatAfterWrite  bool
	failRespAfterWrite  bool
}

func (p *scriptedStreamingProvider) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}

func (p *scriptedStreamingProvider) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}

func (p *scriptedStreamingProvider) StreamChatCompletions(_ context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	p.chatCalls++
	if p.chatCalls <= p.failChatBeforeWrite {
		return openai.ChatCompletionResponse{}, statusError("scripted", 503)
	}
	payload := fmt.Sprintf(`{"id":"chat-scripted","model":%q,"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`, request.Model)
	if err := write(payload); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if p.failChatAfterWrite {
		return openai.ChatCompletionResponse{}, statusError("scripted", 503)
	}
	return openai.ChatCompletionResponse{
		ID: "chat-scripted", Model: request.Model,
		Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
	}, nil
}

func (p *scriptedStreamingProvider) StreamResponses(_ context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	p.responseCalls++
	if p.responseCalls <= p.failRespBeforeWrite {
		return openai.ResponseResponse{}, statusError("scripted", 503)
	}
	if err := write("response.output_text.delta", `{"type":"response.output_text.delta","response_id":"resp-scripted","output_index":0,"delta":"ok"}`); err != nil {
		return openai.ResponseResponse{}, err
	}
	if p.failRespAfterWrite {
		return openai.ResponseResponse{}, statusError("scripted", 503)
	}
	return openai.ResponseResponse{ID: "resp-scripted", Object: "response", Status: "completed", Model: request.Model, OutputText: "ok"}, nil
}

func (splitStreamingProvider) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}

func (splitStreamingProvider) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}

func (splitStreamingProvider) StreamChatCompletions(_ context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	for _, content := range []string{"email={{", "EMA", "IL", "_", "1", "}}"} {
		payload := fmt.Sprintf(`{"id":"chat-split","model":"%s","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, request.Model, content)
		if err := write(payload); err != nil {
			return openai.ChatCompletionResponse{}, err
		}
	}
	if err := write(fmt.Sprintf(`{"id":"chat-split","model":"%s","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, request.Model)); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return openai.ChatCompletionResponse{
		ID: "chat-split", Model: request.Model,
		Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "email={{EMAIL_1}}"}, FinishReason: "stop"}},
	}, nil
}

func (splitStreamingProvider) StreamResponses(_ context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	for _, delta := range []string{"email={{", "EMA", "IL", "_", "1", "}}"} {
		payload := fmt.Sprintf(`{"type":"response.output_text.delta","response_id":"resp-split","output_index":0,"delta":%q}`, delta)
		if err := write("response.output_text.delta", payload); err != nil {
			return openai.ResponseResponse{}, err
		}
	}
	response := openai.ResponseResponse{
		ID: "resp-split", Object: "response", Status: "completed", Model: request.Model, OutputText: "email={{EMAIL_1}}",
		Output: []openai.ResponseOutputItem{{Type: "message", Content: []openai.ResponseOutputContent{{Type: "output_text", Text: "email={{EMAIL_1}}"}}}},
	}
	completed, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
	if err := write("response.completed", string(completed)); err != nil {
		return openai.ResponseResponse{}, err
	}
	return response, nil
}

type orderingAffinity struct {
	stored bool
}

func (*orderingAffinity) get(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (a *orderingAffinity) set(context.Context, string, string) error {
	a.stored = true
	return nil
}

type affinityOrderingModule struct {
	affinity *orderingAffinity
	commits  int
}

func (m *affinityOrderingModule) Name() string   { return "billing" }
func (m *affinityOrderingModule) Required() bool { return true }
func (*affinityOrderingModule) Handle(context.Context, *modules.RequestContext) error {
	return nil
}
func (*affinityOrderingModule) PostResponseEnabled() bool { return true }
func (m *affinityOrderingModule) HandlePostResponse(context.Context, *modules.RequestContext) error {
	if !m.affinity.stored {
		return errors.New("billing committed before response affinity")
	}
	m.commits++
	return nil
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
	p.model = request.Model
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

func TestRouterRerankFailoverAliasAndRestoresOriginalDocument(t *testing.T) {
	failing := &rerankTestClient{err: &Error{Class: FailureUnavailable, Err: errors.New("temporary")}}
	success := &rerankTestClient{}
	router := Router{
		endpoints: []Endpoint{
			{Name: "chat-only", Type: "demo", Priority: 0, Capabilities: []string{"chat"}, Provider: success, Admission: newAdmissionController(0, 0, 0)},
			{Name: "rerank-a", Type: "openai-compatible", Priority: 1, Capabilities: []string{"rerank"}, Provider: failing, Admission: newAdmissionController(0, 0, 0)},
			{Name: "rerank-b", Type: "openai-compatible", Priority: 2, Capabilities: []string{"rerank"}, ModelAliases: map[string]string{"public-rerank": "upstream-rerank"}, Provider: success, Admission: newAdmissionController(0, 0, 0)},
		},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	returnDocuments := true
	request := openai.RerankRequest{Model: "public-rerank", Query: "refund", Documents: []any{"shipping", map[string]any{"text": "refund policy", "id": "doc-2"}}, ReturnDocuments: &returnDocuments}
	response, err := router.Rerank(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, RerankRequest: &request})
	if err != nil {
		t.Fatal(err)
	}
	if failing.calls != 1 || success.calls != 1 || success.model != "upstream-rerank" {
		t.Fatalf("calls failing=%d success=%d model=%q", failing.calls, success.calls, success.model)
	}
	document, ok := response.Results[0].Document.(map[string]any)
	if !ok || document["id"] != "doc-2" {
		t.Fatalf("original document not restored: %+v", response.Results[0].Document)
	}
}

func TestRouterModerationsRequiresCapabilityAndAppliesAlias(t *testing.T) {
	client := &moderationTestClient{}
	router := Router{
		endpoints: []Endpoint{
			{Name: "chat-only", Type: "openai-compatible", Capabilities: []string{"chat"}, Provider: client, Admission: newAdmissionController(0, 0, 0)},
			{Name: "moderation", Type: "openai-compatible", Capabilities: []string{"moderation"}, ModelAliases: map[string]string{"safe": "safe-upstream"}, Provider: client, Admission: newAdmissionController(0, 0, 0)},
		},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ModerationRequest{Model: "safe", Input: "inspect"}
	response, err := router.Moderations(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, ModerationRequest: &request})
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 || client.model != "safe-upstream" || response.Model != "safe-upstream" {
		t.Fatalf("calls=%d model=%q response=%+v", client.calls, client.model, response)
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

func TestRouterStoresResponsesAffinityBeforePostResponseCommit(t *testing.T) {
	affinity := &orderingAffinity{}
	billing := &affinityOrderingModule{affinity: affinity}
	client := &affinityResponseClient{id: "resp-ordered"}
	router := Router{
		endpoints: []Endpoint{{Name: "endpoint-a", Type: "demo", Provider: client}},
		modules:   modules.NewPipeline([]modules.Module{billing}), health: newEndpointHealthTracker(),
		routeCounter: &atomic.Uint64{}, affinity: affinity,
	}
	request := openai.ResponseRequest{Model: "test-model", Input: "first"}
	_, err := router.Responses(context.Background(), modules.RequestContext{
		CredentialID: "tenant-a", Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request,
	})
	if err != nil {
		t.Fatal(err)
	}
	if billing.commits != 1 {
		t.Fatalf("expected one post-response commit, got %d", billing.commits)
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

type responseCaptureModule struct{ text string }

func (*responseCaptureModule) Name() string                                          { return "output-capture" }
func (*responseCaptureModule) Required() bool                                        { return true }
func (*responseCaptureModule) Handle(context.Context, *modules.RequestContext) error { return nil }
func (*responseCaptureModule) PostResponseEnabled() bool                             { return true }
func (m *responseCaptureModule) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	if req.Response != nil && len(req.Response.Choices) > 0 {
		m.text = openai.ContentText(req.Response.Choices[0].Message.Content)
	}
	return nil
}

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

type mcpCaptureClient struct{ *modelCaptureProvider }

func (mcpCaptureClient) SupportsMCP() bool { return true }

type visionCaptureClient struct{ *modelCaptureProvider }

func (visionCaptureClient) SupportsVision() bool { return true }

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

func TestRouterFallsBackWhenPrimaryAdmissionIsFull(t *testing.T) {
	primary := &countingProvider{content: "primary"}
	secondary := &countingProvider{content: "fallback"}
	primaryAdmission := newAdmissionController(1, 0, 0)
	release, err := primaryAdmission.acquire(context.Background(), "primary")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	router := Router{
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, Priority: 1, Provider: primary, Admission: primaryAdmission},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Priority: 2, Provider: secondary},
		},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	response, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	if primary.calls != 0 || secondary.calls != 1 || openai.ContentText(response.Choices[0].Message.Content) != "fallback" {
		t.Fatalf("admission fallback failed: primary=%d secondary=%d response=%+v", primary.calls, secondary.calls, response)
	}
}

func TestStreamingAdmissionRejectsBeforeStreamStarts(t *testing.T) {
	admission := newAdmissionController(1, 0, 0)
	release, err := admission.acquire(context.Background(), "streaming")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	router := Router{
		endpoints: []Endpoint{{
			Name: "streaming", Type: "openai", Models: []string{"model"}, Capabilities: []string{"chat", "stream"},
			Provider: splitStreamingProvider{}, Admission: admission,
		}},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	_, streamed, err := router.StreamChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model", Stream: true},
	}, func(string) error { return nil })
	var admissionErr *AdmissionError
	if streamed || !errors.As(err, &admissionErr) {
		t.Fatalf("admission must fail before SSE starts: streamed=%v err=%v", streamed, err)
	}
}

func TestOutputDLPUsesBufferedStreamingFallback(t *testing.T) {
	client := &scriptedStreamingProvider{}
	router := Router{
		endpoints: []Endpoint{{Name: "guarded", Type: "openai", Models: []string{"model"}, Capabilities: []string{"chat", "responses", "stream"}, OutputDLPEnabled: true, Provider: client}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	writes := 0
	_, streamed, err := router.StreamChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model", Stream: true}}, func(string) error { writes++; return nil })
	if err != nil || streamed || writes != 0 || client.chatCalls != 0 {
		t.Fatalf("chat output leaked before scanning: streamed=%v writes=%d calls=%d err=%v", streamed, writes, client.chatCalls, err)
	}
	responseRequest := openai.ResponseRequest{Model: "model", Stream: true, Input: "hello"}
	_, streamed, err = router.StreamResponses(context.Background(), modules.RequestContext{ResponseRequest: &responseRequest}, func(string, string) error { writes++; return nil })
	if err != nil || streamed || writes != 0 || client.responseCalls != 0 {
		t.Fatalf("responses output leaked before scanning: streamed=%v writes=%d calls=%d err=%v", streamed, writes, client.responseCalls, err)
	}
}

func TestNativeMessagesServerToolsUseBufferedStreamingFallback(t *testing.T) {
	client := &scriptedStreamingProvider{}
	router := Router{
		endpoints: []Endpoint{{Name: "native", Type: "anthropic", Models: []string{"model"}, Capabilities: []string{"chat", "stream", "web_fetch"}, Provider: client}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	maximum := 1
	request := openai.ChatCompletionRequest{Model: "model", Stream: true, ChatGenerationOptions: openai.ChatGenerationOptions{WebFetchOptions: &openai.ChatWebFetchOptions{AllowedDomains: []string{"example.com"}, MaxUses: &maximum, MaxContentTokens: 100}}}
	context := modules.RequestContext{Request: request, Metadata: map[string]string{"gateway.api_type": "messages"}}
	writes := 0
	_, streamed, err := router.StreamChatCompletions(t.Context(), context, func(string) error { writes++; return nil })
	if err != nil || streamed || writes != 0 || client.chatCalls != 0 {
		t.Fatalf("native content streamed before validation: streamed=%v writes=%d calls=%d err=%v", streamed, writes, client.chatCalls, err)
	}
}

type nativeContentProvider struct{ calls int }

func (p *nativeContentProvider) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	p.calls++
	return openai.ChatCompletionResponse{ID: "native", Model: "model", Choices: []openai.Choice{{FinishReason: "stop", Message: openai.Message{Role: "assistant", NativeContent: []json.RawMessage{json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"id","content":[]}`)}}}}}, nil
}

func (p *nativeContentProvider) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, errors.New("unexpected Responses call")
}

func TestRouterDoesNotCacheNativeMessageContent(t *testing.T) {
	upstream := &nativeContentProvider{}
	router := Router{
		cache: newExactCache(time.Minute), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, modules: modules.NewPipeline(nil),
		endpoints: []Endpoint{{Name: "native", Type: "anthropic", Models: []string{"model"}, Provider: upstream}},
	}
	request := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "same"}}}}
	for range 2 {
		if _, err := router.ChatCompletions(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	if upstream.calls != 2 {
		t.Fatalf("native content was cached without its blocks: calls=%d", upstream.calls)
	}
}

func TestRouterRejectsProviderOutputAfterInputDLPAllows(t *testing.T) {
	scans := 0
	scanner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scans++
		var request modules.ScanRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		allowed := scans == 1
		_ = json.NewEncoder(w).Encode(modules.ScanResponse{Allowed: allowed})
	}))
	defer scanner.Close()
	upstream := &countingProvider{content: "provider secret"}
	router := Router{
		endpoints: []Endpoint{{Name: "guarded", Type: "demo", Models: []string{"model"}, DLPEnabled: true, OutputDLPEnabled: true, Provider: upstream}},
		modules:   modules.NewPipeline([]modules.Module{modules.NewProviderRemoteModule("dlp", false, scanner.URL)}),
		health:    newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{RequestID: "execution-output", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "safe input"}}}})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.Class != FailurePostProcessing || !errors.Is(err, modules.ErrContentRejected) {
		t.Fatalf("expected post-processing rejection, got %v", err)
	}
	if scans != 2 || upstream.calls != 1 {
		t.Fatalf("unexpected lifecycle scans=%d upstream=%d", scans, upstream.calls)
	}
}

func TestStreamChatRetriesAndFallsBackOnlyBeforeFirstChunk(t *testing.T) {
	primary := &scriptedStreamingProvider{failChatBeforeWrite: 2}
	secondary := &scriptedStreamingProvider{}
	telemetry := &attemptMetadataModule{}
	var retryDelays []time.Duration
	router := Router{
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, Capabilities: []string{"chat", "stream"}, MaxRetries: 1, Provider: primary},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Capabilities: []string{"chat", "stream"}, Provider: secondary},
		},
		modules: modules.NewPipeline([]modules.Module{telemetry}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		retry: retryScheduler{
			wait: func(_ context.Context, delay time.Duration) error {
				retryDelays = append(retryDelays, delay)
				return nil
			},
			jitter: func(time.Duration) time.Duration { return 0 },
		},
	}

	writes := 0
	_, streamed, err := router.StreamChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model", Stream: true},
	}, func(string) error {
		writes++
		return nil
	})
	if err != nil || !streamed {
		t.Fatalf("expected successful fallback stream, streamed=%v err=%v", streamed, err)
	}
	if primary.chatCalls != 2 || secondary.chatCalls != 1 || writes != 1 {
		t.Fatalf("unexpected attempts: primary=%d secondary=%d writes=%d", primary.chatCalls, secondary.chatCalls, writes)
	}
	if telemetry.metadata["provider.retry_count"] != "1" || telemetry.metadata["provider.fallback_count"] != "1" {
		t.Fatalf("unexpected retry metadata: %+v", telemetry.metadata)
	}
	if len(retryDelays) != 1 || retryDelays[0] != retryInitialDelay {
		t.Fatalf("stream retry delays=%v", retryDelays)
	}
	if _, found := telemetry.metadata["provider.first_token_latency_ms"]; !found {
		t.Fatalf("missing TTFT metadata: %+v", telemetry.metadata)
	}
}

func TestStreamChatDoesNotFallbackAfterFirstChunk(t *testing.T) {
	primary := &scriptedStreamingProvider{failChatAfterWrite: true}
	secondary := &scriptedStreamingProvider{}
	router := Router{
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, Capabilities: []string{"chat", "stream"}, MaxRetries: 2, Provider: primary},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Capabilities: []string{"chat", "stream"}, Provider: secondary},
		},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}

	writes := 0
	_, streamed, err := router.StreamChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model", Stream: true},
	}, func(string) error {
		writes++
		return nil
	})
	if err == nil || !streamed {
		t.Fatalf("expected terminal partial-stream error, streamed=%v err=%v", streamed, err)
	}
	if primary.chatCalls != 1 || secondary.chatCalls != 0 || writes != 1 {
		t.Fatalf("partial response was regenerated: primary=%d secondary=%d writes=%d", primary.chatCalls, secondary.chatCalls, writes)
	}
}

func TestStreamResponsesFallsBackBeforeFirstEvent(t *testing.T) {
	primary := &scriptedStreamingProvider{failRespBeforeWrite: 1}
	secondary := &scriptedStreamingProvider{}
	telemetry := &attemptMetadataModule{}
	router := Router{
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, Capabilities: []string{"responses", "stream"}, Provider: primary},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Capabilities: []string{"responses", "stream"}, Provider: secondary},
		},
		modules: modules.NewPipeline([]modules.Module{telemetry}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ResponseRequest{Model: "model", Stream: true, Input: "hello"}

	writes := 0
	_, streamed, err := router.StreamResponses(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request,
	}, func(string, string) error {
		writes++
		return nil
	})
	if err != nil || !streamed {
		t.Fatalf("expected successful response fallback, streamed=%v err=%v", streamed, err)
	}
	if primary.responseCalls != 1 || secondary.responseCalls != 1 || writes != 1 {
		t.Fatalf("unexpected attempts: primary=%d secondary=%d writes=%d", primary.responseCalls, secondary.responseCalls, writes)
	}
	if telemetry.metadata["provider.retry_count"] != "0" || telemetry.metadata["provider.fallback_count"] != "1" {
		t.Fatalf("unexpected retry metadata: %+v", telemetry.metadata)
	}
}

func TestStreamResponsesDoesNotFallbackAfterFirstEvent(t *testing.T) {
	primary := &scriptedStreamingProvider{failRespAfterWrite: true}
	secondary := &scriptedStreamingProvider{}
	router := Router{
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, Capabilities: []string{"responses", "stream"}, MaxRetries: 2, Provider: primary},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Capabilities: []string{"responses", "stream"}, Provider: secondary},
		},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ResponseRequest{Model: "model", Stream: true, Input: "hello"}

	writes := 0
	_, streamed, err := router.StreamResponses(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request,
	}, func(string, string) error {
		writes++
		return nil
	})
	if err == nil || !streamed {
		t.Fatalf("expected terminal partial-stream error, streamed=%v err=%v", streamed, err)
	}
	if primary.responseCalls != 1 || secondary.responseCalls != 0 || writes != 1 {
		t.Fatalf("partial response was regenerated: primary=%d secondary=%d writes=%d", primary.responseCalls, secondary.responseCalls, writes)
	}
}

func TestRouterObservesEveryProviderRetryAndCacheOperation(t *testing.T) {
	observer := &recordingProviderObserver{}
	telemetry := &attemptMetadataModule{}
	primary := &countingProvider{err: statusError("primary", 503)}
	secondary := &countingProvider{content: "fallback"}
	router := Router{
		health: newEndpointHealthTracker(), observer: observer,
		modules: modules.NewPipeline([]modules.Module{telemetry}),
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
	if telemetry.metadata["provider.retry_count"] != "1" || telemetry.metadata["provider.fallback_count"] != "1" {
		t.Fatalf("unexpected retry metadata: %+v", telemetry.metadata)
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
	if telemetry.metadata["provider.status"] != "ok" || telemetry.metadata["provider.latency_ms"] == "" || telemetry.metadata["provider.retry_count"] != "0" || telemetry.metadata["provider.fallback_count"] != "0" {
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
	if telemetry.metadata["provider.cache.status"] != "hit" || telemetry.metadata["provider.cache.kind"] != "exact" || upstream.calls != 1 {
		t.Fatalf("expected tenant cache hit and post-module execution, calls=%d metadata=%v", upstream.calls, telemetry.metadata)
	}
	if err := request("tenant-b"); err != nil {
		t.Fatal(err)
	}
	if upstream.calls != 2 {
		t.Fatalf("different tenant must not share cache, calls=%d", upstream.calls)
	}
}

func TestRouterDoesNotCacheResponseWhenPostProcessingFails(t *testing.T) {
	upstream := &countingProvider{content: "blocked"}
	router := Router{
		cache: newExactCache(time.Minute), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		modules:   modules.NewPipeline([]modules.Module{failingPostModule{}}),
		endpoints: []Endpoint{{Name: "primary", Type: "openai", Models: []string{"model"}, Provider: upstream}},
	}
	req := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "same prompt"}}}}
	for range 2 {
		if _, err := router.ChatCompletions(context.Background(), req); err == nil {
			t.Fatal("expected post-processing failure")
		}
	}
	if upstream.calls != 2 {
		t.Fatalf("failed response was cached: upstream calls=%d", upstream.calls)
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

func TestPostResponseModulesSeeClientVisibleDeanonymizedText(t *testing.T) {
	echo := &echoProvider{}
	capture := &responseCaptureModule{}
	router := Router{
		defaultProvider: "demo",
		modules: modules.NewPipeline([]modules.Module{
			modules.NewAnonymizerModule(true, modules.RuleEmail), capture,
		}),
		endpoints: []Endpoint{{Name: "echo", Type: "demo", Provider: echo}},
	}
	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "test-model", Messages: []openai.Message{{Role: "user", Content: "user@example.com"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if capture.text != "user@example.com" {
		t.Fatalf("post-response module saw %q", capture.text)
	}
}

func TestRouterDeanonymizesChatPlaceholdersSplitAcrossStreamChunks(t *testing.T) {
	router := Router{
		modules:   modules.NewPipeline([]modules.Module{modules.NewAnonymizerModule(true, modules.RuleEmail)}),
		endpoints: []Endpoint{{Name: "stream", Type: "demo", Capabilities: []string{"chat", "stream"}, Provider: splitStreamingProvider{}}},
		health:    newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ChatCompletionRequest{Model: "test-model", Stream: true, Messages: []openai.Message{{Role: "user", Content: "user@example.com"}}}
	var streamed strings.Builder
	_, didStream, err := router.StreamChatCompletions(context.Background(), modules.RequestContext{Request: request}, func(payload string) error {
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return err
		}
		if len(chunk.Choices) > 0 {
			streamed.WriteString(chunk.Choices[0].Delta.Content)
		}
		if strings.Contains(payload, "{{EMAIL_1}}") {
			return errors.New("placeholder leaked in chat stream")
		}
		return nil
	})
	if err != nil || !didStream {
		t.Fatalf("stream failed: streamed=%v err=%v", didStream, err)
	}
	if streamed.String() != "email=user@example.com" {
		t.Fatalf("unexpected deanonymized stream %q", streamed.String())
	}
}

func TestRouterDeanonymizesResponsesPlaceholdersSplitAcrossStreamEvents(t *testing.T) {
	router := Router{
		modules:   modules.NewPipeline([]modules.Module{modules.NewAnonymizerModule(true, modules.RuleEmail)}),
		endpoints: []Endpoint{{Name: "stream", Type: "demo", Capabilities: []string{"responses", "stream"}, Provider: splitStreamingProvider{}}},
		health:    newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ResponseRequest{Model: "test-model", Stream: true, Input: "user@example.com"}
	var streamed strings.Builder
	response, didStream, err := router.StreamResponses(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request,
	}, func(event, payload string) error {
		if strings.Contains(payload, "{{EMAIL_1}}") {
			return errors.New("placeholder leaked in responses stream")
		}
		if event == "response.output_text.delta" {
			var delta struct {
				Delta string `json:"delta"`
			}
			if err := json.Unmarshal([]byte(payload), &delta); err != nil {
				return err
			}
			streamed.WriteString(delta.Delta)
		}
		return nil
	})
	if err != nil || !didStream {
		t.Fatalf("stream failed: streamed=%v err=%v", didStream, err)
	}
	if streamed.String() != "email=user@example.com" || response.OutputText != "email=user@example.com" {
		t.Fatalf("unexpected deanonymized stream=%q response=%q", streamed.String(), response.OutputText)
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

func TestProviderAttemptContextCombinesAttachedAndDeploymentGuardrails(t *testing.T) {
	req := modules.RequestContext{Metadata: map[string]string{
		"policy.guardrail.required":  "true",
		"policy.guardrail.names":     "strict",
		"policy.modules.dlp.enabled": "true",
		"policy.modules.av.enabled":  "false",
	}}
	attempt := providerAttemptContext(req, Endpoint{Name: "endpoint", ProviderID: "provider", Type: "demo", AVEnabled: true})
	if attempt.Metadata["provider.modules.dlp.enabled"] != "true" || attempt.Metadata["provider.modules.av.enabled"] != "true" {
		t.Fatalf("attached and deployment guardrails were not combined: %+v", attempt.Metadata)
	}
	if attempt.Metadata["provider.guardrail.attached_policies"] != "strict" {
		t.Fatalf("attached policy identity was lost: %+v", attempt.Metadata)
	}
	if attempt.Metadata["provider.guardrail.policy"] != "strict" {
		t.Fatalf("attached policy was not exposed to guardrail observability: %+v", attempt.Metadata)
	}
}

func TestGuardrailUnavailableIsTerminalAcrossFallbacks(t *testing.T) {
	if !terminalModuleError(errors.Join(errors.New("scanner failed"), modules.ErrGuardrailUnavailable)) {
		t.Fatal("required guardrail unavailability must not fall through to another endpoint")
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

func TestRouterRequiresExplicitMCPAdapterAndCapability(t *testing.T) {
	legacy := &modelCaptureProvider{content: "legacy"}
	mcp := &modelCaptureProvider{content: "mcp"}
	router := Router{
		endpoints: []Endpoint{
			{Name: "legacy", Type: "demo", Priority: 1, Provider: mcpCaptureClient{legacy}},
			{Name: "declared-but-unsupported", Type: "demo", Priority: 2, Capabilities: []string{"responses", "tools", "mcp"}, Provider: staticProvider{content: "unsupported"}},
			{Name: "mcp", Type: "openai-compatible", Priority: 3, Capabilities: []string{"responses", "tools", "mcp"}, Provider: mcpCaptureClient{mcp}},
		},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ResponseRequest{Model: "test-model", Input: "weather", Tools: []openai.ResponseTool{{Type: "mcp", ServerLabel: "weather", ServerURL: "https://mcp.example.test"}}}
	if _, err := router.Responses(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request}); err != nil {
		t.Fatal(err)
	}
	if legacy.seenModel != "" || mcp.seenModel != "test-model" {
		t.Fatalf("MCP request reached an undeclared/unsupported endpoint: legacy=%q mcp=%q", legacy.seenModel, mcp.seenModel)
	}
}

func TestRouterRequiresExplicitVisionAdapterAndCapability(t *testing.T) {
	legacy := &modelCaptureProvider{content: "legacy"}
	vision := &modelCaptureProvider{content: "vision"}
	router := Router{
		endpoints: []Endpoint{
			{Name: "legacy", Type: "demo", Priority: 1, AVEnabled: true, Provider: visionCaptureClient{legacy}},
			{Name: "declared-but-unsupported", Type: "demo", Priority: 2, AVEnabled: true, Capabilities: []string{"chat", "vision"}, Provider: staticProvider{content: "unsupported"}},
			{Name: "vision-without-av", Type: "openai-compatible", Priority: 3, Capabilities: []string{"chat", "vision"}, Provider: visionCaptureClient{legacy}},
			{Name: "vision", Type: "openai-compatible", Priority: 4, AVEnabled: true, Capabilities: []string{"chat", "vision"}, Provider: visionCaptureClient{vision}},
		},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ChatCompletionRequest{Model: "test-model", Messages: []openai.Message{{Role: "user", Content: []any{
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
	}}}}
	if _, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: request}); err != nil {
		t.Fatal(err)
	}
	if legacy.seenModel != "" || vision.seenModel != "test-model" {
		t.Fatalf("vision request reached undeclared/unsupported endpoint: legacy=%q vision=%q", legacy.seenModel, vision.seenModel)
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
	router := Router{catalog: modelcatalog.NewRegistry(catalog, nil, time.Second), health: newEndpointHealthTracker(), endpoints: []Endpoint{
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
	router := Router{catalog: modelcatalog.NewRegistry(catalog, nil, time.Second), health: newEndpointHealthTracker(), endpoints: []Endpoint{
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
	router := Router{catalog: modelcatalog.NewRegistry(catalog, nil, time.Second), health: newEndpointHealthTracker(), endpoints: []Endpoint{{
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
		Input: []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{
			"data": "UklGRgAAAABXQVZF", "format": "wav",
		}}},
	}, true)
	if strings.Join(required, ",") != "responses,stream,tools,structured_output,audio" {
		t.Fatalf("unexpected Responses capabilities: %v", required)
	}
}

func TestChatWebSearchRequiresExplicitEndpointCapability(t *testing.T) {
	legacy := &modelCaptureProvider{content: "legacy"}
	search := &modelCaptureProvider{content: "search"}
	router := Router{health: newEndpointHealthTracker(), endpoints: []Endpoint{
		{Name: "legacy", Type: "openai-compatible", Priority: 1, Provider: legacy},
		{Name: "search", Type: "openai-compatible", Priority: 2, Capabilities: []string{"chat", "web_search"}, Provider: search},
	}}
	_, err := router.ChatCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{
		Model: "model", ChatGenerationOptions: openai.ChatGenerationOptions{WebSearchOptions: &openai.ChatWebSearchOptions{}},
	}})
	if err != nil || legacy.seenModel != "" || search.seenModel != "model" {
		t.Fatalf("web search routing used an undeclared endpoint: legacy=%q search=%q err=%v", legacy.seenModel, search.seenModel, err)
	}
}

func TestChatWebFetchRequiresExplicitEndpointCapability(t *testing.T) {
	legacy := &modelCaptureProvider{content: "legacy"}
	fetch := &webFetchModelProvider{modelCaptureProvider: modelCaptureProvider{content: "fetch"}}
	router := Router{health: newEndpointHealthTracker(), endpoints: []Endpoint{
		{Name: "legacy", Type: "openai-compatible", Priority: 1, Provider: legacy},
		{Name: "fetch", Type: "openai-compatible", Priority: 2, Capabilities: []string{"chat", "web_fetch"}, Provider: fetch},
	}}
	_, err := router.ChatCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{
		Model: "model", ChatGenerationOptions: openai.ChatGenerationOptions{WebFetchOptions: &openai.ChatWebFetchOptions{AllowedDomains: []string{"example.com"}, MaxContentTokens: 1000}},
	}})
	if err != nil || legacy.seenModel != "" || fetch.seenModel != "model" {
		t.Fatalf("web fetch routing used an undeclared endpoint: legacy=%q fetch=%q err=%v", legacy.seenModel, fetch.seenModel, err)
	}
}

type webFetchModelProvider struct{ modelCaptureProvider }

func (*webFetchModelProvider) SupportsWebFetch() bool { return true }

func TestRuntimeCatalogPricingSnapshotIsAttachedToProviderAttempt(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"runtime-v2","models":[{"provider":"endpoint-a","model":"upstream","input_cost_per_1m":1.5,"output_cost_per_1m":3,"training_cost_per_1m":5,"search_cost_per_1k":10,"currency":"USD"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Endpoint{Name: "endpoint-a", Type: "openai", ModelAliases: map[string]string{"alias": "upstream"}}
	router := Router{catalog: modelcatalog.NewRegistry(catalog, nil, time.Second)}
	req := providerAttemptContext(modules.RequestContext{}, endpoint)
	router.applyCatalogPricing(context.Background(), &req, endpoint, "alias")
	if req.Metadata["model_catalog.version"] != "runtime-v2" || req.Metadata["model_catalog.pricing_key"] != "endpoint-a/upstream" || req.Metadata["model_catalog.input_cost_per_1m"] != "1.5" || req.Metadata["model_catalog.training_cost_per_1m"] != "5" || req.Metadata["model_catalog.search_cost_per_1k"] != "10" {
		t.Fatalf("metadata=%v", req.Metadata)
	}
}

func TestRuntimeCatalogUpdateChangesModelsWithoutRebuildingRouter(t *testing.T) {
	initial, _ := modelcatalog.Parse(`{"version":"v1","unknown_model_policy":"deny","models":[{"provider":"endpoint-a","model":"old","capabilities":["chat"]}]}`)
	updated, _ := modelcatalog.Parse(`{"version":"v2","unknown_model_policy":"deny","models":[{"provider":"endpoint-a","model":"new","capabilities":["chat"]}]}`)
	registry := modelcatalog.NewRegistry(initial, nil, time.Second)
	router := Router{catalog: registry, health: newEndpointHealthTracker(), endpoints: []Endpoint{{Name: "endpoint-a", Type: "openai", Models: []string{"old", "new"}, Provider: &modelCaptureProvider{}}}}
	if models := router.Models(); len(models) != 1 || models[0].ID != "old" {
		t.Fatalf("before=%+v", models)
	}
	if err := registry.Update(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if models := router.Models(); len(models) != 1 || models[0].ID != "new" {
		t.Fatalf("after=%+v", models)
	}
}

func testBoolPtr(value bool) *bool {
	return &value
}
