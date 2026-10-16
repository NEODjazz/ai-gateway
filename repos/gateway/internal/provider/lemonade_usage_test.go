package provider

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestLemonadeUsagePresence(t *testing.T) {
	for _, test := range []struct {
		name, usage string
		invalid     bool
	}{
		{"absent", "", false}, {"null", "null", false},
		{"zero", `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, false},
		{"details", `{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":1}}`, false},
		{"empty", `{}`, true}, {"partial", `{"prompt_tokens":2,"total_tokens":2}`, true},
		{"negative", `{"prompt_tokens":-1,"completion_tokens":1,"total_tokens":0}`, true},
		{"inconsistent", `{"prompt_tokens":2,"completion_tokens":1,"total_tokens":2}`, true},
		{"overflow", fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":1,"total_tokens":%d}`, math.MaxInt, math.MinInt), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := `{}`
			if test.usage != "" {
				payload = `{"usage":` + test.usage + `}`
			}
			if err := validateLemonadeTokenUsage([]byte(payload), false); (err != nil) != test.invalid {
				t.Fatalf("usage error=%v", err)
			}
			payload = strings.ReplaceAll(strings.ReplaceAll(payload, "prompt_tokens", "input_tokens"), "completion_tokens", "output_tokens")
			if err := validateLemonadeTokenUsage([]byte(payload), true); (err != nil) != test.invalid {
				t.Fatalf("Responses usage error=%v", err)
			}
		})
	}
}

func TestLemonadeRejectsIncompleteUsageBeforeDelivery(t *testing.T) {
	for _, operation := range []string{"chat/completions", "responses", "completions"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", operation, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					payload := `{"id":"test","object":"chat.completion","model":"native","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"total_tokens":1}}`
					if operation == "completions" {
						payload = `{"id":"test","object":"text_completion","created":1,"model":"native","choices":[{"index":0,"text":"answer","finish_reason":"stop"}],"usage":{"prompt_tokens":1,"total_tokens":1}}`
					}
					if operation == "responses" {
						payload = strings.Replace(lemonadeResponse, `"input_tokens":3,"output_tokens":2,"total_tokens":5`, `"input_tokens":3`, 1)
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						if operation == "responses" {
							fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", payload)
						} else {
							fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
						}
					} else {
						fmt.Fprint(w, payload)
					}
				}))
				t.Cleanup(server.Close)
				client := NewLemonade(server.URL, "", true)
				delivered := false
				chatWrite := func(string) error { delivered = true; return nil }
				var err error
				switch operation {
				case "chat/completions":
					request := openai.ChatCompletionRequest{Model: "native", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
					if stream {
						_, err = client.StreamChatCompletions(t.Context(), request, chatWrite)
					} else {
						_, err = client.ChatCompletions(t.Context(), request)
					}
				case "completions":
					request := openai.CompletionRequest{Model: "native", Prompt: "hello"}
					if stream {
						_, err = client.StreamCompletions(t.Context(), request, chatWrite)
					} else {
						_, err = client.Completions(t.Context(), request)
					}
				case "responses":
					request := openai.ResponseRequest{Model: "native", Input: "hello"}
					if stream {
						_, err = client.StreamResponses(t.Context(), request, func(string, string) error { delivered = true; return nil })
					} else {
						_, err = client.Responses(t.Context(), request)
					}
				}
				if err == nil || !strings.Contains(err.Error(), "incomplete token usage") || delivered {
					t.Fatalf("invalid usage accepted or delivered: err=%v delivered=%t", err, delivered)
				}
			})
		}
	}
}

func TestLemonadeRerankUsagePresence(t *testing.T) {
	for _, test := range []struct {
		usage             string
		reported, invalid bool
	}{
		{"", false, false}, {`,"usage":{"prompt_tokens":0,"total_tokens":0}`, true, false},
		{`,"usage":{"prompt_tokens":7,"total_tokens":7}`, true, false},
		{`,"usage":{"prompt_tokens":7}`, false, true}, {`,"usage":{"prompt_tokens":7,"total_tokens":8}`, false, true},
	} {
		response, err := decodeLemonadeRerank(strings.NewReader(`{"results":[{"index":0,"relevance_score":0.9}]` + test.usage + `}`))
		if (err != nil) != test.invalid || response.UsageReported != test.reported {
			t.Fatalf("reported=%t err=%v", response.UsageReported, err)
		}
	}
}
