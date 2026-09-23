package provider

import (
	"strings"
	"testing"
)

func TestResponsesValidateSSEEventTypeBeforeDelivery(t *testing.T) {
	invalid := map[string]string{
		"contradictory header": "event: response.output_text.delta\n" + `data: {"type":"response.completed","delta":"answer"}` + "\n\n",
		"non-string type":      `data: {"type":1}` + "\n\n",
		"empty type":           `data: {"type":""}` + "\n\n",
		"missing type":         `data: {"delta":"answer"}` + "\n\n",
	}
	for name, wire := range invalid {
		t.Run(name, func(t *testing.T) {
			callbacks := 0
			if _, err := streamResponseData(strings.NewReader(wire+responseTestTerminal), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
				t.Fatalf("invalid event delivered: err=%v callbacks=%d", err, callbacks)
			}
		})
	}
}

func TestResponsesAcceptMatchingOrSingleSSEEventType(t *testing.T) {
	for name, event := range map[string]string{
		"matching header": "event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","delta":"answer"}` + "\n\n",
		"payload only":    `data: {"type":"response.output_text.delta","delta":"answer"}` + "\n\n",
		"header only":     "event: response.output_text.delta\n" + `data: {"delta":"answer"}` + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			response, err := streamResponseData(strings.NewReader(event+responseTestTerminal), "m", nil)
			if err != nil || response.OutputText != "answer" {
				t.Fatalf("valid event rejected: response=%+v err=%v", response, err)
			}
		})
	}
}
