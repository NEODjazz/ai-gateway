package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type mirrorCaptureClient struct {
	chat          chan openai.ChatCompletionRequest
	err           error
	waitForCancel bool
}

func (c *mirrorCaptureClient) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if c.chat != nil {
		c.chat <- request
	}
	if c.waitForCancel {
		<-ctx.Done()
		return openai.ChatCompletionResponse{}, ctx.Err()
	}
	return openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "primary"}}}}, c.err
}
func (c *mirrorCaptureClient) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, c.err
}

type mirrorMaskModule struct{ calls *atomic.Int32 }

func (m mirrorMaskModule) Name() string   { return "mask" }
func (m mirrorMaskModule) Required() bool { return true }
func (m mirrorMaskModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.calls.Add(1)
	req.Request.Messages[0].Content = "masked"
	return nil
}

func TestShadowMirrorIsAsyncMaskedAndExcludedFromPrimaryRouting(t *testing.T) {
	primary := &mirrorCaptureClient{}
	shadow := &mirrorCaptureClient{chat: make(chan openai.ChatCompletionRequest, 1), err: errors.New("shadow failed")}
	var moduleCalls atomic.Int32
	catalog, _ := modelcatalog.Parse("")
	router := Router{catalog: modelcatalog.NewRegistry(catalog, nil, time.Second), health: newEndpointHealthTracker(), modules: modules.NewPipeline([]modules.Module{mirrorMaskModule{calls: &moduleCalls}}), endpoints: []Endpoint{
		{Name: "primary", Type: "demo", ModelAliases: map[string]string{"public": "primary-upstream"}, Provider: primary},
		{Name: "shadow", Type: "demo", ModelAliases: map[string]string{"public": "shadow-upstream"}, Shadow: true, MirrorPercentage: 100, MirrorTimeout: time.Second, Provider: shadow},
	}}
	response, err := router.ChatCompletions(context.Background(), modules.RequestContext{RequestID: "req-mirror", Request: openai.ChatCompletionRequest{Model: "public", Messages: []openai.Message{{Role: "user", Content: "secret"}}}})
	if err != nil || len(response.Choices) != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	select {
	case request := <-shadow.chat:
		if request.Provider != "" || request.Model != "shadow-upstream" || request.Stream || openai.ContentText(request.Messages[0].Content) != "masked" {
			t.Fatalf("shadow request=%+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("shadow request not received")
	}
	if moduleCalls.Load() != 1 {
		t.Fatalf("pipeline calls=%d", moduleCalls.Load())
	}
	models := router.Models()
	if len(models) != 1 || models[0].OwnedBy != "primary" {
		t.Fatalf("models=%+v", models)
	}
}

func TestShadowMirrorTimeoutDoesNotDelayPrimary(t *testing.T) {
	primary := &mirrorCaptureClient{}
	shadow := &mirrorCaptureClient{chat: make(chan openai.ChatCompletionRequest, 1), waitForCancel: true}
	catalog, _ := modelcatalog.Parse("")
	router := Router{catalog: modelcatalog.NewRegistry(catalog, nil, time.Second), health: newEndpointHealthTracker(), modules: modules.NewPipeline(nil), endpoints: []Endpoint{{Name: "primary", Models: []string{"m"}, Provider: primary}, {Name: "shadow", Models: []string{"m"}, Shadow: true, MirrorPercentage: 100, MirrorTimeout: 20 * time.Millisecond, Provider: shadow}}}
	started := time.Now()
	if _, err := router.ChatCompletions(context.Background(), modules.RequestContext{RequestID: "r", Request: openai.ChatCompletionRequest{Model: "m"}}); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("primary waited for shadow")
	}
	select {
	case <-shadow.chat:
	case <-time.After(time.Second):
		t.Fatal("shadow did not start")
	}
}

func TestCacheHitIsNotMirrored(t *testing.T) {
	primary := &mirrorCaptureClient{}
	shadow := &mirrorCaptureClient{chat: make(chan openai.ChatCompletionRequest, 2)}
	catalog, _ := modelcatalog.Parse("")
	router := Router{catalog: modelcatalog.NewRegistry(catalog, nil, time.Second), health: newEndpointHealthTracker(), modules: modules.NewPipeline(nil), cache: newResponseCache(time.Minute, 1<<20, nil), endpoints: []Endpoint{{Name: "primary", Models: []string{"m"}, Provider: primary}, {Name: "shadow", Models: []string{"m"}, Shadow: true, MirrorPercentage: 100, MirrorTimeout: time.Second, Provider: shadow}}}
	req := modules.RequestContext{RequestID: "cache-1", CredentialID: "credential", Request: openai.ChatCompletionRequest{Model: "m"}}
	if _, err := router.ChatCompletions(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	select {
	case <-shadow.chat:
	case <-time.After(time.Second):
		t.Fatal("first call was not mirrored")
	}
	req.RequestID = "cache-2"
	if _, err := router.ChatCompletions(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	select {
	case <-shadow.chat:
		t.Fatal("cache hit was mirrored")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMirrorSamplingIsDeterministic(t *testing.T) {
	endpoint := Endpoint{Name: "shadow", MirrorPercentage: 37.5}
	first := mirrorSample("request", "model", endpoint)
	for i := 0; i < 100; i++ {
		if mirrorSample("request", "model", endpoint) != first {
			t.Fatal("sample decision changed")
		}
	}
}
