package provider

import (
	"context"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type pricingAttemptRecorder struct {
	reserves []string
	commits  []string
}

func (*pricingAttemptRecorder) Name() string   { return "billing" }
func (*pricingAttemptRecorder) Required() bool { return true }
func (r *pricingAttemptRecorder) Handle(_ context.Context, req *modules.RequestContext) error {
	r.reserves = append(r.reserves, req.RequestID+":"+req.Metadata["model_catalog.pricing_key"]+":"+req.Metadata["model_catalog.input_cost_per_1m"])
	return nil
}
func (*pricingAttemptRecorder) PostResponseEnabled() bool { return true }
func (r *pricingAttemptRecorder) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	r.commits = append(r.commits, req.RequestID+":"+req.Metadata["model_catalog.pricing_key"]+":"+req.Metadata["model_catalog.input_cost_per_1m"])
	return nil
}

func TestChatFailoverUsesSelectedDeploymentPricing(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"pricing-v1","models":[{"provider":"deployment-a","model":"upstream","capabilities":["chat"],"input_cost_per_1m":1,"currency":"USD"},{"provider":"deployment-b","model":"upstream","capabilities":["chat"],"input_cost_per_1m":3,"currency":"USD"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	primary := &countingProvider{err: statusError("primary", http.StatusServiceUnavailable)}
	secondary := &countingProvider{content: "ok"}
	recorder := &pricingAttemptRecorder{}
	router := Router{
		catalog: modelcatalog.NewRegistry(catalog, nil, time.Second), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		modules: modules.NewPipeline([]modules.Module{recorder}),
		endpoints: []Endpoint{
			{Name: "deployment-a", Type: "openai-compatible", Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"chat"}, Priority: 1, Provider: primary},
			{Name: "deployment-b", Type: "openai-compatible", Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"chat"}, Priority: 2, Provider: secondary},
		},
	}
	response, err := router.ChatCompletions(t.Context(), modules.RequestContext{RequestID: "execution-1", Request: openai.ChatCompletionRequest{Model: "public"}})
	if err != nil || response.ProviderEndpoint != "deployment-b" || primary.calls != 1 || secondary.calls != 1 {
		t.Fatalf("fallback response=%+v err=%v calls=%d/%d", response, err, primary.calls, secondary.calls)
	}
	if want := []string{"execution-1:deployment-a/upstream:1", "execution-1:deployment-b/upstream:3"}; !reflect.DeepEqual(recorder.reserves, want) {
		t.Fatalf("reserve pricing=%v, want %v", recorder.reserves, want)
	}
	if want := []string{"execution-1:deployment-b/upstream:3"}; !reflect.DeepEqual(recorder.commits, want) {
		t.Fatalf("commit pricing=%v, want %v", recorder.commits, want)
	}
}
