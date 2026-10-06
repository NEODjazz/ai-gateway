package provider

import (
	"strings"
	"testing"
)

func TestResponsesRejectInvalidSequenceNumbersBeforeDelivery(t *testing.T) {
	for name, sequence := range map[string]string{
		"negative":   `-1`,
		"fractional": `1.5`,
		"string":     `"1"`,
		"overflow":   `9223372036854775808`,
	} {
		t.Run(name, func(t *testing.T) {
			wire := `data: {"type":"response.output_text.delta","sequence_number":` + sequence + `,"delta":"A"}` + "\n\n" + responseTestTerminal
			callbacks := 0
			if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
				t.Fatalf("invalid sequence delivered: sequence=%s err=%v callbacks=%d", sequence, err, callbacks)
			}
		})
	}
}

func TestResponsesRejectNonIncreasingSequenceNumbers(t *testing.T) {
	for name, second := range map[string]string{"duplicate": "1", "decreasing": "0"} {
		t.Run(name, func(t *testing.T) {
			wire := `data: {"type":"response.output_text.delta","sequence_number":1,"delta":"A"}` + "\n\n" +
				`data: {"type":"response.output_text.delta","sequence_number":` + second + `,"delta":"B"}` + "\n\n" + responseTestTerminal
			callbacks := 0
			_, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil })
			if err == nil || callbacks != 1 {
				t.Fatalf("non-increasing sequence accepted: err=%v callbacks=%d", err, callbacks)
			}
		})
	}
}

func TestResponsesAcceptIncreasingOptionalSequenceNumbers(t *testing.T) {
	wire := `data: {"type":"response.output_text.delta","sequence_number":1,"delta":"A"}` + "\n\n" +
		`data: {"type":"response.output_text.delta","delta":"B"}` + "\n\n" +
		`data: {"type":"response.output_text.delta","sequence_number":3,"delta":"C"}` + "\n\n" + responseTestTerminal
	response, err := streamResponseData(strings.NewReader(wire), "m", nil)
	if err != nil || response.OutputText != "ABC" {
		t.Fatalf("valid sequence rejected: response=%+v err=%v", response, err)
	}
}
