package provider

import (
	"context"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type estimatedUsageProvider struct{ total int }

func (p estimatedUsageProvider) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{
		ID: "chat-usage", Object: "chat.completion", Model: "m",
		Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		Usage:   openai.Usage{CompletionTokens: p.total, TotalTokens: p.total},
	}, nil
}

func (estimatedUsageProvider) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}

func (p estimatedUsageProvider) Embeddings(context.Context, openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	return openai.EmbeddingResponse{
		Object: "list", Model: "m", Data: []openai.Embedding{{Object: "embedding", Embedding: []float64{1}, Index: 0}},
		Usage: openai.Usage{TotalTokens: p.total},
	}, nil
}

type estimatedUsageBilling struct{ commits, failures int }

func (*estimatedUsageBilling) Name() string              { return "billing" }
func (*estimatedUsageBilling) Required() bool            { return true }
func (*estimatedUsageBilling) PostResponseEnabled() bool { return true }
func (*estimatedUsageBilling) Handle(_ context.Context, req *modules.RequestContext) error {
	req.Usage = &openai.Usage{PromptTokens: 1}
	return nil
}
func (m *estimatedUsageBilling) HandlePostResponse(context.Context, *modules.RequestContext) error {
	m.commits++
	return nil
}
func (m *estimatedUsageBilling) HandleFailure(context.Context, *modules.RequestContext, error) error {
	m.failures++
	return nil
}

func TestEstimatedUsageMergesDoNotOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	t.Run("chat", func(t *testing.T) {
		chat := openai.ChatCompletionResponse{Usage: openai.Usage{CompletionTokens: maxInt, TotalTokens: maxInt}}
		if err := mergeChatUsage(&chat, &openai.Usage{PromptTokens: 1}); err == nil || chat.Usage.PromptTokens != 0 || chat.Usage.TotalTokens != maxInt {
			t.Fatalf("overflowing chat estimate: usage=%+v err=%v", chat.Usage, err)
		}
	})

	t.Run("embedding", func(t *testing.T) {
		embedding := openai.EmbeddingResponse{Usage: openai.Usage{TotalTokens: maxInt}}
		if err := mergeEmbeddingUsage(&embedding, &openai.Usage{PromptTokens: 1}); err == nil || embedding.Usage.PromptTokens != 0 || embedding.Usage.TotalTokens != maxInt {
			t.Fatalf("overflowing embedding estimate: usage=%+v err=%v", embedding.Usage, err)
		}
	})
	for _, total := range []int{0, maxInt - 1} {
		chat := openai.ChatCompletionResponse{Usage: openai.Usage{TotalTokens: total}}
		if err := mergeChatUsage(&chat, &openai.Usage{PromptTokens: 1}); err != nil || chat.Usage.TotalTokens != total+1 {
			t.Fatalf("valid chat boundary: usage=%+v err=%v", chat.Usage, err)
		}
		embedding := openai.EmbeddingResponse{Usage: openai.Usage{TotalTokens: total}}
		if err := mergeEmbeddingUsage(&embedding, &openai.Usage{PromptTokens: 1}); err != nil || embedding.Usage.TotalTokens != total+1 {
			t.Fatalf("valid embedding boundary: usage=%+v err=%v", embedding.Usage, err)
		}
	}
}

func TestOverflowingEstimatedUsageDoesNotCommitBilling(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, operation := range []string{"chat", "embeddings"} {
		t.Run(operation, func(t *testing.T) {
			billing := &estimatedUsageBilling{}
			client := estimatedUsageProvider{total: maxInt}
			router := Router{
				endpoints: []Endpoint{{Name: "usage", Type: "demo", Capabilities: []string{"chat", "embeddings"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}},
				modules:   modules.NewPipeline([]modules.Module{billing}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
			}
			var err error
			if operation == "chat" {
				_, err = router.ChatCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "hello"}}}})
			} else {
				request := openai.EmbeddingRequest{Model: "m", Input: "hello"}
				_, err = router.Embeddings(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "m"}, EmbeddingRequest: &request})
			}
			if err == nil || billing.commits != 0 || billing.failures != 1 {
				t.Fatalf("err=%v billing commits=%d failures=%d", err, billing.commits, billing.failures)
			}
		})
	}
}
