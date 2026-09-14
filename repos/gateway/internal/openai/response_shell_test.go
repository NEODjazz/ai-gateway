package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func validShellOutput() []any {
	return []any{map[string]any{
		"type": "shell_call_output", "call_id": "call_1", "status": "completed", "max_output_length": 4096,
		"caller": map[string]any{"type": "direct"},
		"output": []any{
			map[string]any{"stdout": "hello\n", "stderr": "", "outcome": map[string]any{"type": "exit", "exit_code": 0}},
			map[string]any{"stdout": "", "stderr": "timed out", "outcome": map[string]any{"type": "timeout"}},
		},
	}}
}

func TestResponseShellToolAndOutputValidation(t *testing.T) {
	request := ResponseRequest{
		Tools: []ResponseTool{{
			Type: "shell", AllowedCallers: []string{"direct"},
			Environment: map[string]any{"type": "container_auto", "memory_limit": "4g", "file_ids": []string{"file_owned"}},
		}},
		ToolChoice: map[string]any{"type": "shell"}, Input: validShellOutput(),
	}
	if message := request.Validate(); message != "" {
		t.Fatalf("valid shell request rejected: %s", message)
	}
	outputs, message := InspectResponseShellCallOutputs(request.Input)
	if message != "" || len(outputs) != 1 || outputs[0].CallID != "call_1" {
		t.Fatalf("outputs=%+v message=%q", outputs, message)
	}
	if ResponseInputTokens(request) <= ResponseInputTokens(ResponseRequest{Input: "hello"}) {
		t.Fatal("shell tool and output were omitted from token estimation")
	}
}

func TestResponseShellRejectsInvalidToolDefinitions(t *testing.T) {
	invalid := []ResponseTool{
		{Type: "shell", AllowedCallers: []string{"programmatic"}},
		{Type: "shell", AllowedCallers: []string{"direct", "direct"}},
		{Type: "shell", Environment: "local"},
		{Type: "shell", Environment: map[string]any{"type": "local", "skills": []any{}}},
		{Type: "shell", Environment: map[string]any{"type": "container_reference", "container_id": "bad/id"}},
		{Type: "shell", Environment: map[string]any{"type": "container_auto", "memory_limit": "2g"}},
		{Type: "shell", Environment: map[string]any{"type": "container_auto", "file_ids": []string{"same", "same"}}},
		{Type: "shell", Name: "exec"},
	}
	for _, tool := range invalid {
		if message := (ResponseRequest{Tools: []ResponseTool{tool}}).Validate(); message == "" {
			t.Fatalf("invalid shell tool accepted: %+v", tool)
		}
	}
	if message := (ResponseRequest{Tools: []ResponseTool{{Type: "shell"}, {Type: "shell"}}}).Validate(); message == "" {
		t.Fatal("duplicate shell tools were accepted")
	}
	if message := (ResponseRequest{Tools: []ResponseTool{{Type: "function", Name: "exec", Environment: map[string]any{"type": "local"}}}}).Validate(); message == "" {
		t.Fatal("shell environment was accepted on a function tool")
	}
}

func TestResponseShellRejectsInvalidCallOutputs(t *testing.T) {
	tooLarge := strings.Repeat("x", maxResponseShellOutputBytes+1)
	invalid := []any{
		[]any{map[string]any{"type": "shell_call_output", "output": []any{map[string]any{"stdout": "", "stderr": "", "outcome": map[string]any{"type": "exit", "exit_code": 0}}}}},
		[]any{map[string]any{"type": "shell_call_output", "call_id": "call", "extra": true, "output": []any{map[string]any{"stdout": "", "stderr": "", "outcome": map[string]any{"type": "exit", "exit_code": 0}}}}},
		[]any{map[string]any{"type": "shell_call_output", "call_id": "call", "output": []any{}}},
		[]any{map[string]any{"type": "shell_call_output", "call_id": "call", "output": []any{map[string]any{"stdout": "", "outcome": map[string]any{"type": "exit", "exit_code": 0}}}}},
		[]any{map[string]any{"type": "shell_call_output", "call_id": "call", "output": []any{map[string]any{"stdout": tooLarge, "stderr": "", "outcome": map[string]any{"type": "exit", "exit_code": 0}}}}},
		[]any{map[string]any{"type": "shell_call_output", "call_id": "call", "output": []any{map[string]any{"stdout": "", "stderr": "", "outcome": map[string]any{"type": "exit", "exit_code": 1.5}}}}},
		[]any{map[string]any{"type": "shell_call_output", "call_id": "call", "output": []any{map[string]any{"stdout": "", "stderr": "", "outcome": map[string]any{"type": "signal"}}}}},
		[]any{map[string]any{"type": "shell_call_output", "call_id": "call", "caller": map[string]any{"type": "program"}, "output": []any{map[string]any{"stdout": "", "stderr": "", "outcome": map[string]any{"type": "timeout"}}}}},
	}
	for index, input := range invalid {
		if _, message := InspectResponseShellCallOutputs(input); message == "" {
			t.Fatalf("invalid shell output %d accepted", index)
		}
	}
}

func TestResponseShellTextProjectionAndTransformation(t *testing.T) {
	item := ResponseOutputItem{
		Type: "shell_call", Action: json.RawMessage(`{"commands":["echo {{EMAIL_1}}"],"timeout_ms":1000}`),
	}
	if text := ResponseShellText(item); text != "echo {{EMAIL_1}}" {
		t.Fatalf("text=%q", text)
	}
	TransformResponseShellText(&item, func(value string) string { return strings.ReplaceAll(value, "{{EMAIL_1}}", "user@example.com") })
	if text := ResponseShellText(item); text != "echo user@example.com" {
		t.Fatalf("transformed text=%q action=%s", text, item.Action)
	}
	output := ResponseOutputItem{Type: "shell_call_output", Output: json.RawMessage(`[{"stdout":"{{EMAIL_1}}","stderr":"","outcome":{"type":"exit","exit_code":0}}]`)}
	TransformResponseShellText(&output, func(value string) string { return strings.ReplaceAll(value, "{{EMAIL_1}}", "user@example.com") })
	if text := ResponseShellText(output); text != "user@example.com\n" {
		t.Fatalf("output text=%q output=%s", text, output.Output)
	}
}
