package provider

import (
	"fmt"
	"strings"
	"testing"
)

func TestNativeResponseRefusalDoesNotBecomeText(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(fmt.Sprint(named), func(t *testing.T) {
			var stream strings.Builder
			for _, event := range []struct{ kind, data string }{
				{"response.output_text.delta", `"delta":"answer"`},
				{"response.refusal.delta", `"output_index":1,"content_index":2,"item_id":"refusal-item","delta":"cannot "`},
				{"response.refusal.delta", `"output_index":1,"content_index":2,"item_id":"refusal-item","delta":"help"`},
				{"response.refusal.done", `"output_index":1,"content_index":2,"item_id":"refusal-item","refusal":"cannot help"`},
				{"response.reasoning_summary_text.delta", `"output_index":3,"delta":"summary"`},
				{"response.function_call_arguments.delta", `"output_index":2,"delta":"{}"`},
			} {
				if named {
					fmt.Fprintf(&stream, "event: %s\n", event.kind)
				}
				fmt.Fprintf(&stream, "data: {\"type\":%q,%s}\n\n", event.kind, event.data)
			}
			forwarded := 0
			response, err := streamResponseData(strings.NewReader(stream.String()+responseTestTerminal), "m", func(string, string) error { forwarded++; return nil })
			if err != nil || forwarded != 7 || response.OutputText != "answer" {
				t.Fatalf("err=%v forwarded=%d text=%q", err, forwarded, response.OutputText)
			}
			if len(response.Output) != 4 || len(response.Output[1].Content) != 3 || response.Output[1].ID != "refusal-item" || response.Output[1].Content[2].Type != "refusal" || response.Output[1].Content[2].Refusal != "cannot help" || response.Output[2].Arguments != "{}" {
				t.Fatalf("output=%+v", response.Output)
			}
		})
	}
}

func TestNativeResponseRefusalBoundsContentIndex(t *testing.T) {
	for _, index := range []string{"-1", "0.5", `"1"`, "null", "128", "1e100"} {
		for _, kind := range []string{"response.refusal.delta", "response.refusal.done"} {
			payload := fmt.Sprintf(`data: {"type":%q,"content_index":%s,"delta":"no","refusal":"no"}`+"\n\n", kind, index)
			callbacks := 0
			_, err := streamResponseData(strings.NewReader(payload), "m", func(string, string) error { callbacks++; return nil })
			if err == nil || callbacks != 0 {
				t.Fatalf("kind=%s index=%s err=%v callbacks=%d", kind, index, err, callbacks)
			}
		}
	}
	response, err := streamResponseData(strings.NewReader("data: {\"type\":\"response.refusal.done\",\"content_index\":127,\"refusal\":\"no\"}\n\n"+responseTestTerminal), "m", nil)
	if err != nil || len(response.Output[0].Content) != 128 || response.Output[0].Content[127].Refusal != "no" {
		t.Fatalf("upper bound rejected: err=%v", err)
	}
}

func TestResponseRefusalClearsTextMetadata(t *testing.T) {
	for _, kind := range []string{"response.refusal.delta", "response.refusal.done"} {
		wire := `data: {"type":"response.content_part.done","part":{"type":"output_text","text":"old","annotations":[{"type":"url_citation","url":"https://example.com"}],"logprobs":[{"token":"old","logprob":-1}]}}` + "\n\n"
		wire += fmt.Sprintf(`data: {"type":%q,"delta":"no","refusal":"no"}`+"\n\n", kind)
		response, err := streamResponseData(strings.NewReader(wire+responseTestTerminal), "m", nil)
		if err != nil {
			t.Fatal(err)
		}
		part := response.Output[0].Content[0]
		if part.Type != "refusal" || part.Refusal != "no" || part.Text != "" || len(part.Annotations) != 0 || len(part.Logprobs) != 0 || response.OutputText != "" {
			t.Fatalf("%s retained replaced text metadata: %+v", kind, part)
		}
	}
}
