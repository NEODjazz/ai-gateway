package provider

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/redisstore"
	"github.com/alicebob/miniredis/v2"
)

func TestMemoryDeploymentQuotaRejectsOverflowAndResets(t *testing.T) {
	store := NewMemoryDeploymentQuotaStore()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if allowed, _, err := store.Allow(context.Background(), "deployment", 2, 100, 1, time.Minute); !allowed || err != nil {
		t.Fatalf("first admission: allowed=%v err=%v", allowed, err)
	}
	if allowed, retry, err := store.Allow(context.Background(), "deployment", 2, 100, math.MaxInt, time.Minute); allowed || err != nil || retry != time.Minute {
		t.Fatalf("overflowing admission: allowed=%v retry=%s err=%v", allowed, retry, err)
	}
	now = now.Add(time.Minute)
	if allowed, _, err := store.Allow(context.Background(), "deployment", 2, 100, 1, time.Minute); !allowed || err != nil {
		t.Fatalf("reset admission: allowed=%v err=%v", allowed, err)
	}
}

func TestMemoryDeploymentQuotaBoundsIdentityCardinality(t *testing.T) {
	store := NewMemoryDeploymentQuotaStore()
	for index := range memoryDeploymentQuotaCapacity {
		if allowed, _, err := store.Allow(context.Background(), strconv.Itoa(index), 1, 0, 0, time.Minute); !allowed || err != nil {
			t.Fatalf("identity %d: allowed=%v err=%v", index, allowed, err)
		}
	}
	if allowed, _, err := store.Allow(context.Background(), "overflow", 1, 0, 0, time.Minute); allowed || err == nil {
		t.Fatalf("capacity must fail closed: allowed=%v err=%v", allowed, err)
	}
}

func TestDeploymentQuotaUsesFullTokenReserveAndFallsBack(t *testing.T) {
	primary := &countingProvider{content: "primary"}
	secondary := &countingProvider{content: "fallback"}
	maximum := 37
	request := openai.ChatCompletionRequest{
		Model: "model", MaxCompletionTokens: &maximum,
		Messages: []openai.Message{{Role: "user", Content: "hello"}},
		Tools:    []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}},
	}
	reserve := openai.ChatReserveTokens(request)
	router := Router{
		endpoints: []Endpoint{
			{Name: "primary", Type: "openai", Models: []string{"model"}, Priority: 1, Provider: primary, RateLimitTPM: reserve - 1},
			{Name: "secondary", Type: "openai", Models: []string{"model"}, Priority: 2, Provider: secondary},
		},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, deploymentQuotas: NewMemoryDeploymentQuotaStore(),
	}
	response, err := router.ChatCompletions(context.Background(), modules.RequestContext{Request: request})
	if err != nil {
		t.Fatal(err)
	}
	if primary.calls != 0 || secondary.calls != 1 || openai.ContentText(response.Choices[0].Message.Content) != "fallback" {
		t.Fatalf("quota fallback failed: primary=%d secondary=%d response=%+v", primary.calls, secondary.calls, response)
	}
}

func TestRedisDeploymentQuotaIsSharedAcrossRouters(t *testing.T) {
	server := miniredis.RunT(t)
	store := redisstore.New(redisstore.Config{Addr: server.Addr(), Prefix: t.Name()})
	firstClient := &countingProvider{content: "first"}
	secondClient := &countingProvider{content: "second"}
	makeRouter := func(client *countingProvider) Router {
		return Router{
			endpoints: []Endpoint{{Name: "shared", Type: "openai", Models: []string{"model"}, Provider: client, RateLimitRPM: 1}},
			modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, deploymentQuotas: store,
		}
	}
	request := modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	if _, err := makeRouter(firstClient).ChatCompletions(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	_, err := makeRouter(secondClient).ChatCompletions(context.Background(), request)
	var quota *DeploymentQuotaError
	if !errors.As(err, &quota) || secondClient.calls != 0 || quota.RetryAfter <= 0 {
		t.Fatalf("shared quota was not enforced: calls=%d err=%v", secondClient.calls, err)
	}
}

func TestProviderAndDeploymentQuotasAreReservedAtomically(t *testing.T) {
	store := NewMemoryDeploymentQuotaStore()
	first := &countingProvider{content: "first"}
	second := &countingProvider{content: "second"}
	router := Router{health: newEndpointHealthTracker(), deploymentQuotas: store}
	firstEndpoint := Endpoint{Name: "first", ProviderID: "account", Type: "demo", Provider: first, ProviderRateLimitRPM: 2, RateLimitRPM: 1}
	secondEndpoint := Endpoint{Name: "second", ProviderID: "account", Type: "demo", Provider: second, ProviderRateLimitRPM: 2}
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	if _, _, err := router.callChat(context.Background(), firstEndpoint, request); err != nil {
		t.Fatal(err)
	}
	if _, _, err := router.callChat(context.Background(), firstEndpoint, request); err == nil {
		t.Fatal("deployment quota did not reject the second call")
	} else {
		var quota *DeploymentQuotaError
		if !errors.As(err, &quota) {
			t.Fatalf("expected deployment quota error, got %v", err)
		}
	}
	if _, _, err := router.callChat(context.Background(), secondEndpoint, request); err != nil {
		t.Fatalf("rejected deployment call consumed provider quota: %v", err)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("unexpected provider calls: first=%d second=%d", first.calls, second.calls)
	}
}

func TestProviderQuotaIsSharedByDeployments(t *testing.T) {
	store := NewMemoryDeploymentQuotaStore()
	client := &countingProvider{content: "ok"}
	router := Router{health: newEndpointHealthTracker(), deploymentQuotas: store}
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	for _, endpoint := range []Endpoint{
		{Name: "first", ProviderID: "account", Type: "demo", Provider: client, ProviderRateLimitRPM: 1},
		{Name: "second", ProviderID: "account", Type: "demo", Provider: client, ProviderRateLimitRPM: 1},
	} {
		_, _, err := router.callChat(context.Background(), endpoint, request)
		if endpoint.Name == "first" && err != nil {
			t.Fatal(err)
		}
		if endpoint.Name == "second" {
			var quota *ProviderQuotaError
			if !errors.As(err, &quota) || quota.Provider != "account" {
				t.Fatalf("expected shared provider quota, got %v", err)
			}
		}
	}
}

func TestManagedProviderValidatesQuotaBounds(t *testing.T) {
	router := New(Config{}).(*Router)
	for _, input := range []ManagedProvider{
		{ID: "negative", Type: "demo", RateLimitRPM: -1, Enabled: true},
		{ID: "rpm", Type: "demo", RateLimitRPM: 10000001, Enabled: true},
		{ID: "tpm", Type: "demo", RateLimitTPM: 1000000001, Enabled: true},
	} {
		if _, err := router.CreateProvider(input); !errors.Is(err, ErrInvalidProvider) {
			t.Fatalf("invalid provider quota was accepted: %+v err=%v", input, err)
		}
	}
}
