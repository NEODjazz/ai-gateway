package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponseOutcomeSurvivesJSONAndSyntheticSSE(t *testing.T) {
	for _, tc := range []struct{ name, payload, expected string }{
		{"incomplete", `{"id":"r","object":"response","model":"m","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, `"incomplete_details":{"reason":"max_output_tokens"}`},
		{"failed", `{"id":"r","object":"response","model":"m","status":"failed","error":{"code":"server_error","message":"generation failed"}}`, `"error":{"code":"server_error","message":"generation failed"}`},
		{"refusal", `{"id":"r","object":"response","model":"m","status":"completed","output":[{"id":"msg","type":"message","role":"assistant","content":[{"type":"refusal","refusal":"cannot help"}]}]}`, `"refusal":"cannot help"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response openai.ResponseResponse
			if err := json.Unmarshal([]byte(tc.payload), &response); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(response)
			if err != nil || !strings.Contains(string(body), tc.expected) {
				t.Fatalf("outcome lost in JSON: %s err=%v", body, err)
			}
			var terminal string
			refusalDelta, refusalDone := false, false
			err = synthesizeResponseStream(response, func(kind, payload string) error {
				if kind == "response.created" && (strings.Contains(payload, `"incomplete_details"`) || strings.Contains(payload, `"error"`)) {
					t.Fatalf("terminal metadata on creation: %s", payload)
				}
				if kind == "response."+response.Status {
					terminal = payload
				}
				if kind == "response.refusal.delta" {
					refusalDelta = strings.Contains(payload, `"delta":"cannot help"`)
				}
				if kind == "response.refusal.done" {
					refusalDone = strings.Contains(payload, tc.expected)
				}
				return nil
			})
			if err != nil || !strings.Contains(terminal, tc.expected) {
				t.Fatalf("outcome lost in SSE: %s err=%v", terminal, err)
			}
			if tc.name == "refusal" && (!refusalDelta || !refusalDone) {
				t.Fatalf("refusal events missing: delta=%v done=%v", refusalDelta, refusalDone)
			}
		})
	}
}
