package provider

import (
	"fmt"
	"strings"
	"testing"
)

func TestResponseStreamRejectsInvalidOutputIndices(t *testing.T) {
	for _, index := range []string{"-1", "1.5", `"2"`, "null", "1024", "1e100", "9223372036854775807"} {
		for _, kind := range []string{"response.function_call_arguments.delta", "response.output_item.added"} {
			t.Run(kind+"/"+index, func(t *testing.T) {
				payload := fmt.Sprintf(`{"type":%q,"output_index":%s,"delta":"{}","item":{"type":"function_call"}}`, kind, index)
				calls := 0
				_, err := streamResponseData(strings.NewReader("event: "+kind+"\ndata: "+payload+"\n\n"+responseTestTerminal), "m", func(string, string) error { calls++; return nil })
				if err == nil || calls != 0 {
					t.Fatalf("invalid index accepted: err=%v callbacks=%d", err, calls)
				}
			})
		}
	}
}

func TestResponseStreamAcceptsBoundedAndOmittedIndices(t *testing.T) {
	for _, tc := range []struct {
		field string
		index int
	}{{"", 0}, {`"output_index":0,`, 0}, {`"output_index":1023,`, 1023}} {
		payload := `{` + tc.field + `"item":{"type":"function_call","id":"item","call_id":"call","name":"lookup","arguments":"{}"}}`
		response, err := streamResponseData(strings.NewReader("event: response.output_item.done\ndata: "+payload+"\n\n"+responseTestTerminal), "m", nil)
		if err != nil || len(response.Output) != tc.index+1 || response.Output[tc.index].ID != "item" {
			t.Fatalf("index=%d output=%d err=%v", tc.index, len(response.Output), err)
		}
	}
}
