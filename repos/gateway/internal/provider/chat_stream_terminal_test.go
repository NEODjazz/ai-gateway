package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type chatStreamBillingRecorder struct {
	streamUsageRecorder
	failures int
}

func (m *chatStreamBillingRecorder) HandleFailure(context.Context, *modules.RequestContext, error) error {
	m.failures++
	return nil
}

func TestChatStreamRequiresTerminalOutcome(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		valid bool
	}{
		{name: "empty"},
		{name: "partial content", body: `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\n"},
		{name: "partial content and usage", body: `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\n" + `data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n"},
		{name: "done", body: `data: {"choices":[{"index":0,"delta":{"content":"complete"}}]}` + "\n\n" + "data: [DONE]\n\n", valid: true},
		{name: "finish reason without done", body: `data: {"choices":[{"index":0,"delta":{"content":"complete"},"finish_reason":"stop"}]}` + "\n\n", valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := streamChatCompletionData(strings.NewReader(test.body), "model", nil)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t err=%v", test.valid, err)
			}
		})
	}
}

func TestTruncatedChatStreamDoesNotCommitBilling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}`+"\n\n")
	}))
	t.Cleanup(server.Close)
	recorder := &chatStreamBillingRecorder{}
	router := Router{
		health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, modules: modules.NewPipeline([]modules.Module{recorder}),
		endpoints: []Endpoint{{Name: "upstream", Type: "openai", Models: []string{"model"}, Capabilities: []string{"chat", "stream"}, Provider: NewOpenAICompatible(server.URL, "", true)}},
	}
	_, streamed, err := router.StreamChatCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}, func(string) error { return nil })
	if err == nil || !streamed || recorder.calls != 0 || recorder.failures != 1 {
		t.Fatalf("truncated stream: streamed=%t err=%v committed=%d failures=%d", streamed, err, recorder.calls, recorder.failures)
	}
}
