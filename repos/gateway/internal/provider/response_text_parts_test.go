package provider

import (
	"fmt"
	"strings"
	"testing"
)

func TestResponseStreamTextParts(t *testing.T) {
	stream := ""
	for _, payload := range []string{
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"item_id":"second","delta":"C"}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":1,"item_id":"first","delta":"B"}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"first","delta":"wrong"}`,
		`{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"A"}`,
	} {
		stream += "data: " + payload + "\n\n"
	}
	response, err := streamResponseData(strings.NewReader(stream+responseTestTerminal), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.OutputText != "ABC" || len(response.Output) != 2 {
		t.Fatalf("response=%+v", response)
	}
	if response.Output[0].ID != "first" || response.Output[1].ID != "second" || len(response.Output[0].Content) != 2 || response.Output[0].Content[0].Text != "A" || response.Output[0].Content[1].Text != "B" || response.Output[1].Content[0].Text != "C" {
		t.Fatalf("output=%+v", response.Output)
	}
}

func TestResponseStreamTextSnapshot(t *testing.T) {
	for _, tc := range []struct{ output, want string }{
		{`[]`, ""},
		{`[{"type":"message","content":[{"type":"output_text","text":"final"},{"type":"output_text","text":" answer"}]}]`, "final answer"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"obsolete\"}\n\n" + fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"output\":%s}}\n\n", tc.output)
			response, err := streamResponseData(strings.NewReader(stream), "m", nil)
			if err != nil || response.OutputText != tc.want {
				t.Fatalf("text=%q err=%v", response.OutputText, err)
			}
		})
	}
}

func TestResponseStreamTextBounds(t *testing.T) {
	for _, kind := range []string{"response.output_text.delta", "response.output_text.done"} {
		for _, index := range []string{"-1", "0.5", "null", `"1"`, "128", "1e100"} {
			calls := 0
			stream := fmt.Sprintf("data: {\"type\":%q,\"content_index\":%s,\"delta\":\"x\",\"text\":\"x\"}\n\n", kind, index)
			_, err := streamResponseData(strings.NewReader(stream+responseTestTerminal), "m", func(string, string) error { calls++; return nil })
			if err == nil || calls != 0 {
				t.Fatalf("kind=%s index=%s err=%v calls=%d", kind, index, err, calls)
			}
		}
	}
}

func TestResponseJSONCombinesTextParts(t *testing.T) {
	response, err := decodeResponseJSON(strings.NewReader(`{"output":[{"type":"message","content":[{"type":"output_text","text":"A"},{"type":"refusal","refusal":"ignored"},{"type":"output_text","text":"B"}]},{"type":"message","content":[{"type":"output_text","text":"C"}]}]}`))
	if err != nil || response.OutputText != "ABC" {
		t.Fatalf("text=%q err=%v", response.OutputText, err)
	}
}
