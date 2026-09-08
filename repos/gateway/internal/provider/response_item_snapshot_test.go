package provider

import (
	"strings"
	"testing"
)

func TestResponseStreamItemSnapshotReplacesAccumulatedItem(t *testing.T) {
	for _, kind := range []string{"response.output_item.added", "response.output_item.done"} {
		t.Run(kind, func(t *testing.T) {
			stream := "data: {\"type\":\"response.in_progress\",\"response\":{\"output_text\":\"obsolete\"}}\n\n" + "data: {\"type\":\"response.output_text.delta\",\"delta\":\"obsolete\"}\n\n" +
				"data: {\"type\":\"" + kind + "\",\"item\":{\"id\":\"tool\",\"type\":\"function_call\",\"name\":\"lookup\",\"call_id\":\"call\",\"arguments\":\"{}\"}}\n\n"
			result, err := streamResponseData(strings.NewReader(stream+responseTestTerminal), "m", nil)
			if err != nil {
				t.Fatal(err)
			}
			item := result.Output[0]
			if result.OutputText != "" || item.ID != "tool" || item.Type != "function_call" || item.Name != "lookup" || item.CallID != "call" || item.Arguments != "{}" || item.Role != "" || len(item.Content) != 0 || item.Status != "" {
				t.Fatalf("stale snapshot fields: result=%+v", result)
			}
		})
	}
}
