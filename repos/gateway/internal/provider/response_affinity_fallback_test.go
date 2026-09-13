package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type affinityFallbackClient struct{ fallbackTestClient }

func (p *affinityFallbackClient) StreamResponses(ctx context.Context, req openai.ResponseRequest, _ ResponseStreamWriter) (openai.ResponseResponse, error) {
	return p.Responses(ctx, req)
}

func TestResponsesContinuationDoesNotCrossEndpointOnFailure(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, pin := range []int{0, 1} {
			for _, class := range []FailureClass{FailureUnavailable, FailureContextLength, FailureContentPolicy} {
				t.Run(fmt.Sprintf("stream=%v/pin=%d/%s", stream, pin, class), func(t *testing.T) {
					clients := []*affinityFallbackClient{{}, {}, {}, {}}
					router := fallbackTestRouter(clients[0], clients[1], clients[2], clients[3])
					router.affinity = newAffinityStore(time.Hour, nil)
					original := &Error{Class: class, Err: errors.New("pinned endpoint failed")}
					clients[pin].err = original
					request := openai.ResponseRequest{Model: "primary", Input: "continue", PreviousResponse: "resp-owned"}
					req := modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "primary"}, ResponseRequest: &request}
					if err := router.affinity.set(t.Context(), affinityKey(req, request.PreviousResponse), router.endpoints[pin].Name); err != nil {
						t.Fatal(err)
					}
					var err error
					if stream {
						_, _, err = router.StreamResponses(t.Context(), req, func(string, string) error { t.Fatal("failed continuation emitted SSE"); return nil })
					} else {
						_, err = router.Responses(t.Context(), req)
					}
					if !errors.Is(err, original) {
						t.Fatalf("expected original failure, got %v", err)
					}
					for i, client := range clients {
						want := 0
						if i == pin {
							want = 1
						}
						if client.calls != want {
							t.Fatalf("endpoint %d calls=%d want=%d", i, client.calls, want)
						}
					}
				})
			}
		}
	}
}

func TestResponsesPromptCacheComparisonRequiresOwnedEndpointAffinity(t *testing.T) {
	clients := []*affinityFallbackClient{{}, {}, {}, {}}
	router := fallbackTestRouter(clients[0], clients[1], clients[2], clients[3])
	router.affinity = newAffinityStore(time.Hour, nil)
	request := openai.ResponseRequest{Model: "primary", Input: "compare", PromptCacheOptions: &openai.PromptCacheOptions{Mode: "explicit", ComparisonResponseID: "resp_owned"}}
	owner := modules.RequestContext{CredentialID: "tenant", UserID: "user-a", Request: openai.ChatCompletionRequest{Model: "primary"}, ResponseRequest: &request}
	if err := router.affinity.set(t.Context(), affinityKey(owner, "resp_owned"), router.endpoints[1].Name); err != nil {
		t.Fatal(err)
	}
	candidates, err := router.responseCandidates(t.Context(), owner, request, "responses")
	if err != nil || len(candidates) != 1 || candidates[0].Name != router.endpoints[1].Name {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}

	other := owner
	other.UserID = "user-b"
	_, err = router.responseCandidates(t.Context(), other, request, "responses")
	var failure *Error
	if !errors.As(err, &failure) || failure.StatusCode != http.StatusBadRequest || failure.Param != "prompt_cache_options.comparison_response_id" {
		t.Fatalf("cross-owner comparison was not rejected: %v", err)
	}
}

func TestResponsesPromptCacheComparisonRejectsConflictingReferences(t *testing.T) {
	clients := []*affinityFallbackClient{{}, {}, {}, {}}
	router := fallbackTestRouter(clients[0], clients[1], clients[2], clients[3])
	router.affinity = newAffinityStore(time.Hour, nil)
	request := openai.ResponseRequest{Model: "primary", Input: "compare", PreviousResponse: "resp_previous", PromptCacheOptions: &openai.PromptCacheOptions{ComparisonResponseID: "resp_comparison"}}
	req := modules.RequestContext{CredentialID: "tenant", UserID: "user", Request: openai.ChatCompletionRequest{Model: "primary"}, ResponseRequest: &request}
	if err := router.affinity.set(t.Context(), affinityKey(req, request.PreviousResponse), router.endpoints[0].Name); err != nil {
		t.Fatal(err)
	}
	if err := router.affinity.set(t.Context(), affinityKey(req, request.PromptCacheOptions.ComparisonResponseID), router.endpoints[1].Name); err != nil {
		t.Fatal(err)
	}
	_, err := router.responseCandidates(t.Context(), req, request, "responses")
	var failure *Error
	if !errors.As(err, &failure) || failure.Param != "prompt_cache_options.comparison_response_id" {
		t.Fatalf("conflicting response references were not rejected: %v", err)
	}
}
