package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestResponsesCachePreservesEndpointOwnership(t *testing.T) {
	first := &affinityResponseClient{id: "resp-a"}
	second := &affinityResponseClient{id: "resp-b"}
	router := Router{
		endpoints: []Endpoint{{Name: "a", Type: "demo", Provider: first}, {Name: "b", Type: "demo", Provider: second}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		cache: newExactCache(time.Hour), affinity: newAffinityStore(time.Hour, nil),
	}
	request := openai.ResponseRequest{Model: "m", Input: "hello"}
	req := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "m"}, ResponseRequest: &request}
	for i, want := range []string{"resp-a", "resp-b", "resp-a", "resp-b"} {
		router.routeCounter.Store(uint64(i % 2))
		got, err := router.Responses(context.Background(), req)
		if err != nil || got.ID != want {
			t.Fatalf("request %d: response=%+v err=%v; want %s", i, got, err, want)
		}
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("calls: a=%d b=%d", first.calls, second.calls)
	}
}

func TestResponsesCacheHitRestoresExpiredAffinity(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	affinity := newAffinityStore(time.Minute, nil).(*memoryAffinity)
	affinity.now = func() time.Time { return now }
	first := &affinityResponseClient{id: "resp-a"}
	second := &affinityResponseClient{id: "resp-b"}
	router := Router{
		endpoints: []Endpoint{{Name: "a", Type: "demo", Provider: first}, {Name: "b", Type: "demo", Provider: second}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		cache: newExactCache(time.Hour), affinity: affinity,
	}
	request := openai.ResponseRequest{Model: "m", Input: "hello"}
	req := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "m"}, ResponseRequest: &request}
	if _, err := router.Responses(ctx, req); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, found, err := affinity.get(ctx, affinityKey(req, "resp-a")); err != nil || found {
		t.Fatalf("expected expired affinity: found=%v err=%v", found, err)
	}
	router.routeCounter.Store(0)
	cached, err := router.Responses(ctx, req)
	if err != nil || cached.ID != "resp-a" || first.calls != 1 {
		t.Fatalf("cache hit: response=%+v calls=%d err=%v", cached, first.calls, err)
	}
	request.Input = "continue"
	request.PreviousResponse = cached.ID
	router.routeCounter.Store(1)
	if _, err := router.Responses(ctx, req); err != nil {
		t.Fatal(err)
	}
	if first.calls != 2 || second.calls != 0 {
		t.Fatalf("continuation lost ownership: a=%d b=%d", first.calls, second.calls)
	}
}

func TestResponsesCacheSeparatesUpstreamAliasTargets(t *testing.T) {
	request := openai.ResponseRequest{Model: "alias", Input: "hello"}
	req := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "alias"}, ResponseRequest: &request}
	first := providerAttemptContext(req, Endpoint{Name: "a", Type: "demo", ModelAliases: map[string]string{"alias": "upstream-a"}})
	second := providerAttemptContext(req, Endpoint{Name: "a", Type: "demo", ModelAliases: map[string]string{"alias": "upstream-b"}})
	if providerCacheKey("responses", first) == providerCacheKey("responses", second) {
		t.Fatal("Responses cache shared after upstream alias target changed")
	}
}

func TestResponsesCacheSeparatesSafetyIdentifiers(t *testing.T) {
	request := openai.ResponseRequest{Model: "model", Input: "hello", SafetyIdentifier: "first"}
	first := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request}
	secondRequest := request
	secondRequest.SafetyIdentifier = "second"
	second := first
	second.ResponseRequest = &secondRequest
	if providerCacheKey("responses", first) == providerCacheKey("responses", second) {
		t.Fatal("Responses cache shared across safety identifiers")
	}
}

func TestResponsesCacheSeparatesPromptCacheKeys(t *testing.T) {
	request := openai.ResponseRequest{Model: "model", Input: "hello", PromptCacheKey: "first"}
	first := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request}
	secondRequest := request
	secondRequest.PromptCacheKey = "second"
	second := first
	second.ResponseRequest = &secondRequest
	if providerCacheKey("responses", first) == providerCacheKey("responses", second) {
		t.Fatal("Responses cache shared across prompt cache keys")
	}
}

func TestResponsesCacheSeparatesTextVerbosity(t *testing.T) {
	request := openai.ResponseRequest{Model: "model", Input: "hello", Text: map[string]any{"verbosity": "low"}}
	first := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request}
	secondRequest := request
	secondRequest.Text = map[string]any{"verbosity": "high"}
	second := first
	second.ResponseRequest = &secondRequest
	if providerCacheKey("responses", first) == providerCacheKey("responses", second) {
		t.Fatal("Responses cache shared across text verbosity")
	}
}

func TestResponsesCacheHitStoresAffinityBeforeBilling(t *testing.T) {
	affinity := &orderingAffinity{}
	billing := &affinityOrderingModule{affinity: affinity}
	client := &affinityResponseClient{id: "resp-cached"}
	router := Router{
		endpoints: []Endpoint{{Name: "a", Type: "demo", Provider: client}},
		modules:   modules.NewPipeline([]modules.Module{billing}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		cache: newExactCache(time.Hour), affinity: affinity,
	}
	request := openai.ResponseRequest{Model: "m", Input: "hello"}
	req := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "m"}, ResponseRequest: &request}
	if _, err := router.Responses(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	affinity.stored = false
	cached, err := router.Responses(context.Background(), req)
	if err != nil || cached.Usage != (openai.ResponseUsage{}) || client.calls != 1 || billing.commits != 2 {
		t.Fatalf("cache hit: response=%+v err=%v upstream=%d commits=%d", cached, err, client.calls, billing.commits)
	}
}
