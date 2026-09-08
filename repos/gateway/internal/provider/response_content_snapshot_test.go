package provider

import (
	"fmt"
	"strings"
	"testing"
)

func TestResponseStreamContentSnapshots(t *testing.T) {
	for _, kind := range []string{"response.content_part.added", "response.content_part.done"} {
		t.Run(kind, func(t *testing.T) {
			stream := fmt.Sprintf("data: {\"type\":%q,\"output_index\":1,\"content_index\":1,\"item_id\":\"message\",\"part\":{\"type\":\"output_text\",\"text\":\"answer\"}}\n\n", kind) +
				fmt.Sprintf("data: {\"type\":%q,\"output_index\":1,\"content_index\":0,\"item_id\":\"message\",\"part\":{\"type\":\"refusal\",\"refusal\":\"no\"}}\n\n", kind)
			result, err := streamResponseData(strings.NewReader(stream+responseTestTerminal), "m", nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Output) != 2 || result.OutputText != "answer" {
				t.Fatalf("result=%+v", result)
			}
			item := result.Output[1]
			if item.ID != "message" || item.Type != "message" || len(item.Content) != 2 || item.Content[0].Refusal != "no" || item.Content[1].Text != "answer" {
				t.Fatalf("item=%+v", item)
			}
		})
	}
}

func TestResponseStreamContentSnapshotValidation(t *testing.T) {
	for _, payload := range []string{
		`"content_index":128,"part":{"type":"output_text"}`,
		`"content_index":-1,"part":{"type":"output_text"}`,
		`"content_index":null,"part":{"type":"output_text"}`,
		`"part":null`, `"part":"invalid"`, `"part":{"text":123}`,
	} {
		calls := 0
		_, err := streamResponseData(strings.NewReader("data: {\"type\":\"response.content_part.done\","+payload+"}\n\n"+responseTestTerminal), "m", func(string, string) error { calls++; return nil })
		if err == nil || calls != 0 {
			t.Fatalf("payload=%s err=%v calls=%d", payload, err, calls)
		}
	}
}

func TestResponseStreamContentSnapshotReplacesDelta(t *testing.T) {
	stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"obsolete\"}\n\n" +
		"data: {\"type\":\"response.content_part.done\",\"part\":{\"type\":\"refusal\",\"refusal\":\"no\"}}\n\n"
	result, err := streamResponseData(strings.NewReader(stream+responseTestTerminal), "m", nil)
	if err != nil || result.OutputText != "" || result.Output[0].Content[0].Text != "" || result.Output[0].Content[0].Refusal != "no" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
