package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type streamUsageRecorder struct {
	usage openai.Usage
	calls int
}

func (*streamUsageRecorder) Name() string                                          { return "billing" }
func (*streamUsageRecorder) Required() bool                                        { return true }
func (*streamUsageRecorder) Handle(context.Context, *modules.RequestContext) error { return nil }
func (*streamUsageRecorder) PostResponseEnabled() bool                             { return true }
func (m *streamUsageRecorder) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.calls++
	if req.Response != nil {
		m.usage = req.Response.Usage
	}
	return nil
}

func TestRouterDeliversReportedStreamUsageToPostModules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"total_tokens\":120}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	recorder := &streamUsageRecorder{}
	router := Router{health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, modules: modules.NewPipeline([]modules.Module{recorder}), endpoints: []Endpoint{{Name: "upstream", Type: "openai", Models: []string{"model"}, Capabilities: []string{"chat", "stream"}, Provider: NewOpenAICompatible(server.URL, "", true)}}}
	_, streamed, err := router.StreamChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model", Stream: true, Messages: []openai.Message{{Role: "user", Content: "hi"}}}}, func(string) error { return nil })
	if err != nil || !streamed {
		t.Fatalf("stream failed: %v %v", streamed, err)
	}
	if recorder.calls != 1 || recorder.usage.TotalTokens != 120 || recorder.usage.PromptTokens != 100 || recorder.usage.CompletionTokens != 20 {
		t.Fatalf("post-response usage: calls=%d usage=%+v", recorder.calls, recorder.usage)
	}
}
