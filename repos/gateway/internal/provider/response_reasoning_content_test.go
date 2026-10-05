package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

const responseReasoningItem = `{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"Consider greeting"}],"summary":[{"type":"summary_text","text":"Greeting summary"}],"encrypted_content":"opaque"}`

func TestLemonadeResponsesPreserveReasoningContent(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, answer := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/answer=%t", stream, answer), func(t *testing.T) {
				output := responseReasoningItem
				if answer {
					output += `,{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}`
				}
				document := `{"id":"resp_1","object":"response","model":"native","status":"completed","output":[` + output + `],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/responses" {
						t.Errorf("path=%s", r.URL.Path)
					}
					if !stream {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, document)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, payload := range []string{
						`{"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
						`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[]}}`,
						`{"type":"response.reasoning_text.delta","output_index":0,"content_index":0,"item_id":"rs_1","delta":"Consider greeting"}`,
						`{"type":"response.output_item.done","output_index":0,"item":` + responseReasoningItem + `}`,
						`{"type":"response.completed","response":` + document + `}`,
					} {
						fmt.Fprintf(w, "data: %s\n\n", payload)
					}
				}))
				t.Cleanup(server.Close)
				client := NewLemonade(server.URL, "", true)
				request := openai.ResponseRequest{Model: "native", Input: "hello", Stream: stream}
				var response openai.ResponseResponse
				var err error
				var events []string
				if stream {
					response, err = client.StreamResponses(t.Context(), request, func(kind, _ string) error { events = append(events, kind); return nil })
				} else {
					response, err = client.Responses(t.Context(), request)
				}
				if err != nil {
					t.Fatal(err)
				}
				wantText := ""
				if answer {
					wantText = "Hello"
				}
				if response.OutputText != wantText || response.Usage.TotalTokens != 5 || !response.InputTokensReported || !response.OutputTokensReported {
					t.Fatalf("text=%q usage=%+v", response.OutputText, response.Usage)
				}
				got, _ := json.Marshal(response.Output[0])
				var original, retained any
				if err := json.Unmarshal([]byte(responseReasoningItem), &original); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(got, &retained); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(original, retained) {
					t.Fatalf("reasoning changed: %s", got)
				}
				if stream && len(events) != 5 {
					t.Fatalf("events=%v", events)
				}
			})
		}
	}
}

func TestResponseReasoningDeltasAndSummaryRemainSeparate(t *testing.T) {
	wire := ""
	for _, payload := range []string{
		`{"type":"response.reasoning_text.delta","output_index":0,"content_index":0,"delta":"partial"}`,
		`{"type":"response.reasoning_text.done","output_index":0,"content_index":0,"text":"Consider greeting"}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"Summary"}`,
		`{"type":"response.reasoning_text.delta","output_index":0,"content_index":1,"delta":"Second thought"}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"Hello"}`,
	} {
		wire += "data: " + payload + "\n\n"
	}
	response, err := streamResponseData(strings.NewReader(wire+responseTestTerminal), "native", nil)
	if err != nil {
		t.Fatal(err)
	}
	item := response.Output[0]
	if response.OutputText != "Hello" || item.Type != "reasoning" || len(item.Content) != 2 || item.Content[0].Text != "Consider greeting" || item.Content[1].Text != "Second thought" || item.Summary[0].Text != "Summary" {
		t.Fatalf("response=%+v", response)
	}
}

func TestResponsesRejectInvalidReasoningContentBeforeDelivery(t *testing.T) {
	for _, item := range []string{
		`{"type":"reasoning","content":[{}]}`,
		`{"type":"reasoning","content":[{"type":"output_text","text":"bad"}]}`,
		`{"type":"reasoning","content":[{"type":"summary_text","text":"bad"}]}`,
		`{"type":"message","content":[{"type":"reasoning_text","text":"bad"}]}`,
		`{"type":"reasoning","summary":[{"type":"reasoning_text","text":"bad"}]}`,
		`{"type":"reasoning","content":[{"type":"reasoning_text","refusal":"bad"}]}`,
		`{"type":"reasoning","content":[{"type":"reasoning_text","annotations":[null]}]}`,
		`{"type":"reasoning","content":[{"type":"reasoning_text","logprobs":[{"token":"x","logprob":-1}]}]}`,
	} {
		document := `{"id":"r","status":"completed","output":[` + item + `]}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid JSON accepted: %s", item)
		}
		calls := 0
		_, err := streamResponseData(strings.NewReader(`data: {"type":"response.completed","response":`+document+"}\n\n"), "native", func(string, string) error { calls++; return nil })
		if err == nil || calls != 0 {
			t.Fatalf("invalid SSE accepted: err=%v calls=%d", err, calls)
		}
	}
}

func TestResponseReasoningEventValidation(t *testing.T) {
	for _, kind := range []string{"response.reasoning_text.delta", "response.reasoning_text.done"} {
		for _, fields := range []string{`"content_index":-1`, `"content_index":128`, `"content_index":0.5`, `"content_index":null`, `"content_index":"0"`, fmt.Sprintf(`"output_index":%d`, maxResponseStreamOutputItems), `"output_index":-1`, `"item_id":"bad/id"`, `"delta":null,"text":null`} {
			// Append fields after defaults so the invalid value is decoded last.
			wire := fmt.Sprintf("data: {\"type\":%q,\"delta\":\"x\",\"text\":\"x\",%s}\n\n", kind, fields)
			calls := 0
			_, err := streamResponseData(strings.NewReader(wire+responseTestTerminal), "native", func(string, string) error { calls++; return nil })
			if err == nil || calls != 0 {
				t.Fatalf("kind=%s fields=%s err=%v calls=%d", kind, fields, err, calls)
			}
		}
	}
	wire := "data: " + `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}` + "\n\n" + "data: " + `{"type":"response.reasoning_text.delta","output_index":0,"item_id":"msg_1","delta":"bad"}` + "\n\n"
	calls := 0
	_, err := streamResponseData(strings.NewReader(wire+responseTestTerminal), "native", func(string, string) error { calls++; return nil })
	if err == nil || calls != 1 {
		t.Fatalf("reasoning delivered on message: err=%v calls=%d", err, calls)
	}
}
