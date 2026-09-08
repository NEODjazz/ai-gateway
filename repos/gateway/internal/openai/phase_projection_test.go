package openai

import (
	"reflect"
	"strings"
	"testing"
)

func TestMessagePhaseTextTransforms(t *testing.T) {
	input := func() map[string]any {
		return map[string]any{"type": "message", "id": "r", "phase": "commentary", "content": []any{map[string]any{"type": "output_text", "text": "user@example.com"}}}
	}
	transformed := TransformTextContent(input(), func(string) string { return "MASKED" }).(map[string]any)
	if transformed["phase"] != "commentary" || transformed["content"].([]any)[0].(map[string]any)["text"] != "MASKED" {
		t.Fatalf("transform=%v", transformed)
	}
	original := input()
	projection := TextOnlyProjection(original).(map[string]any)
	if _, exists := projection["phase"]; exists {
		t.Fatal("message phase sent to text processor")
	}
	if projection["content"].([]any)[0].(map[string]any)["text"] != "user@example.com" {
		t.Fatal("content missing")
	}
	projection["phase"] = "rewritten"
	projection["content"].([]any)[0].(map[string]any)["text"] = "MASKED"
	merged := MergeTextProjection(original, projection).(map[string]any)
	if merged["phase"] != "commentary" || merged["content"].([]any)[0].(map[string]any)["text"] != "MASKED" {
		t.Fatalf("merge=%v", merged)
	}
	if !reflect.DeepEqual(original, input()) {
		t.Fatal("projection mutated original")
	}
	restored := TransformTextContent(merged, func(s string) string { return strings.ReplaceAll(s, "MASKED", "user@example.com") }).(map[string]any)
	if !reflect.DeepEqual(restored, input()) {
		t.Fatalf("round trip=%v", restored)
	}
}
