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
