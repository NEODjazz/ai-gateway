package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func validApplyPatchOutput() []any {
	return []any{map[string]any{
		"type": "apply_patch_call_output", "call_id": "call_1", "status": "completed",
		"caller": map[string]any{"type": "direct"}, "output": "Done",
	}}
}

func TestResponseApplyPatchToolAndOutputValidation(t *testing.T) {
	request := ResponseRequest{
		Tools:      []ResponseTool{{Type: "apply_patch", AllowedCallers: []string{"direct"}}},
		ToolChoice: map[string]any{"type": "apply_patch"}, Input: validApplyPatchOutput(),
	}
	if message := request.Validate(); message != "" {
		t.Fatalf("valid apply patch request rejected: %s", message)
	}
	outputs, message := InspectResponseApplyPatchCallOutputs(request.Input)
	if message != "" || len(outputs) != 1 || outputs[0].CallID != "call_1" {
		t.Fatalf("outputs=%+v message=%q", outputs, message)
	}
	if ResponseInputTokens(request) <= ResponseInputTokens(ResponseRequest{Input: "Done"}) {
		t.Fatal("apply patch tool and output were omitted from token estimation")
	}
}

func TestResponseApplyPatchRejectsInvalidDefinitionsAndOutputs(t *testing.T) {
	for _, tool := range []ResponseTool{
		{Type: "apply_patch", AllowedCallers: []string{"programmatic"}},
		{Type: "apply_patch", AllowedCallers: []string{"direct", "direct"}},
		{Type: "apply_patch", Environment: map[string]any{"type": "local"}},
		{Type: "apply_patch", Name: "patch"},
	} {
		if message := (ResponseRequest{Tools: []ResponseTool{tool}}).Validate(); message == "" {
			t.Fatalf("invalid apply patch tool accepted: %+v", tool)
		}
	}
	if message := (ResponseRequest{Tools: []ResponseTool{{Type: "apply_patch"}, {Type: "apply_patch"}}}).Validate(); message == "" {
		t.Fatal("duplicate apply patch tools were accepted")
	}
	invalid := []any{
		[]any{map[string]any{"type": "apply_patch_call_output", "call_id": "call", "output": "Done"}},
		[]any{map[string]any{"type": "apply_patch_call_output", "call_id": "call", "status": "pending"}},
		[]any{map[string]any{"type": "apply_patch_call_output", "call_id": "call", "status": "completed", "caller": map[string]any{"type": "program", "caller_id": "program"}}},
		[]any{map[string]any{"type": "apply_patch_call_output", "call_id": "call", "status": "completed", "extra": true}},
		[]any{map[string]any{"type": "apply_patch_call_output", "call_id": "call", "status": "completed", "output": strings.Repeat("x", maxResponseApplyPatchOutputBytes+1)}},
	}
	for index, input := range invalid {
		if _, message := InspectResponseApplyPatchCallOutputs(input); message == "" {
			t.Fatalf("invalid apply patch output %d accepted", index)
		}
	}
}

func TestResponseApplyPatchCallAndTextValidation(t *testing.T) {
	item := ResponseOutputItem{
		Type: "apply_patch_call", ID: "patch_1", CallID: "call_1", Status: "completed",
		Caller:    json.RawMessage(`{"type":"direct"}`),
		Operation: json.RawMessage(`{"type":"update_file","path":"docs/user@example.com.md","diff":"@@ -1 +1 @@\n-old\n+new"}`),
	}
	if message := ValidateResponseApplyPatchCall(item); message != "" {
		t.Fatalf("valid apply patch call rejected: %s", message)
	}
	if text := ResponseApplyPatchText(item); !strings.Contains(text, "user@example.com") || !strings.Contains(text, "+new") {
		t.Fatalf("patch text projection=%q", text)
	}
	TransformResponseApplyPatchText(&item, func(value string) string { return strings.ReplaceAll(value, "user@example.com", "{{EMAIL_1}}") })
	if text := ResponseApplyPatchText(item); strings.Contains(text, "user@example.com") || !strings.Contains(text, "{{EMAIL_1}}") {
		t.Fatalf("transformed patch text=%q", text)
	}
	unsafePath := ResponseOutputItem{Type: "apply_patch_call", Operation: json.RawMessage(`{"type":"delete_file","path":"{{PATH}}"}`)}
	TransformResponseApplyPatchText(&unsafePath, func(string) string { return "../secret" })
	if text := ResponseApplyPatchText(unsafePath); text != "{{PATH}}" {
		t.Fatalf("unsafe transformed path escaped validation: %q", text)
	}

	invalid := []ResponseOutputItem{
		{Type: "apply_patch_call", CallID: "call", Status: "completed", Operation: json.RawMessage(`{"type":"delete_file","path":"../secret"}`)},
		{Type: "apply_patch_call", CallID: "call", Status: "completed", Operation: json.RawMessage(`{"type":"create_file","path":"/absolute","diff":"+x"}`)},
		{Type: "apply_patch_call", CallID: "call", Status: "completed", Operation: json.RawMessage(`{"type":"update_file","path":"file","diff":""}`)},
		{Type: "apply_patch_call", CallID: "call", Status: "incomplete", Operation: json.RawMessage(`{"type":"delete_file","path":"file"}`)},
		{Type: "apply_patch_call", CallID: "call", Status: "completed", Caller: json.RawMessage(`{"type":"program","caller_id":"program"}`), Operation: json.RawMessage(`{"type":"delete_file","path":"file"}`)},
	}
	for index, candidate := range invalid {
		if message := ValidateResponseApplyPatchCall(candidate); message == "" {
			t.Fatalf("invalid apply patch call %d accepted", index)
		}
	}
}

func TestResponseApplyPatchStreamingDiffIsBounded(t *testing.T) {
	item := ResponseOutputItem{
		Type: "apply_patch_call", ID: "patch", CallID: "call", Status: "in_progress",
		Operation: json.RawMessage(`{"type":"update_file","path":"file.txt","diff":""}`),
	}
	if message := ValidateResponseApplyPatchCallPartial(item); message != "" {
		t.Fatalf("partial patch rejected: %s", message)
	}
	if message := UpdateResponseApplyPatchDiff(&item, "+first", false); message != "" {
		t.Fatalf("patch delta rejected: %s", message)
	}
	if message := UpdateResponseApplyPatchDiff(&item, "+final", true); message != "" || ResponseApplyPatchText(item) != "file.txt\n+final" {
		t.Fatalf("patch done message=%q operation=%s", message, item.Operation)
	}
	if message := UpdateResponseApplyPatchDiff(&item, strings.Repeat("x", maxResponseApplyPatchDiffBytes+1), false); message == "" {
		t.Fatal("oversized patch delta was accepted")
	}
}
