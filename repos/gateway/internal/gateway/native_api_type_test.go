package gateway

import (
	"context"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type nativeAPITypeRecorder struct {
	values []string
}

func (*nativeAPITypeRecorder) Name() string   { return "api-type-recorder" }
func (*nativeAPITypeRecorder) Required() bool { return false }
func (m *nativeAPITypeRecorder) Handle(_ context.Context, request *modules.RequestContext) error {
	m.values = append(m.values, request.Metadata["gateway.api_type"])
	return nil
}

func TestNativeChatSurfacesSetBillingAPITypeBeforePipeline(t *testing.T) {
	response := openai.ChatCompletionResponse{
		ID: "result", Model: "model",
		Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		Usage:   openai.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}
	for _, test := range []struct {
		name, apiType string
		call          func(*nativeAPITypeRecorder, *fallbackChatProvider) int
	}{
		{name: "messages", apiType: "messages", call: func(recorder *nativeAPITypeRecorder, upstream *fallbackChatProvider) int {
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{recorder}), upstream))
			return nativeMessageCall(handler, `{"model":"model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "").Code
		}},
		{name: "generate content", apiType: "generate_content", call: func(recorder *nativeAPITypeRecorder, upstream *fallbackChatProvider) int {
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{recorder}), upstream))
			return generateCall(handler, "/v1beta/models/model:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`, "").Code
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &nativeAPITypeRecorder{}
			upstream := &fallbackChatProvider{response: response}
			if status := test.call(recorder, upstream); status != 200 || upstream.calls != 1 {
				t.Fatalf("status=%d calls=%d", status, upstream.calls)
			}
			if len(recorder.values) != 1 || recorder.values[0] != test.apiType {
				t.Fatalf("pipeline api types=%v", recorder.values)
			}
		})
	}
}
