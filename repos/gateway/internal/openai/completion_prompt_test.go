package openai

import (
	"reflect"
	"testing"
)

func TestInspectCompletionPromptAcceptsAllWireShapes(t *testing.T) {
	tests := []struct {
		name       string
		prompt     any
		kind       CompletionPromptKind
		count      int
		tokenCount int
	}{
		{name: "omitted", prompt: nil, kind: CompletionPromptText, count: 1},
		{name: "string", prompt: "hello", kind: CompletionPromptText, count: 1},
		{name: "strings", prompt: []any{"one", "two"}, kind: CompletionPromptTexts, count: 2},
		{name: "tokens", prompt: []any{1.0, 2.0, 3.0}, kind: CompletionPromptTokens, count: 1, tokenCount: 3},
		{name: "token arrays", prompt: []any{[]any{1.0, 2.0}, []any{3.0}}, kind: CompletionPromptTokenArrays, count: 2, tokenCount: 3},
		{name: "empty tokens", prompt: []any{}, kind: CompletionPromptTokens, count: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info, err := InspectCompletionPrompt(test.prompt)
			if err != nil {
				t.Fatal(err)
			}
			if info.Kind != test.kind || info.Count != test.count || info.TokenCount != test.tokenCount {
				t.Fatalf("unexpected prompt info: %+v", info)
			}
		})
	}
}

func TestInspectCompletionPromptRejectsInvalidAndUnboundedShapes(t *testing.T) {
	tooMany := make([]string, 129)
	for _, prompt := range []any{
		42,
		[]any{"text", 1},
		[]any{1, -1},
		[]any{1, 1.5},
		[]any{[]any{1}, "text"},
		[]any{4294967295},
		tooMany,
	} {
		if _, err := InspectCompletionPrompt(prompt); err == nil {
			t.Fatalf("invalid prompt accepted: %#v", prompt)
		}
	}
}

func TestCompletionPromptPolicyRoundTripPreservesTextBoundariesAndTokens(t *testing.T) {
	textPrompt := []any{"first@example.com", "second@example.com"}
	content := CompletionPromptPolicyContent(textPrompt).([]any)
	content[0] = "{{EMAIL_1}}"
	content[1] = "{{EMAIL_2}}"
	updated, err := ApplyCompletionPromptPolicyContent(textPrompt, content)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated, []string{"{{EMAIL_1}}", "{{EMAIL_2}}"}) {
		t.Fatalf("text prompt boundaries changed: %#v", updated)
	}
	tokens := []any{1.0, 2.0}
	updated, err = ApplyCompletionPromptPolicyContent(tokens, "changed")
	if err != nil || !reflect.DeepEqual(updated, tokens) {
		t.Fatalf("token prompt changed: %#v err=%v", updated, err)
	}
}

func TestCompletionChoiceCountBoundsPromptProduct(t *testing.T) {
	n := 64
	count, err := CompletionChoiceCount([]string{"one", "two"}, &n)
	if err != nil || count != 128 {
		t.Fatalf("unexpected choice count: count=%d err=%v", count, err)
	}
	n = 65
	if _, err := CompletionChoiceCount([]string{"one", "two"}, &n); err == nil {
		t.Fatal("unbounded choice product was accepted")
	}
	n = intMax()
	if _, err := CompletionChoiceCount([]string{"one", "two"}, &n); err == nil {
		t.Fatal("overflowing choice count was accepted")
	}
}
