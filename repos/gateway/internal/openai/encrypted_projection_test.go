package openai

import (
	"reflect"
	"strings"
	"testing"
)

func TestEncryptedReasoningTextTransforms(t *testing.T) {
	input := func() map[string]any {
		return map[string]any{"type": "reasoning", "id": "r", "encrypted_content": "opaque1234567890", "summary": []any{map[string]any{"type": "summary_text", "text": "user@example.com"}}}
	}
	transformed := TransformTextContent(input(), func(string) string { return "MASKED" }).(map[string]any)
	if transformed["encrypted_content"] != "opaque1234567890" || transformed["summary"].([]any)[0].(map[string]any)["text"] != "MASKED" {
		t.Fatalf("transform=%v", transformed)
	}
	original := input()
	projection := TextOnlyProjection(original).(map[string]any)
	if _, exists := projection["encrypted_content"]; exists {
		t.Fatal("opaque encrypted context sent to text processor")
	}
	if projection["summary"].([]any)[0].(map[string]any)["text"] != "user@example.com" {
		t.Fatal("summary missing")
	}
	projection["encrypted_content"] = "rewritten"
	projection["summary"].([]any)[0].(map[string]any)["text"] = "MASKED"
	merged := MergeTextProjection(original, projection).(map[string]any)
	if merged["encrypted_content"] != "opaque1234567890" || merged["summary"].([]any)[0].(map[string]any)["text"] != "MASKED" {
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
