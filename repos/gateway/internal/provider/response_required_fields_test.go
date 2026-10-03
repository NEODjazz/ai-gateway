package provider

import (
	"fmt"
	"strings"
	"testing"
)

func TestResponseStreamRequiresTypedEventTextBeforeDelivery(t *testing.T) {
	events := map[string]string{
		"response.output_text.delta":             "delta",
		"response.output_text.done":              "text",
		"response.refusal.delta":                 "delta",
		"response.refusal.done":                  "refusal",
		"response.function_call_arguments.delta": "delta",
		"response.function_call_arguments.done":  "arguments",
		"response.custom_tool_call_input.delta":  "delta",
		"response.custom_tool_call_input.done":   "input",
		"response.reasoning_summary_text.delta":  "delta",
		"response.reasoning_summary_text.done":   "text",
	}
	for event, field := range events {
		for _, malformed := range []string{"", fmt.Sprintf(",%q:null", field), fmt.Sprintf(",%q:1", field)} {
			t.Run(event+malformed, func(t *testing.T) {
				wire := fmt.Sprintf("data: {\"type\":%q%s}\n\n", event, malformed)
				callbacks := 0
				_, err := streamResponseData(strings.NewReader(wire+responseTestTerminal), "m", func(string, string) error { callbacks++; return nil })
				if err == nil || callbacks != 0 {
					t.Fatalf("malformed event delivered: event=%s field=%s err=%v callbacks=%d", event, field, err, callbacks)
				}
			})
		}
	}
}

func TestResponseStreamAllowsEmptyRequiredEventText(t *testing.T) {
	events := []struct {
		event, field, suffix string
	}{
		{"response.output_text.delta", "delta", ""},
		{"response.output_text.done", "text", ""},
		{"response.refusal.delta", "delta", ""},
		{"response.refusal.done", "refusal", ""},
		{"response.function_call_arguments.delta", "delta", `data: {"type":"response.function_call_arguments.done","arguments":"{}"}` + "\n\n"},
		{"response.custom_tool_call_input.delta", "delta", ""},
		{"response.custom_tool_call_input.done", "input", ""},
		{"response.reasoning_summary_text.delta", "delta", ""},
		{"response.reasoning_summary_text.done", "text", ""},
	}
	for _, test := range events {
		wire := fmt.Sprintf("data: {\"type\":%q,%q:\"\"}\n\n", test.event, test.field) + test.suffix
		if _, err := streamResponseData(strings.NewReader(wire+responseTestTerminal), "m", nil); err != nil {
			t.Fatalf("empty string rejected: event=%s field=%s err=%v", test.event, test.field, err)
		}
	}
}

func TestResponseFunctionArgumentsDoneRequiresJSONObjectBeforeDelivery(t *testing.T) {
	for _, arguments := range []string{"", "[]", "null", "broken"} {
		wire := fmt.Sprintf("data: {\"type\":\"response.function_call_arguments.done\",\"arguments\":%q}\n\n", arguments)
		callbacks := 0
		_, err := streamResponseData(strings.NewReader(wire+responseTestTerminal), "m", func(string, string) error { callbacks++; return nil })
		if err == nil || callbacks != 0 {
			t.Fatalf("invalid final arguments delivered: arguments=%q err=%v callbacks=%d", arguments, err, callbacks)
		}
	}
}
