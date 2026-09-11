package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInteractionStreamTransformsFunctionAndThoughtSteps(t *testing.T) {
	transformer := newInteractionStreamTransformer()
	tests := []struct {
		event   string
		payload string
		want    string
	}{
		{"response.output_item.added", `{"type":"response.output_item.added","output_index":1,"item":{"id":"reason","type":"reasoning"}}`, `"type":"thought"`},
		{"response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","output_index":1,"delta":"summary"}`, `"type":"thought_summary"`},
		{"response.refusal.delta", `{"type":"response.refusal.delta","output_index":0,"delta":"declined"}`, `"text":"declined"`},
		{"response.output_item.done", `{"type":"response.output_item.done","output_index":1,"item":{"id":"reason","type":"reasoning"}}`, `"event_type":"step.stop"`},
		{"response.output_item.added", `{"type":"response.output_item.added","output_index":2,"item":{"id":"call","call_id":"call_1","type":"function_call","name":"weather"}}`, `"name":"weather"`},
		{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{"}`, `"type":"arguments_delta"`},
	}
	for _, test := range tests {
		events, err := transformer.Transform(test.event, test.payload)
		if err != nil || len(events) != 1 || !json.Valid([]byte(events[0].Payload)) || !strings.Contains(events[0].Payload, test.want) {
			t.Fatalf("event=%s transformed=%+v err=%v", test.event, events, err)
		}
	}
}

func TestInteractionStreamSkipsResponseSnapshots(t *testing.T) {
	transformer := newInteractionStreamTransformer()
	for _, event := range []string{"response.content_part.added", "response.content_part.done", "response.output_text.done", "response.output_text.annotation.added", "response.refusal.done", "response.function_call_arguments.done", "response.reasoning_summary_part.added", "response.reasoning_summary_text.done", "response.reasoning_summary_part.done"} {
		payload := `{"type":"` + event + `"}`
		events, err := transformer.Transform(event, payload)
		if err != nil || len(events) != 0 {
			t.Fatalf("event=%s transformed=%+v err=%v", event, events, err)
		}
	}
}

func TestInteractionStreamRejectsUnknownMalformedAndMismatchedEvents(t *testing.T) {
	transformer := newInteractionStreamTransformer()
	for _, input := range [][2]string{{"unknown", `{}`}, {"response.created", `{`}, {"response.created", `{"type":"response.completed"}`}, {"response.output_item.added", `{"type":"response.output_item.added","item":{"type":"image_generation_call"}}`}, {"response.output_text.delta", `{"type":"response.output_text.delta","output_index":1024}`}} {
		if _, err := transformer.Transform(input[0], input[1]); err == nil {
			t.Fatalf("accepted event=%s payload=%s", input[0], input[1])
		}
	}
}
