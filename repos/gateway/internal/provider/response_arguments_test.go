package provider

import (
	"strings"
	"testing"
)

func TestResponseStreamFinalFunctionArguments(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(map[bool]string{false: "data_type", true: "event_name"}[named], func(t *testing.T) {
			var stream strings.Builder
			events := []struct{ kind, payload string }{
				{"response.output_item.added", `"output_index":0,"item":{"type":"function_call","id":"first","call_id":"call-first","name":"lookup","arguments":""}`},
				{"response.function_call_arguments.delta", `"output_index":0,"item_id":"first","delta":"partial"`},
				{"response.function_call_arguments.delta", `"output_index":1,"item_id":"second","delta":"other"`},
				{"response.function_call_arguments.done", `"output_index":0,"item_id":"first","arguments":"{\"city\":\"Paris\"}"`},
				{"response.function_call_arguments.done", `"output_index":1,"item_id":"second","arguments":"{}"`},
			}
			for _, event := range events {
				if named {
					stream.WriteString("event: " + event.kind + "\n")
				}
				stream.WriteString("data: {\"type\":\"" + event.kind + "\"," + event.payload + "}\n\n")
			}
			forwarded := 0
			response, err := streamResponseData(strings.NewReader(stream.String()+responseTestTerminal), "m", func(string, string) error { forwarded++; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if forwarded != 6 || len(response.Output) != 2 || response.OutputText != "" {
				t.Fatalf("response=%+v forwarded=%d", response, forwarded)
			}
			a, b := response.Output[0], response.Output[1]
			if a.Arguments != `{"city":"Paris"}` || a.ID != "first" || a.CallID != "call-first" || a.Name != "lookup" || b.Arguments != "{}" || b.ID != "second" || b.Type != "function_call" {
				t.Fatalf("output=%+v", response.Output)
			}
		})
	}
}

func TestResponseStreamFunctionDoneWithoutDeltas(t *testing.T) {
	response, err := streamResponseData(strings.NewReader("data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"tool\",\"arguments\":\"{}\"}\n\n"+responseTestTerminal), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	item := response.Output[0]
	if item.ID != "tool" || item.Type != "function_call" || item.Arguments != "{}" || len(item.Content) != 0 || item.Role != "" {
		t.Fatalf("item=%+v", item)
	}
}

func TestResponseStreamFinalCustomToolInput(t *testing.T) {
	events := "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"custom_tool_call\",\"id\":\"custom\",\"call_id\":\"call-custom\",\"name\":\"query\",\"input\":\"\"}}\n\n" +
		"data: {\"type\":\"response.custom_tool_call_input.delta\",\"output_index\":0,\"item_id\":\"custom\",\"delta\":\"partial\"}\n\n" +
		"data: {\"type\":\"response.custom_tool_call_input.done\",\"output_index\":0,\"item_id\":\"custom\",\"input\":\"status:open\"}\n\n"
	response, err := streamResponseData(strings.NewReader(events+responseTestTerminal), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 1 {
		t.Fatalf("output=%+v", response.Output)
	}
	item := response.Output[0]
	if item.ID != "custom" || item.CallID != "call-custom" || item.Name != "query" || item.Type != "custom_tool_call" || item.Input != "status:open" {
		t.Fatalf("item=%+v", item)
	}
}
