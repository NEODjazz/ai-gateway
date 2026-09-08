package provider

import (
	"ai-gateway-gateway/internal/modules"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponsesStreamRequiresTerminalOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"empty", "", false},
		{"created only", "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\"}}\n\n", false},
		{"partial text", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", false},
		{"done without outcome", "data: [DONE]\n\n", false},
		{"completed", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\"}}\n\n", true},
		{"incomplete", "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"r\",\"status\":\"incomplete\"}}\n\n", true},
		{"failed", "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"r\",\"status\":\"failed\"}}\n\n", true},
		{"missing response", "data: {\"type\":\"response.completed\"}\n\n", false},
		{"contradictory status", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\"}}\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client := NewOpenAICompatible(server.URL, "", true)
			recorder := &responseEstimateRecorder{}
			router := Router{endpoints: []Endpoint{{Name: "upstream", Provider: client}}, modules: modules.NewPipeline([]modules.Module{recorder}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}}
			request := openai.ResponseRequest{Model: "m", Input: "hello"}
			_, _, err := router.StreamResponses(t.Context(), modules.RequestContext{CredentialID: "tenant", Request: openai.ChatCompletionRequest{Model: "m"}, ResponseRequest: &request}, func(string, string) error { return nil })
			if tc.valid {
				if len(recorder.usage) != 1 || recorder.failures != 0 {
					t.Fatalf("valid lifecycle: usage=%v failures=%d", recorder.usage, recorder.failures)
				}
			} else if len(recorder.usage) != 0 || recorder.failures != 1 {
				t.Fatalf("invalid lifecycle: usage=%v failures=%d", recorder.usage, recorder.failures)
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

const responseTestTerminal = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
