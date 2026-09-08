package gateway

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestSyntheticResponseStreamPreservesOutputAndUsage(t *testing.T) {
	response := openai.ResponseResponse{ID: "resp", Status: "completed", Model: "m", Usage: openai.ResponseUsage{InputTokens: 7, OutputTokens: 2, TotalTokens: 9}, Output: []openai.ResponseOutputItem{
		{ID: "message", Type: "message", Role: "assistant", Status: "completed", Content: []openai.ResponseOutputContent{{Type: "output_text", Text: "hello"}}},
		{ID: "function", Type: "function_call", CallID: "call", Name: "tool", Arguments: `{"x":1}`, Status: "completed"},
	}}
	var kinds []string
	var final openai.ResponseResponse
	err := synthesizeResponseStream(response, func(kind, payload string) error {
		var data map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			t.Fatal(err)
		}
		var sequence int
		json.Unmarshal(data["sequence_number"], &sequence)
		if sequence != len(kinds) {
			t.Fatalf("sequence=%d", sequence)
		}
		kinds = append(kinds, kind)
		if kind == "response.completed" {
			if err := json.Unmarshal(data["response"], &final); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	want := []string{"response.created", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done", "response.completed"}
	if err != nil || !reflect.DeepEqual(kinds, want) || !reflect.DeepEqual(final, response) {
		t.Fatalf("err=%v events=%v final=%+v", err, kinds, final)
	}
}

func TestSyntheticResponseStreamStatusesAndWriteFailure(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed", "queued"} {
		var kinds []string
		err := synthesizeResponseStream(openai.ResponseResponse{ID: "r", Status: status}, func(kind, _ string) error { kinds = append(kinds, kind); return nil })
		if status == "queued" {
			if err == nil || len(kinds) != 0 {
				t.Fatal("non-terminal response emitted events")
			}
			continue
		}
		if err != nil || kinds[len(kinds)-1] != "response."+status {
			t.Fatalf("status=%s events=%v err=%v", status, kinds, err)
		}
	}
	stopped := errors.New("client disconnected")
	calls := 0
	err := synthesizeResponseStream(openai.ResponseResponse{ID: "r", Status: "completed", OutputText: "hello"}, func(string, string) error { calls++; return stopped })
	if !errors.Is(err, stopped) || calls != 1 {
		t.Fatalf("err=%v writes=%d", err, calls)
	}
}
