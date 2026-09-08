package provider

import (
	"fmt"
	"strings"
	"testing"
)

func TestResponseStreamReasoningSummary(t *testing.T) {
	stream := ""
	for _, p := range []string{
		`{"type":"response.reasoning_summary_part.added","output_index":1,"summary_index":0,"item_id":"reason","part":{"type":"summary_text","text":""}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":1,"summary_index":0,"delta":"partial"}`,
		`{"type":"response.reasoning_summary_text.done","output_index":1,"summary_index":0,"text":"first"}`,
		`{"type":"response.reasoning_summary_part.done","output_index":1,"summary_index":1,"part":{"type":"summary_text","text":"second"}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"answer"}`,
	} {
		stream += "data: " + p + "\n\n"
	}
	response, err := streamResponseData(strings.NewReader(stream+responseTestTerminal), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.OutputText != "answer" || len(response.Output) != 2 {
		t.Fatalf("response=%+v", response)
	}
	item := response.Output[1]
	if item.ID != "reason" || item.Type != "reasoning" || len(item.Summary) != 2 || item.Summary[0].Text != "first" || item.Summary[1].Text != "second" || len(item.Content) != 0 {
		t.Fatalf("item=%+v", item)
	}
}

func TestResponseSummaryIndexBounds(t *testing.T) {
	for _, kind := range []string{"response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done"} {
		for _, index := range []string{"-1", "128", "null", "0.5", `"1"`} {
			payload := fmt.Sprintf("data: {\"type\":%q,\"summary_index\":%s,\"part\":{\"type\":\"summary_text\",\"text\":\"x\"},\"delta\":\"x\",\"text\":\"x\"}\n\n", kind, index)
			calls := 0
			_, err := streamResponseData(strings.NewReader(payload+responseTestTerminal), "m", func(string, string) error { calls++; return nil })
			if err == nil || calls != 0 {
				t.Fatalf("kind=%s index=%s err=%v calls=%d", kind, index, err, calls)
			}
		}
	}
}
