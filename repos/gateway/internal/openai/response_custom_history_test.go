package openai

import (
	"slices"
	"testing"
)

func TestInspectResponseCustomToolHistory(t *testing.T) {
	for _, test := range []struct {
		name       string
		input      any
		wantNames  []string
		wantCustom bool
		wantError  bool
	}{
		{name: "text", input: "hello"},
		{name: "named call and output", input: []any{map[string]any{"type": "custom_tool_call", "call_id": "call_1", "name": "apply_patch", "input": "patch"}, map[string]any{"type": "custom_tool_call_output", "call_id": "call_1", "output": "ok"}}, wantNames: []string{"apply_patch"}, wantCustom: true},
		{name: "output only", input: []any{map[string]any{"type": "custom_tool_call_output", "call_id": "call_1", "output": "ok"}}, wantCustom: true},
		{name: "invalid name", input: []any{map[string]any{"type": "custom_tool_call", "call_id": "call_1", "name": "bad name"}}, wantError: true},
		{name: "missing call id", input: []any{map[string]any{"type": "custom_tool_call_output", "output": "ok"}}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			names, custom, message := InspectResponseCustomToolHistory(test.input)
			if !slices.Equal(names, test.wantNames) || custom != test.wantCustom || (message != "") != test.wantError {
				t.Fatalf("names=%v custom=%t message=%q", names, custom, message)
			}
		})
	}
}

func TestInspectResponseFunctionToolHistory(t *testing.T) {
	for _, test := range []struct {
		name      string
		input     any
		wantNames []string
		wantTool  bool
		wantError bool
	}{
		{name: "named call and output", input: []any{map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": "{}"}, map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "ok"}}, wantNames: []string{"lookup"}, wantTool: true},
		{name: "output only", input: []any{map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "ok"}}, wantTool: true},
		{name: "invalid name", input: []any{map[string]any{"type": "function_call", "call_id": "call_1", "name": "bad name"}}, wantError: true},
		{name: "missing call id", input: []any{map[string]any{"type": "function_call_output", "output": "ok"}}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			names, hasTool, message := InspectResponseFunctionToolHistory(test.input)
			if !slices.Equal(names, test.wantNames) || hasTool != test.wantTool || (message != "") != test.wantError {
				t.Fatalf("names=%v hasTool=%t message=%q", names, hasTool, message)
			}
		})
	}
}
