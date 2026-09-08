package provider

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestResponsesSSEPreservesIntegerUsage(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("large integers require a 64-bit platform")
	}
	for _, number := range []string{"9007199254740993", "9223372036854775807"} {
		t.Run(number, func(t *testing.T) {
			document := fmt.Sprintf(`{"id":"r","object":"response","model":"m","status":"completed","usage":{"input_tokens":%s,"output_tokens":0,"total_tokens":%s}}`, number, number)
			expected, err := decodeResponseJSON(strings.NewReader(document))
			if err != nil {
				t.Fatal(err)
			}
			got, err := streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", nil)
			if err != nil || got.Usage.InputTokens != expected.Usage.InputTokens || got.Usage.TotalTokens != expected.Usage.TotalTokens {
				t.Fatalf("usage=%+v expected=%+v err=%v", got.Usage, expected.Usage, err)
			}
		})
	}
}

func TestResponsesSSENumberDecodingKeepsDocumentValidation(t *testing.T) {
	for _, suffix := range []string{` {}`, ` trailing`} {
		callbacks := 0
		_, err := streamResponseData(strings.NewReader(`data: {"type":"response.output_text.delta","delta":"text"}`+suffix+"\n\n"), "m", func(string, string) error { callbacks++; return nil })
		if err == nil || callbacks != 0 {
			t.Fatalf("suffix=%q err=%v callbacks=%d", suffix, err, callbacks)
		}
	}
	for _, index := range []string{"1.0", "1e2"} {
		_, err := streamResponseData(strings.NewReader(`data: {"type":"response.output_item.added","output_index":`+index+`,"item":{"type":"message"}}`+"\n\n"+responseTestTerminal), "m", nil)
		if err != nil {
			t.Fatalf("integer-valued index %s rejected: %v", index, err)
		}
	}
}
