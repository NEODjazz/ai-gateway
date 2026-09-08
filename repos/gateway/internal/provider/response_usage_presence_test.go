package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponsesPreservesReportedZeroInputUsage(t *testing.T) {
	for _, tc := range []struct {
		name, field  string
		input, total int
	}{
		{"missing", "", 11, 11},
		{"null", `,"usage":null`, 11, 11},
		{"output only", `,"usage":{"output_tokens":2,"total_tokens":2}`, 11, 13},
		{"zero", `,"usage":{"input_tokens":0,"output_tokens":2,"total_tokens":2}`, 0, 2},
		{"positive", `,"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}`, 7, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed"` + tc.field + `}`
			for _, stream := range []bool{false, true} {
				var response openai.ResponseResponse
				var err error
				if stream {
					response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", nil)
				} else {
					response, err = decodeResponseJSON(strings.NewReader(document))
				}
				if err != nil {
					t.Fatal(err)
				}
				mergeResponseUsage(&response, &openai.Usage{PromptTokens: 11})
				if response.Usage.InputTokens != tc.input || response.Usage.TotalTokens != tc.total {
					t.Fatalf("stream=%v usage=%+v want input=%d total=%d", stream, response.Usage, tc.input, tc.total)
				}
			}
		})
	}
}

func TestResponsesUsagePresenceSurvivesPartialSnapshots(t *testing.T) {
	wire := `data: {"type":"response.in_progress","response":{"id":"r","usage":{"input_tokens":0,"output_tokens":1,"total_tokens":1}}}` + "\n\n" + `data: {"type":"response.completed","response":{"id":"r","status":"completed"}}` + "\n\n"
	response, err := streamResponseData(strings.NewReader(wire), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	mergeResponseUsage(&response, &openai.Usage{PromptTokens: 11})
	if response.Usage.InputTokens != 0 || response.Usage.TotalTokens != 1 || !response.InputTokensReported {
		t.Fatalf("reported zero lost: %+v", response)
	}
	payload, err := json.Marshal(response)
	if err != nil || strings.Contains(string(payload), "InputTokensReported") || strings.Contains(string(payload), "input_tokens_reported") {
		t.Fatalf("internal field exposed: %s err=%v", payload, err)
	}
}
