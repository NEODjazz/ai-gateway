package modules

import (
	"context"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

type BillingModule struct {
	required bool
}

func NewBillingModule(required bool) BillingModule {
	return BillingModule{required: required}
}

func (m BillingModule) Name() string {
	return "billing"
}

func (m BillingModule) Required() bool {
	return m.required
}

func (m BillingModule) PostResponseEnabled() bool {
	return true
}

func (m BillingModule) HandlePostResponse(ctx context.Context, req *RequestContext) error {
	return m.Handle(ctx, req)
}

func (m BillingModule) Handle(_ context.Context, req *RequestContext) error {
	promptTokens := estimateRequestTokens(req)

	req.Usage = &openai.Usage{
		PromptTokens: promptTokens,
		TotalTokens:  promptTokens,
	}
	if req.EmbeddingResponse != nil {
		usage := req.EmbeddingResponse.Usage
		req.Usage = &usage
	}

	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["billing.prompt_tokens_estimated"] = strconv.Itoa(promptTokens)
	return nil
}

func textFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, textFromAny(item))
		}
		return strings.Join(parts, " ")
	case map[string]any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, textFromAny(item))
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func estimateTokens(value string) int {
	words := strings.Fields(value)
	if len(words) == 0 {
		return 0
	}
	return len(words)
}
