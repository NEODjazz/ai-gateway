package provider

import (
	"ai-gateway-gateway/internal/openai"
	"fmt"
	"strings"
	"testing"
)

func TestResponsesRejectsInvalidUsageBeforeDelivery(t *testing.T) {
	for _, usage := range []string{
		`{"input_tokens":-1,"output_tokens":1,"total_tokens":0}`,
		`{"input_tokens":1,"output_tokens":-1,"total_tokens":0}`,
		`{"total_tokens":-1}`,
		fmt.Sprintf(`{"input_tokens":%d,"output_tokens":1}`, int(^uint(0)>>1)),
		`{"input_tokens":1,"input_tokens_details":{"cached_tokens":-1}}`,
		`{"input_tokens":1,"input_tokens_details":{"cache_write_tokens":-1}}`,
		`{"input_tokens":1,"input_tokens_details":{"cache_creation_tokens":-1}}`,
		`{"input_tokens":1,"input_tokens_details":{"audio_tokens":-1}}`,
		`{"input_tokens":1,"input_tokens_details":{"image_tokens":-1}}`,
		`{"input_tokens":1,"input_tokens_details":{"reasoning_tokens":-1}}`,
		`{"input_tokens":1,"input_tokens_details":{"text_tokens":-1}}`,
		`{"output_tokens":1,"output_tokens_details":{"accepted_prediction_tokens":-1}}`,
		`{"output_tokens":1,"output_tokens_details":{"audio_tokens":-1}}`,
		`{"output_tokens":1,"output_tokens_details":{"cached_tokens":-1}}`,
		`{"output_tokens":1,"output_tokens_details":{"rejected_prediction_tokens":-1}}`,
		`{"output_tokens":1,"output_tokens_details":{"text_tokens":-1}}`,
	} {
		t.Run(usage, func(t *testing.T) {
			document := fmt.Sprintf(`{"id":"r","object":"response","model":"m","status":"completed","usage":%s}`, usage)
			if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
				t.Fatal("invalid JSON usage accepted")
			}
			callbacks := 0
			wire := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
			if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
				t.Fatalf("invalid SSE usage delivered: err=%v callbacks=%d", err, callbacks)
			}
		})
	}
}

func TestResponseUsageRangeBoundary(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, usage := range []openai.ResponseUsage{{}, {InputTokens: maxInt - 1, OutputTokens: 1, TotalTokens: maxInt}, {InputTokens: 7, OutputTokens: 2, TotalTokens: 9}, {InputTokens: 7}} {
		if err := validateResponseUsage(usage); err != nil {
			t.Fatalf("valid usage rejected: %+v: %v", usage, err)
		}
	}
	if err := validateResponseUsage(openai.ResponseUsage{InputTokens: maxInt, OutputTokens: 1}); err == nil {
		t.Fatal("overflowing token sum accepted")
	}
}
