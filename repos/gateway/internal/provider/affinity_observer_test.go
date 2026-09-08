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

type affinityFailureStore struct {
	err    error
	found  bool
	writes int
}

func (s *affinityFailureStore) get(context.Context, string) (string, bool, error) {
	return "a", s.found, s.err
}
func (s *affinityFailureStore) set(context.Context, string, string) error { s.writes++; return s.err }

type affinityObserver struct{ operations map[string]int }

func (*affinityObserver) ObserveProvider(string, string, string, string, time.Duration) {}
func (o *affinityObserver) ObserveCache(operation, result string) {
	o.operations[operation+"/"+result]++
}

type affinityUsageModule struct{ usage []openai.ResponseUsage }

func (*affinityUsageModule) Name() string                                          { return "billing" }
func (*affinityUsageModule) Required() bool                                        { return true }
func (*affinityUsageModule) Handle(context.Context, *modules.RequestContext) error { return nil }
func (*affinityUsageModule) PostResponseEnabled() bool                             { return true }
func (m *affinityUsageModule) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.usage = append(m.usage, req.ResponsesResponse.Usage)
	return nil
}

type affinityUsageProvider struct{ calls int }

func (*affinityUsageProvider) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}
func (p *affinityUsageProvider) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	p.calls++
	return openai.ResponseResponse{ID: "resp-usage", Model: "m", Usage: openai.ResponseUsage{InputTokens: 7, OutputTokens: 2, TotalTokens: 9}}, nil
}
func (p *affinityUsageProvider) StreamResponses(ctx context.Context, req openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if err := write("response.output_text.delta", `{"type":"response.output_text.delta","delta":"hello"}`); err != nil {
		return openai.ResponseResponse{}, err
	}
	return p.Responses(ctx, req)
}

func TestAffinityWriteFailureIsObservedWithoutLosingUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		observer := &affinityObserver{operations: map[string]int{}}
		store := &affinityFailureStore{err: errors.New("private storage error")}
		billing := &affinityUsageModule{}
		client := &affinityUsageProvider{}
		router := Router{endpoints: []Endpoint{{Name: "a", Type: "demo", Capabilities: []string{"responses", "stream"}, Provider: client}}, modules: modules.NewPipeline([]modules.Module{billing}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, cache: newExactCache(time.Hour), affinity: store, observer: observer}
		request := openai.ResponseRequest{Model: "m", Input: "hello"}
		req := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "m"}, ResponseRequest: &request}
		var err error
		if stream {
			_, _, err = router.StreamResponses(context.Background(), req, func(string, string) error { return nil })
		} else {
			_, err = router.Responses(context.Background(), req)
		}
		if err != nil || client.calls != 1 || len(billing.usage) != 1 || billing.usage[0].TotalTokens != 9 || observer.operations["affinity_set/error"] != 1 {
			t.Fatalf("stream=%v err=%v calls=%d usage=%+v metrics=%v", stream, err, client.calls, billing.usage, observer.operations)
		}
		if !stream {
			if _, err := router.Responses(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if client.calls != 1 || len(billing.usage) != 2 || billing.usage[1].TotalTokens != 0 || observer.operations["affinity_set/error"] != 2 {
				t.Fatalf("cache hit: calls=%d usage=%+v metrics=%v", client.calls, billing.usage, observer.operations)
			}
		}
	}
}

func TestAffinityLookupMetrics(t *testing.T) {
	for _, tc := range []struct {
		name  string
		found bool
		err   error
	}{
		{name: "hit", found: true}, {name: "miss"}, {name: "error", err: errors.New("storage failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer := &affinityObserver{operations: map[string]int{}}
			router := Router{endpoints: []Endpoint{{Name: "a", Type: "demo", Provider: &affinityUsageProvider{}}}, health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, affinity: &affinityFailureStore{found: tc.found, err: tc.err}, observer: observer}
			_, err := router.responseCandidates(context.Background(), modules.RequestContext{CredentialID: "tenant"}, openai.ResponseRequest{Model: "m", PreviousResponse: "resp-private"}, "responses")
			if (err != nil) != (tc.err != nil) || observer.operations["affinity_get/"+tc.name] != 1 || len(observer.operations) != 1 {
				t.Fatalf("err=%v metrics=%v", err, observer.operations)
			}
		})
	}
}

func TestAffinityWritesCountOnlyActualStoreCalls(t *testing.T) {
	observer := &affinityObserver{operations: map[string]int{}}
	store := &affinityFailureStore{}
	router := Router{affinity: store, observer: observer}
	ctx := context.Background()
	req := modules.RequestContext{CredentialID: "tenant"}
	router.rememberResponseAffinity(ctx, req, "resp", "a")
	router.rememberResponseAffinity(ctx, req, "", "a")
	router.rememberResponseAffinity(ctx, modules.RequestContext{}, "resp", "a")
	router.affinity = nil
	router.rememberResponseAffinity(ctx, req, "resp", "a")
	if store.writes != 1 || len(observer.operations) != 1 || observer.operations["affinity_set/ok"] != 1 {
		t.Fatalf("writes=%d metrics=%v", store.writes, observer.operations)
	}
}
