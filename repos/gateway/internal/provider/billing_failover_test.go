package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type rejectSecondReserve struct {
	reserves int
	cancels  int
}

func (*rejectSecondReserve) Name() string   { return "billing" }
func (*rejectSecondReserve) Required() bool { return true }
func (m *rejectSecondReserve) Handle(context.Context, *modules.RequestContext) error {
	m.reserves++
	if m.reserves == 2 {
		return modules.ErrBudgetExceeded
	}
	return nil
}
func (m *rejectSecondReserve) HandleFailure(context.Context, *modules.RequestContext, error) error {
	m.cancels++
	return nil
}

type failingCompletionProvider struct {
	countingProvider
	completionCalls int
}

type parameterRejectingProvider struct {
	failingCompletionProvider
	err         error
	streamCalls int
}

type corruptCompletionModule struct {
	change func(*modules.RequestContext)
}

type enableResponseStorageModule struct{}

func (enableResponseStorageModule) Name() string   { return "response-policy" }
func (enableResponseStorageModule) Required() bool { return true }
func (enableResponseStorageModule) Handle(_ context.Context, req *modules.RequestContext) error {
	store := true
	req.ResponseRequest.Store = &store
	return nil
}

func (corruptCompletionModule) Name() string   { return "prompt-policy" }
func (corruptCompletionModule) Required() bool { return true }
func (m corruptCompletionModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.change(req)
	return nil
}

func (p *parameterRejectingProvider) ValidateChatParameters(openai.ChatCompletionRequest) error {
	return p.err
}

func (p *parameterRejectingProvider) ValidateResponseParameters(openai.ResponseRequest) error {
	return p.err
}

func (p *parameterRejectingProvider) ValidateCompletionParameters(openai.CompletionRequest) error {
	return p.err
}

func (p *parameterRejectingProvider) StreamChatCompletions(context.Context, openai.ChatCompletionRequest, ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	p.streamCalls++
	return openai.ChatCompletionResponse{}, errors.New("unexpected chat provider call")
}

func (p *parameterRejectingProvider) StreamResponses(context.Context, openai.ResponseRequest, ResponseStreamWriter) (openai.ResponseResponse, error) {
	p.streamCalls++
	return openai.ResponseResponse{}, errors.New("unexpected responses provider call")
}

func (p *failingCompletionProvider) Completions(context.Context, openai.CompletionRequest) (openai.CompletionResponse, error) {
	p.completionCalls++
	return openai.CompletionResponse{}, statusError("completion", http.StatusServiceUnavailable)
}

func (p *failingCompletionProvider) StreamCompletions(context.Context, openai.CompletionRequest, CompletionStreamWriter) (openai.CompletionResponse, error) {
	p.completionCalls++
	return openai.CompletionResponse{}, statusError("completion", http.StatusServiceUnavailable)
}

func TestFailoverParameterRejectionCancelsPreviousReserve(t *testing.T) {
	for _, test := range []struct {
		name         string
		capabilities []string
		first        Client
		run          func(Router) error
	}{
		{
			name: "chat", capabilities: []string{"chat"},
			first: &countingProvider{err: statusError("primary", http.StatusServiceUnavailable)},
			run: func(r Router) error {
				_, err := r.ChatCompletions(t.Context(), modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model"}})
				return err
			},
		},
		{
			name: "stream chat", capabilities: []string{"chat", "stream"},
			first: &scriptedStreamingProvider{failChatBeforeWrite: 1},
			run: func(r Router) error {
				_, _, err := r.StreamChatCompletions(t.Context(), modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model"}}, func(string) error { return nil })
				return err
			},
		},
		{
			name: "responses", capabilities: []string{"responses"},
			first: &countingProvider{err: statusError("primary", http.StatusServiceUnavailable)},
			run: func(r Router) error {
				request := openai.ResponseRequest{Model: "model", Input: "hello"}
				_, err := r.Responses(t.Context(), modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request})
				return err
			},
		},
		{
			name: "stream responses", capabilities: []string{"responses", "stream"},
			first: &scriptedStreamingProvider{failRespBeforeWrite: 1},
			run: func(r Router) error {
				request := openai.ResponseRequest{Model: "model", Input: "hello"}
				_, _, err := r.StreamResponses(t.Context(), modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request}, func(string, string) error { return nil })
				return err
			},
		},
		{
			name: "completions", capabilities: []string{"chat"}, first: &failingCompletionProvider{},
			run: func(r Router) error {
				request := openai.CompletionRequest{Model: "model", Prompt: "hello"}
				_, err := r.Completions(t.Context(), modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, CompletionRequest: &request})
				return err
			},
		},
		{
			name: "stream completions", capabilities: []string{"chat", "stream"}, first: &failingCompletionProvider{},
			run: func(r Router) error {
				request := openai.CompletionRequest{Model: "model", Prompt: "hello"}
				_, _, err := r.StreamCompletions(t.Context(), modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, CompletionRequest: &request}, func(string) error { return nil })
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			validationErr := errors.New("unsupported parameter")
			secondary := &parameterRejectingProvider{err: validationErr}
			billing := &rejectSecondReserve{}
			router := Router{
				health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, modules: modules.NewPipeline([]modules.Module{billing}),
				endpoints: []Endpoint{
					{Name: "primary", Type: "openai", Models: []string{"model"}, Capabilities: test.capabilities, Provider: test.first},
					{Name: "secondary", Type: "openai", Models: []string{"model"}, Capabilities: test.capabilities, Provider: secondary},
				},
			}
			err := test.run(router)
			if !errors.Is(err, validationErr) || billing.reserves != 1 || billing.cancels != 1 || secondary.calls != 0 || secondary.responses != 0 || secondary.completionCalls != 0 || secondary.streamCalls != 0 {
				t.Fatalf("unexpected lifecycle: err=%v reserves=%d cancels=%d secondary=%+v", err, billing.reserves, billing.cancels, secondary)
			}
		})
	}
}

func TestCompletionPolicyFailureCancelsReserve(t *testing.T) {
	for _, test := range []struct {
		name   string
		stream bool
		change func(*modules.RequestContext)
	}{
		{name: "removed request", change: func(req *modules.RequestContext) { req.CompletionRequest = nil }},
		{name: "invalid prompt shape", change: func(req *modules.RequestContext) { req.Request.Messages[0].Content = []any{"unexpected"} }},
		{name: "stream removed request", stream: true, change: func(req *modules.RequestContext) { req.CompletionRequest = nil }},
		{name: "stream invalid prompt shape", stream: true, change: func(req *modules.RequestContext) { req.Request.Messages[0].Content = []any{"unexpected"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			billing := &rejectSecondReserve{}
			client := &failingCompletionProvider{}
			capabilities := []string{"chat"}
			if test.stream {
				capabilities = append(capabilities, "stream")
			}
			router := Router{
				health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
				modules:   modules.NewPipeline([]modules.Module{corruptCompletionModule{change: test.change}, billing}),
				endpoints: []Endpoint{{Name: "deployment", Type: "openai", Models: []string{"model"}, Capabilities: capabilities, Provider: client}},
			}
			request := openai.CompletionRequest{Model: "model", Prompt: "hello"}
			req := modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, CompletionRequest: &request}
			var err error
			if test.stream {
				_, _, err = router.StreamCompletions(t.Context(), req, func(string) error { return nil })
			} else {
				_, err = router.Completions(t.Context(), req)
			}
			if err == nil || billing.reserves != 1 || billing.cancels != 1 || client.completionCalls != 0 {
				t.Fatalf("unexpected lifecycle: err=%v reserves=%d cancels=%d provider calls=%d", err, billing.reserves, billing.cancels, client.completionCalls)
			}
		})
	}
}

func TestResponseOwnershipFailureAfterPolicyCancelsReserve(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "responses"
		if stream {
			name = "stream responses"
		}
		t.Run(name, func(t *testing.T) {
			billing := &rejectSecondReserve{}
			client := &scriptedStreamingProvider{}
			capabilities := []string{"responses"}
			if stream {
				capabilities = append(capabilities, "stream")
			}
			router := Router{
				health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
				modules:   modules.NewPipeline([]modules.Module{enableResponseStorageModule{}, billing}),
				endpoints: []Endpoint{{Name: "deployment", Type: "openai", Models: []string{"model"}, Capabilities: capabilities, Provider: client}},
			}
			request := openai.ResponseRequest{Model: "model", Input: "hello"}
			req := modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request}
			var err error
			if stream {
				_, _, err = router.StreamResponses(t.Context(), req, func(string, string) error { return nil })
			} else {
				_, err = router.Responses(t.Context(), req)
			}
			if !errors.Is(err, ErrResponseOwnershipUnavailable) || billing.reserves != 1 || billing.cancels != 1 || client.responseCalls != 0 {
				t.Fatalf("unexpected lifecycle: err=%v reserves=%d cancels=%d provider calls=%d", err, billing.reserves, billing.cancels, client.responseCalls)
			}
		})
	}
}

func TestFailoverPreflightRejectionRunsPreviousFailureLifecycle(t *testing.T) {
	for _, test := range []struct {
		name         string
		capabilities []string
		first, next  Client
		run          func(Router) error
	}{
		{
			name: "stream chat", capabilities: []string{"chat", "stream"},
			first: &scriptedStreamingProvider{failChatBeforeWrite: 1}, next: &scriptedStreamingProvider{},
			run: func(r Router) error {
				_, _, err := r.StreamChatCompletions(t.Context(), modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model"}}, func(string) error { return nil })
				return err
			},
		},
		{
			name: "responses", capabilities: []string{"responses"},
			first: &countingProvider{err: statusError("primary", http.StatusServiceUnavailable)}, next: &countingProvider{},
			run: func(r Router) error {
				request := openai.ResponseRequest{Model: "model", Input: "hello"}
				_, err := r.Responses(t.Context(), modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request})
				return err
			},
		},
		{
			name: "stream responses", capabilities: []string{"responses", "stream"},
			first: &scriptedStreamingProvider{failRespBeforeWrite: 1}, next: &scriptedStreamingProvider{},
			run: func(r Router) error {
				request := openai.ResponseRequest{Model: "model", Input: "hello"}
				_, _, err := r.StreamResponses(t.Context(), modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request}, func(string, string) error { return nil })
				return err
			},
		},
		{
			name: "completions", capabilities: []string{"chat"},
			first: &failingCompletionProvider{}, next: &failingCompletionProvider{},
			run: func(r Router) error {
				request := openai.CompletionRequest{Model: "model", Prompt: "hello"}
				_, err := r.Completions(t.Context(), modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, CompletionRequest: &request})
				return err
			},
		},
		{
			name: "stream completions", capabilities: []string{"chat", "stream"},
			first: &failingCompletionProvider{}, next: &failingCompletionProvider{},
			run: func(r Router) error {
				request := openai.CompletionRequest{Model: "model", Prompt: "hello"}
				_, _, err := r.StreamCompletions(t.Context(), modules.RequestContext{RequestID: "execution", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, CompletionRequest: &request}, func(string) error { return nil })
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			billing := &rejectSecondReserve{}
			router := Router{
				health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, modules: modules.NewPipeline([]modules.Module{billing}),
				endpoints: []Endpoint{
					{Name: "primary", Type: "openai", Models: []string{"model"}, Capabilities: test.capabilities, Provider: test.first},
					{Name: "secondary", Type: "openai", Models: []string{"model"}, Capabilities: test.capabilities, Provider: test.next},
				},
			}
			err := test.run(router)
			if !errors.Is(err, modules.ErrBudgetExceeded) || billing.reserves != 2 || billing.cancels != 1 {
				t.Fatalf("unexpected lifecycle: err=%v reserves=%d cancels=%d", err, billing.reserves, billing.cancels)
			}
		})
	}
}

func TestChatFailoverBudgetRejectionCancelsPreviousReserve(t *testing.T) {
	var mu sync.Mutex
	var phases []string
	reserveCalls := 0
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request modules.UsageRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid billing request", http.StatusBadRequest)
			return
		}
		if request.RequestID != "execution-failover" {
			t.Errorf("unexpected billing execution ID: %q", request.RequestID)
		}
		mu.Lock()
		phases = append(phases, request.Phase)
		if request.Phase == "reserve" {
			reserveCalls++
		}
		reject := reserveCalls == 2 && request.Phase == "reserve"
		mu.Unlock()
		if reject {
			http.Error(w, "budget exceeded", http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(modules.UsageResponse{})
	}))
	t.Cleanup(billing.Close)
	primary := &countingProvider{err: statusError("primary", http.StatusServiceUnavailable)}
	secondary := &countingProvider{content: "must not run"}
	router := Router{
		health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		modules: modules.NewPipeline([]modules.Module{modules.NewRemoteBillingModule(true, billing.URL)}),
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, Provider: primary},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Provider: secondary},
		},
	}
	_, err := router.ChatCompletions(t.Context(), modules.RequestContext{RequestID: "execution-failover", Request: openai.ChatCompletionRequest{Model: "model"}})
	if !errors.Is(err, modules.ErrBudgetExceeded) || primary.calls != 1 || secondary.calls != 0 {
		t.Fatalf("unexpected fallback outcome: err=%v primary=%d secondary=%d", err, primary.calls, secondary.calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(phases) != 3 || phases[0] != "reserve" || phases[1] != "reserve" || phases[2] != "cancel" {
		t.Fatalf("previous reservation was not canceled: phases=%v", phases)
	}
}
