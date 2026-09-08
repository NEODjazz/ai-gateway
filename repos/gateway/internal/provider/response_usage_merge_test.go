package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type responseEstimateRecorder struct {
	affinityUsageModule
	failures int
}

func (*responseEstimateRecorder) Handle(_ context.Context, req *modules.RequestContext) error {
	req.Usage = &openai.Usage{PromptTokens: 1}
	return nil
}
func (m *responseEstimateRecorder) HandleFailure(context.Context, *modules.RequestContext, error) error {
	m.failures++
	return nil
}

type overflowingResponseClient struct{ responseOutcomeClient }

func (p *overflowingResponseClient) StreamResponses(ctx context.Context, request openai.ResponseRequest, _ ResponseStreamWriter) (openai.ResponseResponse, error) {
	return p.Responses(ctx, request)
}

func TestResponsesEstimateOverflowStopsPostProcessing(t *testing.T) {
	for _, stream := range []bool{false, true} {
		client := &overflowingResponseClient{responseOutcomeClient{outcome: openai.ResponseResponse{ID: "r", Status: "completed", Model: "m", Usage: openai.ResponseUsage{TotalTokens: int(^uint(0) >> 1)}}}}
		recorder := &responseEstimateRecorder{}
		cache := newExactCache(time.Hour)
		router := Router{endpoints: []Endpoint{{Name: "a", Type: "demo", Provider: client}}, modules: modules.NewPipeline([]modules.Module{recorder}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, cache: cache}
		request := openai.ResponseRequest{Model: "m", Input: "hello"}
		req := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "m"}, ResponseRequest: &request}
		var err error
		if stream {
			_, _, err = router.StreamResponses(t.Context(), req, func(string, string) error { return nil })
		} else {
			_, err = router.Responses(t.Context(), req)
		}
		entries, _ := cache.entries.Size()
		if err == nil || client.calls != 1 || len(recorder.usage) != 0 || recorder.failures != 1 || entries != 0 {
			t.Fatalf("stream=%v err=%v calls=%d billing=%+v failures=%d cache=%d", stream, err, client.calls, recorder.usage, recorder.failures, entries)
		}
	}
}

func TestResponseUsageMergeChecksBeforeMutation(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name                  string
		total, output, prompt int
		invalid               bool
	}{
		{"exact boundary", maxInt - 1, 0, 1, false},
		{"total overflow", maxInt, 0, 1, true},
		{"input output overflow", 0, maxInt, 1, true},
		{"negative estimate", 0, 0, -1, true},
		{"normal", 2, 2, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := openai.ResponseResponse{Usage: openai.ResponseUsage{OutputTokens: tc.output, TotalTokens: tc.total}}
			before := response.Usage
			err := mergeResponseUsage(&response, &openai.Usage{PromptTokens: tc.prompt})
			if (err != nil) != tc.invalid {
				t.Fatalf("unexpected err=%v", err)
			}
			if tc.invalid {
				if response.Usage != before {
					t.Fatal("invalid merge mutated usage")
				}
			} else if response.Usage.InputTokens != tc.prompt || response.Usage.TotalTokens != tc.total+tc.prompt {
				t.Fatalf("usage=%+v", response.Usage)
			}
		})
	}
}
