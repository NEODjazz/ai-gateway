package provider

import (
	"context"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type stopAdmissionModule struct{ calls int }

func (*stopAdmissionModule) Name() string   { return "billing" }
func (*stopAdmissionModule) Required() bool { return true }
func (m *stopAdmissionModule) Handle(context.Context, *modules.RequestContext) error {
	m.calls++
	return nil
}

func TestMatchedStopRejectedBeforeProviderModules(t *testing.T) {
	module := &stopAdmissionModule{}
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "unsupported", Type: "openai-compatible", BaseURL: "http://unused.invalid", Models: []string{"model"}, Stream: true}}, Modules: modules.NewPipeline([]modules.Module{module})}).(*Router)
	request := modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hi"}}, Stop: []string{" END "}, RequireMatchedStop: true}}
	_, err := router.ChatCompletions(context.Background(), request)
	assertUnsupportedParameter(t, err, "stop_sequences")
	_, _, err = router.StreamChatCompletions(context.Background(), request, func(string) error { t.Error("unexpected SSE"); return nil })
	assertUnsupportedParameter(t, err, "stop_sequences")
	if module.calls != 0 {
		t.Fatalf("billing reserve ran %d times", module.calls)
	}
}
func TestMatchedStopCacheIsolation(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hi"}}, Stop: []string{" END "}}}
	ordinary := providerCacheKey("chat", request)
	request.Request.RequireMatchedStop = true
	native := providerCacheKey("chat", request)
	if ordinary == "" || native == "" || ordinary == native {
		t.Fatal("matched-stop request shares an ordinary cache entry")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("matched-stop request uses semantic cache")
	}
}
