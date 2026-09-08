package provider

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type responseOutcomeClient struct {
	affinityUsageProvider
	outcome openai.ResponseResponse
}

func (p *responseOutcomeClient) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	p.calls++
	return p.outcome, nil
}

func TestResponseCacheOnlyReusesSuccessfulOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		err          *openai.ResponseError
		incomplete   *openai.ResponseIncompleteDetails
		cacheable    bool
	}{
		{name: "completed", status: "completed", cacheable: true},
		{name: "legacy omitted status", cacheable: true},
		{name: "failed", status: "failed"},
		{name: "incomplete", status: "incomplete"},
		{name: "queued", status: "queued"},
		{name: "in progress", status: "in_progress"},
		{name: "cancelled", status: "cancelled"},
		{name: "error detail", status: "completed", err: &openai.ResponseError{Code: "server_error"}},
		{name: "incomplete detail", status: "completed", incomplete: &openai.ResponseIncompleteDetails{Reason: "max_output_tokens"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, seeded := range []bool{false, true} {
				client := &responseOutcomeClient{outcome: openai.ResponseResponse{ID: "r", Model: "m", Status: tc.status, Error: tc.err, IncompleteDetails: tc.incomplete, Usage: openai.ResponseUsage{InputTokens: 7, OutputTokens: 2, TotalTokens: 9}}}
				billing := &affinityUsageModule{}
				endpoint := Endpoint{Name: "a", Type: "demo", Provider: client}
				router := Router{endpoints: []Endpoint{endpoint}, modules: modules.NewPipeline([]modules.Module{billing}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, cache: newExactCache(time.Hour)}
				request := openai.ResponseRequest{Model: "m", Input: "hello"}
				req := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "m"}, ResponseRequest: &request}
				if seeded {
					payload, err := json.Marshal(client.outcome)
					if err != nil {
						t.Fatal(err)
					}
					if err := router.cache.set(t.Context(), providerCacheKey("responses", providerAttemptContext(req, endpoint)), payload); err != nil {
						t.Fatal(err)
					}
				}
				for i := 0; i < 2; i++ {
					if _, err := router.Responses(t.Context(), req); err != nil {
						t.Fatal(err)
					}
				}
				wantCalls, wantTotal := 2, 9
				if tc.cacheable {
					wantCalls, wantTotal = 1, 0
					if seeded {
						wantCalls = 0
					}
				}
				if client.calls != wantCalls || len(billing.usage) != 2 || billing.usage[1].TotalTokens != wantTotal {
					t.Fatalf("seeded=%v calls=%d billing=%+v", seeded, client.calls, billing.usage)
				}
			}
		})
	}
}
