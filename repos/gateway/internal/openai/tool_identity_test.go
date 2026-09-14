package openai

import "testing"

func TestToolIdentitySurvivesTextProcessing(t *testing.T) {
	for _, key := range []string{"call_id", "tool_call_id", "created_by"} {
		input := func() map[string]any {
			return map[string]any{"type": "function_call_output", key: "call1234567890", "output": "sensitive text"}
		}
		transformed := TransformTextContent(input(), func(string) string { return "MASKED" }).(map[string]any)
		if transformed[key] != "call1234567890" || transformed["output"] != "MASKED" {
			t.Fatalf("key=%s transformed=%v", key, transformed)
		}
		projected := TextOnlyProjection(input()).(map[string]any)
		projected[key] = "changed-id"
		projected["output"] = "MASKED"
		merged := MergeTextProjection(input(), projected).(map[string]any)
		if merged[key] != "call1234567890" || merged["output"] != "MASKED" {
			t.Fatalf("key=%s merged=%v", key, merged)
		}
	}
}
