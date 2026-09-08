package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

type statelessUsageRecorder struct {
	ids    []string
	totals []int
}

func (*statelessUsageRecorder) Name() string                                          { return "billing" }
func (*statelessUsageRecorder) Required() bool                                        { return true }
func (*statelessUsageRecorder) Handle(context.Context, *modules.RequestContext) error { return nil }
func (*statelessUsageRecorder) PostResponseEnabled() bool                             { return true }
func (m *statelessUsageRecorder) HandlePostResponse(_ context.Context, r *modules.RequestContext) error {
	m.ids = append(m.ids, r.RequestID)
	m.totals = append(m.totals, r.ResponsesResponse.Usage.TotalTokens)
	return nil
}

func TestResponsesStatelessContinuationThroughGateway(t *testing.T) {
	for _, mode := range []string{"json", "native stream", "fallback stream"} {
		t.Run(mode, func(t *testing.T) {
			const opaque = "opaque1234567890"
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["store"] != false {
					t.Error("store=false lost")
				}
				include, _ := body["include"].([]any)
				if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
					t.Error("include lost")
				}
				if n == 2 {
					items, ok := body["input"].([]any)
					if !ok || len(items) != 2 {
						t.Error("continuation items lost")
						return
					}
					if items[0].(map[string]any)["encrypted_content"] != opaque {
						t.Error("encrypted context changed")
					}
					encoded, _ := json.Marshal(items[1])
					if strings.Contains(string(encoded), "user@example.com") || !strings.Contains(string(encoded), "EMAIL") {
						t.Error("text was not anonymized")
					}
				}
				result := fmt.Sprintf(`{"id":"r%d","status":"completed","model":"m","output":[{"id":"reason","type":"reasoning","encrypted_content":%q,"summary":[]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`, n, opaque)
				if mode == "native stream" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", result)
				} else {
					_, _ = fmt.Fprint(w, result)
				}
			}))
			defer upstream.Close()
			recorder := &statelessUsageRecorder{}
			endpoint := config.ProviderEndpointConfig{Name: "upstream", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"m"}, Stream: mode == "native stream", Capabilities: []string{"responses"}}
			if endpoint.Stream {
				endpoint.Capabilities = append(endpoint.Capabilities, "stream")
			}
			router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{endpoint}, Modules: modules.NewPipeline([]modules.Module{recorder})})
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}}}, modules.NewAnonymizerModule(true, "email")}), router))
			var input any = "hello"
			for step := 0; step < 2; step++ {
				body, _ := json.Marshal(map[string]any{"model": "m", "input": input, "store": false, "include": []string{"reasoning.encrypted_content"}, "stream": mode != "json"})
				request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(body)))
				request.Header.Set("Authorization", "Bearer gateway-test-key")
				request.Header.Set("X-Request-ID", "same-external-id")
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, request)
				if out.Code != 200 {
					t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
				}
				payload := out.Body.Bytes()
				if mode != "json" {
					payload = nil
					for _, line := range strings.Split(out.Body.String(), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						var event struct {
							Type     string          `json:"type"`
							Response json.RawMessage `json:"response"`
						}
						if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil && event.Type == "response.completed" {
							payload = event.Response
						}
					}
				}
				var result struct {
					Output []map[string]any `json:"output"`
				}
				if err := json.Unmarshal(payload, &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Output) != 1 || result.Output[0]["encrypted_content"] != opaque {
					t.Fatal("returned context lost")
				}
				input = []any{result.Output[0], map[string]any{"role": "user", "content": "user@example.com"}}
			}
			if calls.Load() != 2 || len(recorder.ids) != 2 || recorder.ids[0] == "" || recorder.ids[0] == recorder.ids[1] || len(recorder.totals) != 2 || recorder.totals[0] != 5 || recorder.totals[1] != 5 {
				t.Fatalf("calls=%d ids=%v totals=%v", calls.Load(), recorder.ids, recorder.totals)
			}
		})
	}
}
