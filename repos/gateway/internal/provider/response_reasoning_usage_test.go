package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponsesReasoningUsagePreserved(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, count := range []int{0, 3, -1} {
			t.Run(fmt.Sprintf("%v/%d", stream, count), func(t *testing.T) {
				body := fmt.Sprintf(`{"id":"r","status":"completed","usage":{"input_tokens":2,"output_tokens":5,"total_tokens":7,"output_tokens_details":{"reasoning_tokens":%d}}}`, count)
				var response openai.ResponseResponse
				var err error
				calls := 0
				if stream {
					response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+body+"}\n\n"), "m", func(string, string) error { calls++; return nil })
				} else {
					response, err = decodeResponseJSON(strings.NewReader(body))
				}
				if count < 0 {
					if err == nil || calls != 0 {
						t.Fatalf("negative usage accepted: err=%v calls=%d", err, calls)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				var roundTrip struct {
					Usage struct {
						Details *struct {
							Reasoning int `json:"reasoning_tokens"`
						} `json:"output_tokens_details"`
					}
				}
				if err := json.Unmarshal(encoded, &roundTrip); err != nil {
					t.Fatal(err)
				}
				if roundTrip.Usage.Details == nil || roundTrip.Usage.Details.Reasoning != count {
					t.Fatal("reasoning usage detail lost")
				}
				if response.Usage.OutputTokens != 5 || response.Usage.TotalTokens != 7 {
					t.Fatalf("usage double counted: %+v", response.Usage)
				}
			})
		}
	}
}

func TestResponsesOutputCachedUsagePreserved(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			body := `{"id":"r","status":"completed","usage":{"input_tokens":2,"output_tokens":5,"total_tokens":7,"output_tokens_details":{"cached_tokens":3}}}`
			var response openai.ResponseResponse
			var err error
			calls := 0
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+body+"}\n\n"), "m", func(string, string) error { calls++; return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(body))
			}
			if err != nil || response.Usage.OutputTokensDetails == nil || response.Usage.OutputTokensDetails.CachedTokens != 3 {
				t.Fatalf("response=%+v calls=%d err=%v", response, calls, err)
			}
			encoded, err := json.Marshal(response)
			if err != nil || !strings.Contains(string(encoded), `"cached_tokens":3`) {
				t.Fatalf("encoded=%s err=%v", encoded, err)
			}
			if response.Usage.OutputTokens != 5 || response.Usage.TotalTokens != 7 {
				t.Fatalf("cached detail changed totals: %+v", response.Usage)
			}
		})
	}
}

func TestResponsesInputReasoningUsagePreserved(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			body := `{"id":"r","status":"completed","usage":{"input_tokens":4,"input_tokens_details":{"reasoning_tokens":2},"output_tokens":3,"total_tokens":7}}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+body+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(body))
			}
			if err != nil || response.Usage.InputTokensDetails == nil || response.Usage.InputTokensDetails.ReasoningTokens != 2 {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			if err != nil || !strings.Contains(string(encoded), `"reasoning_tokens":2`) {
				t.Fatalf("encoded=%s err=%v", encoded, err)
			}
			if response.Usage.InputTokens != 4 || response.Usage.TotalTokens != 7 {
				t.Fatalf("reasoning detail changed totals: %+v", response.Usage)
			}
		})
	}
}

func TestResponsesProviderUsageCountersPreserved(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, count := range []int{0, 3} {
			t.Run(fmt.Sprintf("stream=%v/count=%d", stream, count), func(t *testing.T) {
				body := fmt.Sprintf(`{"id":"r","status":"completed","usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7,"num_sources_used":%d,"num_server_side_tools_used":%d}}`, count, count)
				var response openai.ResponseResponse
				var err error
				if stream {
					response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+body+"}\n\n"), "m", func(string, string) error { return nil })
				} else {
					response, err = decodeResponseJSON(strings.NewReader(body))
				}
				if err != nil || response.Usage.NumSourcesUsed == nil || *response.Usage.NumSourcesUsed != count || response.Usage.NumServerSideToolsUsed == nil || *response.Usage.NumServerSideToolsUsed != count {
					t.Fatalf("response=%+v err=%v", response, err)
				}
				encoded, err := json.Marshal(response)
				if err != nil || !strings.Contains(string(encoded), fmt.Sprintf(`"num_sources_used":%d`, count)) || !strings.Contains(string(encoded), fmt.Sprintf(`"num_server_side_tools_used":%d`, count)) {
					t.Fatalf("encoded=%s err=%v", encoded, err)
				}
				if response.Usage.InputTokens != 4 || response.Usage.OutputTokens != 3 || response.Usage.TotalTokens != 7 {
					t.Fatalf("provider counters changed token totals: %+v", response.Usage)
				}
			})
		}
	}
}
