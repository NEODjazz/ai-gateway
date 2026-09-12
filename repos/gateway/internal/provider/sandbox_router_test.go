package provider

import (
	"context"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type sandboxRouterClient struct {
	staticProvider
	request openai.SandboxExecuteRequest
}

func (c *sandboxRouterClient) ExecuteSandbox(_ context.Context, request openai.SandboxExecuteRequest) (openai.SandboxExecutionResult, error) {
	c.request = request
	return openai.SandboxExecutionResult{Object: "code_execution", Stdout: "ok", Results: []map[string]any{}}, nil
}

type sandboxLifecycleModule struct {
	pre  int
	post int
	seen modules.RequestContext
}

func (*sandboxLifecycleModule) Name() string   { return "billing" }
func (*sandboxLifecycleModule) Required() bool { return true }
func (m *sandboxLifecycleModule) Handle(_ context.Context, request *modules.RequestContext) error {
	m.pre++
	return nil
}
func (*sandboxLifecycleModule) PostResponseEnabled() bool { return true }
func (m *sandboxLifecycleModule) HandlePostResponse(_ context.Context, request *modules.RequestContext) error {
	m.post++
	m.seen = *request
	return nil
}

func TestSandboxRouterUsesPinnedCapabilityAliasAndBillingLifecycle(t *testing.T) {
	client := &sandboxRouterClient{}
	billing := &sandboxLifecycleModule{}
	router := Router{
		endpoints: []Endpoint{{
			Name: "sandbox-primary", ProviderID: "sandbox-provider", Type: "opensandbox", Models: []string{"public-code"},
			Capabilities: []string{"sandbox"}, ModelAliases: map[string]string{"public-code": "runtime-code"}, Provider: client,
		}},
		modules: modules.NewPipeline([]modules.Module{billing}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.SandboxExecuteRequest{Model: "public-code", Code: "print('ok')", Language: "python", Template: openai.DefaultSandboxTemplate, TimeoutSeconds: 30}
	response, err := router.ExecuteSandbox(t.Context(), modules.RequestContext{
		RequestID: "execution-id", SandboxRequest: &request,
		Request:  openai.ChatCompletionRequest{Model: request.Model, Messages: []openai.Message{{Role: "user", Content: request.Code}}},
		Metadata: map[string]string{"gateway.api_type": "sandbox"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Stdout != "ok" || client.request.Model != "runtime-code" || client.request.Code != request.Code {
		t.Fatalf("response=%+v request=%+v", response, client.request)
	}
	if billing.pre != 1 || billing.post != 1 || billing.seen.Usage == nil || billing.seen.Metadata["provider.endpoint.name"] != "sandbox-primary" {
		t.Fatalf("pre=%d post=%d usage=%+v metadata=%+v", billing.pre, billing.post, billing.seen.Usage, billing.seen.Metadata)
	}
}
