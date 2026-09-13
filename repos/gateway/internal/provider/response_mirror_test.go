package provider

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type responseMirrorCapture struct {
	affinityUsageProvider
	requests chan openai.ResponseRequest
}

type mcpResponseMirrorCapture struct{ *responseMirrorCapture }

func (mcpResponseMirrorCapture) SupportsMCP() bool               { return true }
func (mcpResponseMirrorCapture) SupportsResponseWebSearch() bool { return true }

func (p *responseMirrorCapture) Responses(_ context.Context, req openai.ResponseRequest) (openai.ResponseResponse, error) {
	p.requests <- req
	return openai.ResponseResponse{ID: "shadow-response"}, nil
}

func TestResponsesMirrorsOnlyIndependentRequests(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, previous := range []string{"", "resp-primary"} {
			t.Run(fmt.Sprintf("stream=%v/continuation=%v", stream, previous != ""), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					primary := &affinityUsageProvider{}
					shadow := &responseMirrorCapture{requests: make(chan openai.ResponseRequest, 1)}
					billing := &affinityUsageModule{}
					router := Router{health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, modules: modules.NewPipeline([]modules.Module{billing}), endpoints: []Endpoint{
						{Name: "primary", Models: []string{"m"}, Capabilities: []string{"responses", "stream"}, Provider: primary},
						{Name: "shadow", Models: []string{"m"}, Capabilities: []string{"responses", "stream"}, ModelAliases: map[string]string{"m": "shadow-model"}, Shadow: true, MirrorPercentage: 100, MirrorTimeout: time.Second, Provider: shadow},
					}}
					request := openai.ResponseRequest{Model: "m", Input: "hello", PreviousResponse: previous}
					req := modules.RequestContext{RequestID: "request", CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "m"}, ResponseRequest: &request}
					var err error
					if stream {
						_, _, err = router.StreamResponses(t.Context(), req, func(string, string) error { return nil })
					} else {
						_, err = router.Responses(t.Context(), req)
					}
					if err != nil || primary.calls != 1 || len(billing.usage) != 1 || billing.usage[0].TotalTokens != 9 {
						t.Fatalf("err=%v calls=%d billing=%+v", err, primary.calls, billing.usage)
					}
					synctest.Wait()
					select {
					case mirrored := <-shadow.requests:
						if previous != "" {
							t.Fatalf("continuation sent to shadow: %+v", mirrored)
						}
						if mirrored.Model != "shadow-model" || mirrored.Stream || mirrored.Provider != "" {
							t.Fatalf("invalid shadow request: %+v", mirrored)
						}
					default:
						if previous == "" {
							t.Fatal("initial request was not mirrored")
						}
					}
				})
			})
		}
	}
}

func TestStoredResponsesAreNotMirrored(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := true
				primary := &affinityUsageProvider{}
				shadow := &responseMirrorCapture{requests: make(chan openai.ResponseRequest, 1)}
				backend := &ownershipTestStore{data: map[string][]byte{}}
				router := Router{
					health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, modules: modules.NewPipeline(nil),
					ownership: newResponseOwnershipStore(time.Hour, backend),
					endpoints: []Endpoint{
						{Name: "primary", Models: []string{"m"}, Capabilities: []string{"responses", "stream"}, Provider: primary},
						{Name: "shadow", Models: []string{"m"}, Capabilities: []string{"responses", "stream"}, Shadow: true, MirrorPercentage: 100, MirrorTimeout: time.Second, Provider: shadow},
					},
				}
				request := openai.ResponseRequest{Model: "m", Input: "hello", Store: &store}
				req := modules.RequestContext{RequestID: "request", CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "m"}, ResponseRequest: &request}
				var err error
				if stream {
					_, _, err = router.StreamResponses(t.Context(), req, func(string, string) error { return nil })
				} else {
					_, err = router.Responses(t.Context(), req)
				}
				if err != nil || primary.calls != 1 {
					t.Fatalf("err=%v calls=%d", err, primary.calls)
				}
				synctest.Wait()
				select {
				case mirrored := <-shadow.requests:
					t.Fatalf("stored response sent to shadow: %+v", mirrored)
				default:
				}
			})
		})
	}
}

func TestResponsesPromptCacheComparisonIsNotMirrored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shadow := &responseMirrorCapture{requests: make(chan openai.ResponseRequest, 1)}
		router := Router{health: newEndpointHealthTracker(), modules: modules.NewPipeline(nil), endpoints: []Endpoint{
			{Name: "shadow", Models: []string{"m"}, Capabilities: []string{"responses"}, Shadow: true, MirrorPercentage: 100, MirrorTimeout: time.Second, Provider: shadow},
		}}
		request := openai.ResponseRequest{Model: "m", Input: "hello", PromptCacheOptions: &openai.PromptCacheOptions{ComparisonResponseID: "resp_primary"}}
		router.mirrorResponses(t.Context(), "request", request, "m", "responses")
		synctest.Wait()
		select {
		case mirrored := <-shadow.requests:
			t.Fatalf("prompt-cache comparison sent to shadow: %+v", mirrored)
		default:
		}
	})
}

func TestResponsesHostedToolsAreNotMirrored(t *testing.T) {
	for _, test := range []struct {
		name         string
		tool         openai.ResponseTool
		capabilities []string
	}{
		{name: "mcp", tool: openai.ResponseTool{Type: "mcp", ServerLabel: "documents", ServerURL: "https://documents.example.test"}, capabilities: []string{"responses", "tools", "mcp"}},
		{name: "web search", tool: openai.ResponseTool{Type: "web_search"}, capabilities: []string{"responses", "tools", "web_search"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				capture := &responseMirrorCapture{requests: make(chan openai.ResponseRequest, 1)}
				router := Router{health: newEndpointHealthTracker(), modules: modules.NewPipeline(nil), endpoints: []Endpoint{
					{Name: "shadow", Models: []string{"m"}, Capabilities: test.capabilities, Shadow: true, MirrorPercentage: 100, MirrorTimeout: time.Second, Provider: mcpResponseMirrorCapture{capture}},
				}}
				request := openai.ResponseRequest{Model: "m", Input: "hello", Tools: []openai.ResponseTool{test.tool}}
				router.mirrorResponses(t.Context(), "request", request, "m", test.capabilities...)
				synctest.Wait()
				select {
				case mirrored := <-capture.requests:
					t.Fatalf("hosted tool request sent to shadow: %+v", mirrored)
				default:
				}
			})
		})
	}
}
